// Package tui holds the Bubble Tea application. Phase 1 is the read-only
// dashboard: config-driven sections of PRs/issues from GraphQL, cycled with
// tab, navigated with j/k, refreshed with r.
package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/cli/go-gh/v2/pkg/repository"
	"github.com/dotnetemmanuel/cairn/internal/config"
	"github.com/dotnetemmanuel/cairn/internal/conflict"
	"github.com/dotnetemmanuel/cairn/internal/gh"
	"github.com/dotnetemmanuel/cairn/internal/stack"
	"github.com/dotnetemmanuel/cairn/internal/theme"
)

// searchLimit caps items fetched per section (the total match count is shown
// separately, so capping is visible rather than silent).
const searchLimit = 50

// group is one labeled list within a grouped section (e.g. "ASSIGNED TO ME"),
// holding the open PRs that matched its sub-query after cross-group dedup.
type group struct {
	title string
	items []gh.Item
}

// section is one board tab. Most sections are a single search filter (with an
// OPEN/CLOSED split); a notifications section pulls the REST feed; a grouped
// section (typ == SectionInvolved) renders several labeled sub-query lists
// instead of the open/closed split.
type section struct {
	title       string
	typ         string
	filter      string
	showClosed  bool // include the recently-closed tail for this section
	closedLimit int  // cap for that tail
	list        list.Model
	loading     bool
	loaded      bool
	err         error
	total       int

	// Raw matches kept apart from the list so the groups can be re-folded without
	// re-fetching: rebuildRows reassembles list items from these plus the fold
	// state. A plain section uses open/closed; a grouped one uses groups.
	open   []gh.Item
	closed []gh.Item
	groups []group

	// Notification feed for the Notifications inbox (typ == SectionNotifications),
	// split UNREAD/READ; the left pane's rows are built from these.
	notifUnread []gh.Notification
	notifRead   []gh.Notification

	// collapsed records each foldable group's fold state, keyed by header label
	// (e.g. "OPEN", "ASSIGNED TO ME"). Persists across refresh + section switches
	// within a session; reset (expanded) on each launch.
	collapsed map[string]bool
}

// grouped reports whether the section renders labeled sub-query lists rather than
// the OPEN/CLOSED split.
func (s *section) grouped() bool {
	return s.typ == config.SectionInvolved || s.typ == config.SectionOrgs
}

// isNotif reports whether the section is the Notifications inbox (two-pane view).
func (s *section) isNotif() bool { return s.typ == config.SectionNotifications }

// rebuildRows reassembles the section's list rows from its stored matches and
// current fold state. Called on load, whenever a group is toggled, and when the
// sort order flips. sortByRepo groups each list by repo (the Notifications inbox
// ignores it, keeping its unread/read split).
func (s *section) rebuildRows(sortByRepo bool) {
	switch {
	case s.isNotif():
		s.list.SetItems(notifRows(s.notifUnread, s.notifRead, s.collapsed))
	case s.grouped():
		s.list.SetItems(groupedRows(s.groups, s.collapsed, sortByRepo))
	default:
		s.list.SetItems(sectionRows(s.open, s.closed, s.collapsed, sortByRepo))
	}
}

// appMode selects which screen is active.
type appMode int

const (
	modeDashboard appMode = iota
	modeDetail
	modeStack
	modeConflict
)

// Model is the root Bubble Tea model.
type Model struct {
	cfg       config.Config
	th        theme.Theme
	themeMode string // "dark" | "light"; toggled with ctrl+t

	width  int
	height int

	mode      appMode
	detail    detailModel
	stackMode stackModel
	conflict  conflictModel

	sections []section
	active   int

	// sortByRepo flips every PR-list tab between the default newest-updated order
	// and a per-repo grouping (foldable repo subheaders); toggled with `o`. Global
	// so the ordering is consistent as you move between tabs.
	sortByRepo bool

	// Stack tree: remote stacks reconstructed from loaded PRs (any repo), plus
	// the local git-town tree for the cwd repo (drift-aware overlay).
	stacks     []*stack.Tree
	localStack *stack.Tree
	localRepo  string // owner/name of the cwd repo, "" if none
	showStack  bool
	showHelp   bool
	helpVP     viewport.Model // scrollable body of the help overlay

	spinner spinner.Model

	// header state
	headerLoading bool
	login         string
	rate          int
	limit         int
	headerErr     error

	// flash is a transient status line shown in the header (overriding the session
	// line) — e.g. while auto-syncing the board after you post on a PR. Cleared on
	// a timer so the user sees what's happening without it sticking.
	flash string

	// notifPrev is the Notifications inbox preview-pane state: the thread currently
	// previewed, whether its content is loading, and a per-thread content cache so
	// arrowing back to a row is instant.
	notifPrev notifPreview

	// notifArmed gates the inbox's →-focuses-preview behavior: it stays false when
	// you first land on the Notifications tab (so ←/→ keep navigating tabs) and
	// flips true once you engage the list with ↑/↓, matching the intuition that you
	// step into the list before → reaches over to the preview pane.
	notifArmed bool
}

// notifPreview holds the inbox preview pane's state: the thread currently shown,
// the thread whose conversation is loaded into the scroll viewport, whether a
// fetch is in flight, and a per-thread cache of fetched PR conversations so
// arrowing back is instant. The preview reuses the detail screen's conversation
// renderer (read-only); enter opens the same PR in the full interactive detail.
type notifPreview struct {
	threadID   string // selected thread (what the pane should show)
	renderedAs string // render key (thread|theme|width) of what's in vp — re-render when it drifts
	loading    bool
	focused    bool // preview pane has focus → arrows/j/k scroll it, esc returns to list
	vp         viewport.Model
	cache      map[string]notifConvEntry
}

type notifConvEntry struct {
	detail gh.PRDetail
	err    error
}

// New constructs the root model from loaded config.
func New(cfg config.Config) Model {
	mode := cfg.ThemeMode
	if mode != theme.ModeLight {
		mode = theme.ModeDark
	}
	th := theme.Resolve(mode, cfg.Theme)

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(th.Focus)

	m := Model{
		cfg:           cfg,
		th:            th,
		themeMode:     mode,
		spinner:       sp,
		headerLoading: true,
		showStack:     true,
		notifPrev:     notifPreview{vp: newVP(), cache: map[string]notifConvEntry{}},
	}

	// Read the cwd repo's git-town lineage once (fast, local) for the drift
	// overlay, and resolve its GitHub slug to match against PR repos.
	if t, err := stack.Load(""); err == nil {
		m.localStack = t
	}
	if r, err := repository.Current(); err == nil {
		m.localRepo = r.Owner + "/" + r.Name
	}

	delegate := itemDelegate{th: th}
	for _, s := range cfg.Sections {
		l := list.New(nil, delegate, 0, 0)
		l.SetShowTitle(false)
		l.SetShowStatusBar(false)
		l.SetShowHelp(false)
		l.SetShowPagination(true)
		l.SetFilteringEnabled(false)
		// The list's built-in quit binding maps to both q and esc; disable it so
		// only our app-level q / ctrl+c quit (esc must not exit the dashboard).
		l.DisableQuitKeybindings()
		// Resolve the closed-tail settings: per-section overrides the global, which
		// falls back to the built-in cap.
		showClosed := cfg.ShowClosed
		if s.ShowClosed != nil {
			showClosed = *s.ShowClosed
		}
		limit := s.ClosedLimit
		if limit == 0 {
			limit = cfg.ClosedLimit
		}
		if limit == 0 {
			limit = closedLimit
		}
		m.sections = append(m.sections, section{
			title:       s.Title,
			typ:         s.Type,
			filter:      s.Filter,
			showClosed:  showClosed,
			closedLimit: limit,
			list:        l,
			loading:     true,
			collapsed:   map[string]bool{},
		})
	}
	return m
}

// ---- messages & commands ----

type viewerMsg struct {
	v   gh.Viewer
	err error
}

type sectionLoadedMsg struct {
	idx    int
	items  []gh.Item
	closed []gh.Item // recently closed/merged matches, shown under a divider
	total  int
	err    error
}

func fetchViewer() tea.Msg {
	v, err := gh.FetchViewer()
	return viewerMsg{v: v, err: err}
}

// closedLimit is the built-in fallback cap for the recently-closed tail when
// neither the section nor the global config specifies one.
const closedLimit = 15

// syncAllCmds marks every section (and the header) loading and returns the
// commands to reload them — the whole-board sync shared by manual refresh (r) and
// the auto-sync after you post on a PR. It mutates the receiver via the shared
// section slice + header fields, so callers pass the returned cmds to tea.Batch.
func (m *Model) syncAllCmds() []tea.Cmd {
	cmds := []tea.Cmd{fetchViewer}
	for i := range m.sections {
		s := &m.sections[i]
		s.loading = true
		s.err = nil
		cmds = append(cmds, loadSection(i, s.typ, s.filter, s.showClosed, s.closedLimit))
	}
	m.headerLoading = true
	return cmds
}

// flashClearMsg dismisses the header flash after its display window.
type flashClearMsg struct{}

// clearFlashAfter dismisses the header flash a few seconds out — long enough to
// read "syncing…", short enough not to linger after the sync has landed.
func clearFlashAfter() tea.Cmd {
	return tea.Tick(3*time.Second, func(time.Time) tea.Msg { return flashClearMsg{} })
}

// involvedSpec is one role group of the Involved tab: a label and its search
// sub-query. The base excludes your own PRs (the My PRs tab) and review requests
// (the Needs my review tab) so each PR has a single home — the most-actionable
// tab that applies. Groups are deduped in priority order: Assigned > Mentioned >
// Participating.
type involvedSpec struct {
	title, filter string
}

func involvedSpecs() []involvedSpec {
	const base = "is:open is:pr -author:@me -review-requested:@me"
	return []involvedSpec{
		{"ASSIGNED TO ME", "assignee:@me " + base},
		{"MENTIONED", "mentions:@me " + base},
		{"PARTICIPATING", "commenter:@me " + base},
	}
}

func loadSection(idx int, typ, filter string, showClosed bool, limit int) tea.Cmd {
	return func() tea.Msg {
		if typ == config.SectionNotifications {
			feed, err := gh.FetchNotificationFeed(searchLimit)
			if err != nil {
				return notifFeedMsg{idx: idx, err: err}
			}
			var unread, read []gh.Notification
			for _, n := range feed {
				if n.Unread {
					unread = append(unread, n)
				} else {
					read = append(read, n)
				}
			}
			return notifFeedMsg{idx: idx, unread: unread, read: read}
		}
		if typ == config.SectionInvolved {
			return loadInvolved(idx)
		}
		if typ == config.SectionOrgs {
			return loadOrgs(idx)
		}
		items, total, err := gh.SearchItems(filter, searchLimit)
		if err != nil {
			return sectionLoadedMsg{idx: idx, err: err}
		}
		// A best-effort recent-closed tail (when enabled): failures here don't
		// fail the section — the open list is what matters.
		var closed []gh.Item
		if showClosed && limit > 0 {
			closed, _, _ = gh.SearchItems(closedFilter(filter), limit)
		}
		return sectionLoadedMsg{idx: idx, items: items, closed: closed, total: total}
	}
}

// groupedLoadedMsg carries a grouped section's loaded role groups (Involved).
type groupedLoadedMsg struct {
	idx    int
	groups []group
	total  int
	err    error
}

// notifFeedMsg carries the Notifications inbox feed, pre-split into unread/read.
type notifFeedMsg struct {
	idx          int
	unread, read []gh.Notification
	err          error
}

// notifConvMsg carries a lazily-fetched PR conversation for one thread.
type notifConvMsg struct {
	threadID string
	detail   gh.PRDetail
	err      error
}

// loadInvolved runs each role sub-query in turn, deduping across groups so a PR
// surfaces only in its highest-priority group (Assigned > Mentioned >
// Participating). A failure on any sub-query fails the section.
func loadInvolved(idx int) tea.Msg {
	seen := map[string]bool{}
	var groups []group
	total := 0
	for _, sp := range involvedSpecs() {
		items, _, err := gh.SearchItems(sp.filter, searchLimit)
		if err != nil {
			return groupedLoadedMsg{idx: idx, err: err}
		}
		kept := items[:0:0]
		for _, it := range items {
			key := fmt.Sprintf("%s#%d", it.Repo, it.Number)
			if seen[key] {
				continue
			}
			seen[key] = true
			kept = append(kept, it)
		}
		groups = append(groups, group{title: sp.title, items: kept})
		total += len(kept)
	}
	return groupedLoadedMsg{idx: idx, groups: groups, total: total}
}

// loadOrgs powers the Orgs tab: it resolves the orgs you belong to, then fetches
// every open PR across them — the org's whole open-PR picture, for an overview of
// the company's total in-flight work — one group per org. Your own PRs are
// included (and tinted apart in the list); the My PRs and Involved tabs remain the
// filtered views. A single search spans all orgs; results are bucketed by owner so
// each org reads as its own foldable list.
func loadOrgs(idx int) tea.Msg {
	orgs, err := gh.FetchOrgs()
	if err != nil {
		return groupedLoadedMsg{idx: idx, err: err}
	}
	if len(orgs) == 0 {
		return groupedLoadedMsg{idx: idx} // no orgs → empty tab, not an error
	}

	// Every open PR in the orgs, freshest first — no involvement filter, so the tab
	// is the org's full picture (yours included).
	q := "is:open is:pr sort:updated-desc"
	for _, o := range orgs {
		q += " org:" + o
	}
	items, _, err := gh.SearchItems(q, searchLimit)
	if err != nil {
		return groupedLoadedMsg{idx: idx, err: err}
	}

	groups, total := orgGroups(orgs, items)
	return groupedLoadedMsg{idx: idx, groups: groups, total: total}
}

// orgGroups buckets items into one group per org (in the given order), keyed by
// repo owner case-insensitively — org logins and a repo's nameWithOwner share
// GitHub's canonical case, but we normalize defensively. An org with no matches
// still gets an (empty) group so it reads as "checked, nothing new". Group titles
// are uppercased to match the other section headers (OPEN, ASSIGNED TO ME, …).
func orgGroups(orgs []string, items []gh.Item) (groups []group, total int) {
	byOrg := map[string][]gh.Item{}
	for _, it := range items {
		owner := it.Repo
		if i := strings.IndexByte(owner, '/'); i >= 0 {
			owner = owner[:i]
		}
		key := strings.ToLower(owner)
		byOrg[key] = append(byOrg[key], it)
	}
	groups = make([]group, 0, len(orgs))
	for _, o := range orgs {
		gi := byOrg[strings.ToLower(o)]
		groups = append(groups, group{title: strings.ToUpper(o), items: gi})
		total += len(gi)
	}
	return groups, total
}

// closedFilter turns a section's (open) search into its closed counterpart:
// flip is:open to is:closed (or append it), and pin a recency sort so the
// freshest closed items surface.
func closedFilter(filter string) string {
	fields := strings.Fields(filter)
	flipped, hasSort := false, false
	for i, t := range fields {
		switch {
		case strings.EqualFold(t, "is:open"):
			fields[i] = "is:closed"
			flipped = true
		case strings.HasPrefix(strings.ToLower(t), "sort:"):
			hasSort = true
		}
	}
	if !flipped {
		fields = append(fields, "is:closed")
	}
	if !hasSort {
		fields = append(fields, "sort:updated-desc")
	}
	return strings.Join(fields, " ")
}

// byUpdatedDesc returns items ordered newest-updated first — the board's default
// order, applied client-side so every tab sorts consistently regardless of the
// order GitHub's search returned (some tab queries carry no sort qualifier).
func byUpdatedDesc(items []gh.Item) []gh.Item {
	out := append([]gh.Item(nil), items...)
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out
}

// repoBucket is a set of items sharing one repo (owner/name), used when the sort
// toggle groups a list by repo.
type repoBucket struct {
	repo  string
	items []gh.Item
}

// byRepo groups items into per-repo buckets: within a bucket items are
// newest-updated first, and the buckets are ordered by their freshest item so the
// repo with the most recent activity leads — a "by repo, then chronological" order.
func byRepo(items []gh.Item) []repoBucket {
	idx := map[string]int{}
	var buckets []repoBucket
	for _, it := range items {
		i, ok := idx[it.Repo]
		if !ok {
			i = len(buckets)
			idx[it.Repo] = i
			buckets = append(buckets, repoBucket{repo: it.Repo})
		}
		buckets[i].items = append(buckets[i].items, it)
	}
	for i := range buckets {
		buckets[i].items = byUpdatedDesc(buckets[i].items)
	}
	sort.SliceStable(buckets, func(i, j int) bool {
		return buckets[i].items[0].UpdatedAt.After(buckets[j].items[0].UpdatedAt)
	})
	return buckets
}

// repoKey is the fold-state key for a repo subheader. It is scoped by the parent
// group's title (an org, in the Orgs tab; "" for a flat tab) so the same repo
// name under two orgs folds independently, and prefixed so it never collides with
// a group label like OPEN/CLOSED or an org title.
func repoKey(scope, repo string) string {
	return "repo:" + scope + "/" + repo
}

// repoGroupRows emits foldable per-repo subheaders for items (each repo's PRs
// newest-first, repos ordered by freshest activity), at the given nesting depth.
// scope namespaces the fold keys so the same repo under different parents (an org,
// or the OPEN vs CLOSED split) folds independently. A blank spacer separates
// top-level (depth 0) repo groups; nested groups stay compact.
func repoGroupRows(items []gh.Item, collapsed map[string]bool, scope string, depth int) []list.Item {
	var rows []list.Item
	for i, b := range byRepo(items) {
		if i > 0 && depth == 0 {
			rows = append(rows, sectionHeader{}) // spacer between top-level repo groups
		}
		key := repoKey(scope, b.repo)
		folded := collapsed[key]
		rows = append(rows, sectionHeader{label: shortRepo(b.repo), key: key, collapsible: true, collapsed: folded, count: len(b.items), depth: depth})
		if folded {
			continue
		}
		for _, it := range b.items {
			rows = append(rows, prItem{Item: it, depth: depth + 1}) // one step under the repo subheader
		}
	}
	return rows
}

// sectionRows assembles a flat section's list rows: open items, then any recently
// closed/merged items beneath a divider. Whenever a closed tail is present the
// OPEN/CLOSED structure is shown — including when there are zero open PRs, where a
// muted "nothing open" placeholder sits under the OPEN header — so an all-closed
// section reads as "nothing open + N closed" rather than an unlabeled list that
// looks miscounted. A lone open group (no closed tail) needs no header.
//
// sortByRepo keeps the same OPEN/CLOSED headers but nests foldable per-repo
// subheaders under each (so OPEN never vanishes); open and closed use separate
// fold scopes, so folding open "api" and closed "api" are independent. A lone open
// group with no closed tail groups its repos at the top level (no OPEN header).
// Otherwise items are simply newest-updated first. Fold state lives in collapsed,
// keyed by header (OPEN/CLOSED, or repoKey for a repo subheader).
func sectionRows(open, closed []gh.Item, collapsed map[string]bool, sortByRepo bool) []list.Item {
	rows := make([]list.Item, 0, len(open)+len(closed)+8)
	labeled := len(closed) > 0

	// A lone open group (no closed tail) needs no OPEN/CLOSED split.
	if !labeled {
		if sortByRepo {
			return repoGroupRows(open, collapsed, "", 0)
		}
		for _, it := range byUpdatedDesc(open) {
			rows = append(rows, prItem{Item: it}) // no header above → no indent
		}
		return rows
	}

	// Closed tail present → OPEN then CLOSED, each either a flat newest-first list or
	// nested repo subgroups.
	openCollapsed := collapsed["OPEN"]
	rows = append(rows, sectionHeader{label: "OPEN", key: "OPEN", collapsible: true, collapsed: openCollapsed, count: len(open)})
	if !openCollapsed {
		if len(open) == 0 {
			rows = append(rows, listNote{"nothing open"})
		}
		if sortByRepo {
			rows = append(rows, repoGroupRows(open, collapsed, "open", 1)...)
		} else {
			for _, it := range byUpdatedDesc(open) {
				rows = append(rows, prItem{Item: it, depth: 1}) // under the OPEN header
			}
		}
	}

	// A blank spacer sets the closed group apart from the open list.
	closedCollapsed := collapsed["CLOSED"]
	rows = append(rows, sectionHeader{}, sectionHeader{label: "CLOSED", key: "CLOSED", collapsible: true, collapsed: closedCollapsed, count: len(closed)})
	if !closedCollapsed {
		if sortByRepo {
			rows = append(rows, repoGroupRows(closed, collapsed, "closed", 1)...)
		} else {
			for _, it := range closed {
				rows = append(rows, prItem{Item: it, depth: 1}) // under the CLOSED header
			}
		}
	}
	return rows
}

// groupedRows assembles a grouped section's rows: each group gets a foldable
// header (with its count) followed by its open items, separated by a blank
// spacer. A folded group hides its items; an empty, expanded group shows a muted
// "none" placeholder so it doesn't read as broken. Group titles are unique, so
// the collapsed map keys cleanly by label.
//
// sortByRepo nests a foldable repo subheader (depth 1) under each group, its PRs
// newest-updated first and the repos ordered by freshest activity; the outer
// grouping (org, or involvement role) is preserved. Otherwise a group's items are
// listed flat, newest-updated first.
func groupedRows(groups []group, collapsed map[string]bool, sortByRepo bool) []list.Item {
	rows := make([]list.Item, 0, len(groups)*2)
	for i, g := range groups {
		if i > 0 {
			rows = append(rows, sectionHeader{}) // spacer between groups
		}
		folded := collapsed[g.title]
		rows = append(rows, sectionHeader{label: g.title, key: g.title, collapsible: true, collapsed: folded, count: len(g.items)})
		if folded {
			continue
		}
		if len(g.items) == 0 {
			rows = append(rows, listNote{"none"})
			continue
		}
		if sortByRepo {
			rows = append(rows, repoGroupRows(g.items, collapsed, g.title, 1)...)
			continue
		}
		for _, it := range byUpdatedDesc(g.items) {
			rows = append(rows, prItem{Item: it, depth: 1}) // under the group (org / role) header
		}
	}
	return rows
}

// notifRows assembles the inbox's rows: unread items beneath an UNREAD header,
// then read items beneath a (foldable) READ header — the same collapsible split
// as OPEN/CLOSED. The headers always show so the inbox reads as "N unread / M
// read" even when one side is empty (a muted note fills an empty side).
func notifRows(unread, read []gh.Notification, collapsed map[string]bool) []list.Item {
	rows := make([]list.Item, 0, len(unread)+len(read)+4)
	rows = append(rows, sectionHeader{label: "UNREAD", collapsible: true, collapsed: collapsed["UNREAD"], count: len(unread)})
	if !collapsed["UNREAD"] {
		if len(unread) == 0 {
			rows = append(rows, listNote{"nothing unread"})
		}
		for _, n := range unread {
			rows = append(rows, notifItem{n})
		}
	}
	rows = append(rows, sectionHeader{}, sectionHeader{label: "READ", collapsible: true, collapsed: collapsed["READ"], count: len(read)})
	if !collapsed["READ"] {
		if len(read) == 0 {
			rows = append(rows, listNote{"nothing read"})
		}
		for _, n := range read {
			rows = append(rows, notifItem{n})
		}
	}
	return rows
}

// contentRow reports whether a list row is a real content item (a PR or a
// notification) as opposed to a header/spacer/note — used to rest the cursor on
// something meaningful after a (re)load.
func contentRow(li list.Item) bool {
	switch li.(type) {
	case prItem, notifItem:
		return true
	}
	return false
}

// navigable reports whether the cursor may rest on this row: real items and
// collapsible group headers (so the user can fold/unfold them with enter), but
// not the blank spacer or muted notes.
func navigable(li list.Item) bool {
	switch h := li.(type) {
	case prItem, notifItem:
		return true
	case sectionHeader:
		return h.collapsible
	}
	return false
}

// preferItem rests the cursor on a real PR row whenever there is one, nudging
// off any header/spacer (scanning forward, then wrapping). Used after (re)loading
// so the cursor lands on a PR rather than the freshly-arrived OPEN header — even
// though headers are otherwise navigable. Falls back to ensureSelectable when the
// list has no PR rows (all groups folded, or empty).
func preferItem(lst *list.Model) {
	items := lst.Items()
	n := len(items)
	if n == 0 {
		return
	}
	// After SetItems shrinks a list (e.g. toggling sort-by-repo off drops the repo
	// subheaders), a cursor left below the new length would be out of range; clamp
	// it before indexing so we never panic on items[Index()].
	if lst.Index() >= n {
		lst.Select(n - 1)
	}
	if contentRow(items[lst.Index()]) {
		return
	}
	for i := 0; i < n; i++ {
		idx := (lst.Index() + i) % n
		if contentRow(items[idx]) {
			lst.Select(idx)
			return
		}
	}
	ensureSelectable(lst)
}

// restNotifCursor rests the inbox cursor on the top category header that has
// content — UNREAD when there are unread items, else READ — so arriving at the
// Notifications tab doesn't immediately preview a notification; the first ↓ then
// steps onto a real row. Falls back to the nearest navigable row if the target
// header isn't found.
func restNotifCursor(lst *list.Model, unreadEmpty bool) {
	target := "UNREAD"
	if unreadEmpty {
		target = "READ"
	}
	for i, li := range lst.Items() {
		if h, ok := li.(sectionHeader); ok && h.label == target {
			lst.Select(i)
			return
		}
	}
	ensureSelectable(lst)
}

// ensureSelectable nudges the cursor off a non-navigable row (the blank spacer or
// a note) onto the nearest navigable row — a real item or a foldable header —
// searching forward then wrapping. No-op when already navigable or list is empty.
func ensureSelectable(lst *list.Model) {
	items := lst.Items()
	n := len(items)
	if n == 0 {
		return
	}
	// Guard against a cursor left past the end after the list shrank (see preferItem).
	if lst.Index() >= n {
		lst.Select(n - 1)
	}
	if navigable(items[lst.Index()]) {
		return
	}
	for i := 1; i <= n; i++ {
		idx := (lst.Index() + i) % n
		if navigable(items[idx]) {
			lst.Select(idx)
			return
		}
	}
}

// selectablePos reports the 1-based rank of the selected item among the
// selectable (non-divider) rows, and the total selectable count — so the
// position counter ignores divider headers.
func selectablePos(lst *list.Model) (pos, total int) {
	items := lst.Items()
	idx := lst.Index()
	// total counts PRs only — group headers are not items, so they don't inflate
	// the count. nextRank is the rank of the first PR at or after the cursor, so a
	// cursor parked on an OPEN/CLOSED header reads as the start of that group
	// rather than 0.
	nextRank := 0
	for i, li := range items {
		if !contentRow(li) {
			continue
		}
		total++
		if nextRank == 0 && i >= idx {
			nextRank = total
		}
	}
	if total == 0 {
		return 0, 0
	}
	if nextRank == 0 { // cursor sits past the last PR
		nextRank = total
	}
	return nextRank, total
}

// selectAdjacent moves the cursor to the next selectable item in direction dir
// (+1 down, -1 up), skipping divider rows and wrapping at the ends.
func selectAdjacent(lst *list.Model, dir int) {
	items := lst.Items()
	n := len(items)
	if n == 0 {
		return
	}
	cur := lst.Index()
	for i := 0; i < n; i++ {
		cur = (cur + dir + n) % n
		if navigable(items[cur]) {
			lst.Select(cur)
			return
		}
	}
}

// selectAdjacentHeader jumps the cursor straight to the next foldable group
// header (OPEN/CLOSED) in direction dir, wrapping — so n/N hop between the two
// group selectors regardless of how many PRs sit between them. A no-op when the
// list has no headers (a lone open group with no closed tail).
func selectAdjacentHeader(lst *list.Model, dir int) {
	items := lst.Items()
	n := len(items)
	if n == 0 {
		return
	}
	cur := lst.Index()
	for i := 0; i < n; i++ {
		cur = (cur + dir + n) % n
		if h, ok := items[cur].(sectionHeader); ok && h.collapsible {
			lst.Select(cur)
			return
		}
	}
}

// Init fetches the viewer and loads every section concurrently (progressive
// render — each tab fills in as its query returns).
func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{fetchViewer, m.spinner.Tick}
	for i, s := range m.sections {
		cmds = append(cmds, loadSection(i, s.typ, s.filter, s.showClosed, s.closedLimit))
	}
	return tea.Batch(cmds...)
}

// Update handles messages.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Global messages handled regardless of mode.
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.resizeLists()
		if m.showHelp {
			m.openHelp() // re-flow the overlay to the new size
		}
		if m.mode == modeDetail {
			var cmd tea.Cmd
			m.detail, cmd = m.detail.Update(msg) // keep the detail screen sized
			return m, cmd
		}
		if m.mode == modeStack {
			var cmd tea.Cmd
			m.stackMode, cmd = m.stackMode.Update(msg)
			return m, cmd
		}
		if m.mode == modeConflict {
			var cmd tea.Cmd
			m.conflict, cmd = m.conflict.Update(msg)
			return m, cmd
		}
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case detailExitMsg:
		m.mode = modeDashboard
		// If you posted on the PR, auto-sync the whole board so it moves to its new
		// home (e.g. Orgs → Involved) without a manual refresh — with a header flash
		// so it's clear why every tab just reloaded.
		if msg.posted {
			m.flash = "↻ You posted — syncing all tabs so this PR moves to its new home…"
			cmds := append(m.syncAllCmds(), clearFlashAfter())
			return m, tea.Batch(cmds...)
		}
		return m, nil

	case flashClearMsg:
		m.flash = ""
		return m, nil

	case stackExitMsg:
		m.mode = modeDashboard
		return m, nil

	case enterConflictMsg:
		st, err := detectConflict(msg.dir)
		if err != nil || st.Op == conflict.OpNone || len(st.Files) == 0 {
			return m, nil // nothing to resolve after all
		}
		m.conflict = newConflictModel(m.th, msg.dir, st, diskLoader(msg.dir))
		m.conflict.gitTown = msg.gitTown
		// Auto-opened from a git-town op (sync) → greet with a "conflicts detected"
		// gate first, rather than dropping the user straight into the resolver. A
		// manual R entry is already a deliberate choice, so it skips the gate.
		m.conflict.intro = msg.gitTown
		m.conflict, _ = m.conflict.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
		m.mode = modeConflict
		// Clear the screen so the failed-op output behind us doesn't ghost through.
		return m, tea.ClearScreen

	case conflictExitMsg:
		// Back to the stack screen. Drop the now-stale failed-op output/error from
		// the op that triggered the conflict, then reload to reflect the resolved
		// (or undone) tree.
		m.stackMode.clearOp()
		m.stackMode.reload()
		m.mode = modeStack
		return m, tea.ClearScreen

	case tea.KeyMsg:
		// The help overlay is global and captures keys while open.
		if m.showHelp {
			switch msg.String() {
			case "?", "esc", "q":
				m.showHelp = false
				return m, nil
			}
			// Anything else scrolls the (possibly taller-than-screen) help body.
			var cmd tea.Cmd
			m.helpVP, cmd = m.helpVP.Update(msg)
			return m, cmd
		}
		// Open help on ? — unless a text field is capturing input, where ? is a
		// literal character.
		capturing := (m.mode == modeDetail && m.detail.composing()) ||
			(m.mode == modeStack && m.stackMode.capturing()) ||
			(m.mode == modeConflict && m.conflict.capturing())
		if msg.String() == "?" && !capturing {
			m.showHelp = true
			m.openHelp() // build + size the scrollable help body for this mode/terminal
			return m, nil
		}
		// Theme toggle is global — flip light/dark from any screen. ctrl+t is a
		// control key, so it never collides with text fields (no capturing guard).
		if msg.String() == "ctrl+t" {
			return m.toggleTheme()
		}
	}

	// In detail mode, everything else routes to the detail screen.
	if m.mode == modeDetail {
		var cmd tea.Cmd
		m.detail, cmd = m.detail.Update(msg)
		return m, cmd
	}

	// In stack mode, everything else routes to the stack screen.
	if m.mode == modeStack {
		var cmd tea.Cmd
		m.stackMode, cmd = m.stackMode.Update(msg)
		return m, cmd
	}

	// In conflict mode, everything else routes to the resolver.
	if m.mode == modeConflict {
		var cmd tea.Cmd
		m.conflict, cmd = m.conflict.Update(msg)
		return m, cmd
	}

	switch msg := msg.(type) {
	case viewerMsg:
		m.headerLoading = false
		if msg.err != nil {
			m.headerErr = msg.err
		} else {
			m.headerErr = nil
			m.login = msg.v.Login
			m.rate = msg.v.RateRemaining
			m.limit = msg.v.RateLimit
			viewerLogin = msg.v.Login // so renderMarkdown can flag your own @mentions
		}
		return m, nil

	case sectionLoadedMsg:
		if msg.idx < 0 || msg.idx >= len(m.sections) {
			return m, nil
		}
		s := &m.sections[msg.idx]
		s.loading = false
		s.loaded = true
		s.err = msg.err
		s.total = msg.total
		if msg.err == nil {
			// Keep the raw matches so the OPEN/CLOSED groups can be re-folded
			// without re-fetching; fold state persists across the reload.
			s.open = msg.items
			s.closed = msg.closed
			s.rebuildRows(m.sortByRepo)
			preferItem(&s.list)
			m.rebuildStacks()
			m.resizeLists() // sidebar visibility may have changed
		}
		return m, nil

	case groupedLoadedMsg:
		if msg.idx < 0 || msg.idx >= len(m.sections) {
			return m, nil
		}
		s := &m.sections[msg.idx]
		s.loading = false
		s.loaded = true
		s.err = msg.err
		s.total = msg.total
		if msg.err == nil {
			s.groups = msg.groups
			s.rebuildRows(m.sortByRepo)
			preferItem(&s.list)
			m.rebuildStacks()
			m.resizeLists()
		}
		return m, nil

	case notifFeedMsg:
		if msg.idx < 0 || msg.idx >= len(m.sections) {
			return m, nil
		}
		s := &m.sections[msg.idx]
		s.loading = false
		s.loaded = true
		s.err = msg.err
		s.total = len(msg.unread) + len(msg.read)
		if msg.err == nil {
			s.notifUnread = msg.unread
			s.notifRead = msg.read
			s.rebuildRows(m.sortByRepo)
			// Rest on the top category header (not a notification) so a fresh inbox
			// doesn't auto-preview; ↓ then steps onto the first row.
			restNotifCursor(&s.list, len(msg.unread) == 0)
			m.resizeLists()
			// Prime the preview for whatever row we landed on (a no-op on a header).
			if msg.idx == m.active {
				return m, m.notifPreviewCmd()
			}
		}
		return m, nil

	case notifConvMsg:
		m.notifPrev.cache[msg.threadID] = notifConvEntry{detail: msg.detail, err: msg.err}
		if msg.threadID == m.notifPrev.threadID {
			m.notifPrev.loading = false
			m.loadPreviewVP() // render the freshly-loaded conversation into the scroll vp
		}
		return m, nil

	case markReadMsg:
		// The row was moved optimistically; only surface a failure.
		if msg.err != nil {
			m.flash = "couldn't mark read: " + msg.err.Error()
			return m, clearFlashAfter()
		}
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "enter":
			// On a foldable OPEN/CLOSED (or UNREAD/READ) header, enter folds the group.
			if m.toggleSelectedGroup() {
				return m, nil
			}
			// Inbox drill-in: enter first focuses the preview to read/scroll the
			// conversation, then (already focused) opens the full interactive detail.
			// esc backs out a level.
			if len(m.sections) > 0 && m.sections[m.active].isNotif() {
				if n, ok := m.selectedNotif(); ok {
					if !m.notifPrev.focused && n.Type == "PullRequest" && n.Number > 0 {
						m.notifPrev.focused = true
						return m, m.notifPreviewCmd()
					}
					return m.openSelected()
				}
			}
			return m.openSelected()
		case "tab", "l":
			m.notifPrev.focused = false // leaving the inbox drops preview focus
			m.notifArmed = false        // a freshly-landed inbox is not yet armed
			m.active = (m.active + 1) % len(m.sections)
			return m, m.notifPreviewCmd() // prime the inbox preview if we landed there
		case "shift+tab", "h":
			m.notifPrev.focused = false
			m.notifArmed = false
			m.active = (m.active - 1 + len(m.sections)) % len(m.sections)
			return m, m.notifPreviewCmd()
		case "right":
			// On the inbox, once you've engaged the list (↑/↓ armed it), right focuses
			// the preview for a PR (or hides it if already focused). Before that — the
			// moment you land on the tab — right just advances to the next section, so
			// tab-hopping with ←/→ isn't hijacked by the preview pane.
			if len(m.sections) > 0 && m.sections[m.active].isNotif() {
				if m.notifPrev.focused {
					m.notifPrev.focused = false
					return m, nil
				}
				if m.notifArmed {
					if n, ok := m.selectedNotif(); ok && n.Type == "PullRequest" && n.Number > 0 {
						m.notifPrev.focused = true
						return m, m.notifPreviewCmd()
					}
				}
			}
			m.notifPrev.focused = false
			m.notifArmed = false
			m.active = (m.active + 1) % len(m.sections)
			return m, m.notifPreviewCmd()
		case "left":
			// Mirror: on the inbox with the preview focused, left returns to the list;
			// otherwise it steps to the previous section.
			if len(m.sections) > 0 && m.sections[m.active].isNotif() && m.notifPrev.focused {
				m.notifPrev.focused = false
				return m, nil
			}
			m.notifPrev.focused = false
			m.notifArmed = false
			m.active = (m.active - 1 + len(m.sections)) % len(m.sections)
			return m, m.notifPreviewCmd()
		case "n":
			// Hop straight to the next OPEN/CLOSED (or UNREAD/READ) group header —
			// quick travel between selectors even with many rows between them.
			if len(m.sections) > 0 {
				selectAdjacentHeader(&m.sections[m.active].list, +1)
			}
			return m, m.notifPreviewCmd()
		case "N":
			if len(m.sections) > 0 {
				selectAdjacentHeader(&m.sections[m.active].list, -1)
			}
			return m, m.notifPreviewCmd()
		case "x":
			// Mark the selected notification as read (inbox only). No-op elsewhere.
			return m.markSelectedRead()
		case "esc":
			// On the inbox, esc returns focus from the preview pane to the list.
			if m.notifPrev.focused {
				m.notifPrev.focused = false
				return m, nil
			}
		case "s":
			// The stack sidebar is a PR-list-tab feature; on the Notifications inbox
			// it has nowhere to render, so s is a no-op there.
			if len(m.sections) > 0 && m.sections[m.active].isNotif() {
				return m, nil
			}
			m.showStack = !m.showStack
			m.resizeLists()
			return m, nil
		case "S":
			// Enter the dedicated, local-context stack authoring mode.
			m.stackMode = newStackModel(m.th, m.localRepo)
			m.stackMode, _ = m.stackMode.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
			m.mode = modeStack
			// Load the open-PR flags for the local tree (branch → #number).
			return m, fetchStackPRNums(m.localRepo)
		case "o":
			// Flip the PR-list ordering: default newest-updated ↔ grouped by repo.
			// Global (every tab reorders together, so scanning stays consistent as you
			// tab across); no-op on the Notifications inbox, which owns its ordering.
			if len(m.sections) > 0 && m.sections[m.active].isNotif() {
				return m, nil
			}
			m.sortByRepo = !m.sortByRepo
			for i := range m.sections {
				m.sections[i].rebuildRows(m.sortByRepo)
				// The inbox ignores the sort and rests on its own header — don't nudge
				// its cursor onto a notification (which would auto-preview).
				if !m.sections[i].isNotif() {
					preferItem(&m.sections[i].list)
				}
			}
			return m, nil
		case "r":
			// Refresh is a whole-board sync, not just the active tab: re-run every
			// section's query so a PR whose state changed (e.g. you just commented on
			// an Orgs PR) lands in its correct home across all tabs in one press,
			// instead of needing a separate refresh per tab.
			return m, tea.Batch(m.syncAllCmds()...)
		}
		// Forward navigation to the active section's list. j/k/arrows move to the
		// next selectable row, skipping divider headers and wrapping at the ends.
		if len(m.sections) > 0 {
			// Inbox preview focused: arrows/j/k scroll the conversation, not the list.
			if m.sections[m.active].isNotif() && m.notifPrev.focused {
				switch msg.String() {
				case "down", "j":
					m.notifPrev.vp.LineDown(1)
					return m, nil
				case "up", "k":
					m.notifPrev.vp.LineUp(1)
					return m, nil
				}
			}
			lst := &m.sections[m.active].list
			if len(lst.Items()) > 0 && lst.FilterState() != list.Filtering {
				switch msg.String() {
				case "down", "j":
					selectAdjacent(lst, +1)
					m.notifArmed = true // engaging the list arms → to reach the preview
					return m, m.notifPreviewCmd()
				case "up", "k":
					selectAdjacent(lst, -1)
					m.notifArmed = true
					return m, m.notifPreviewCmd()
				}
			}
			var cmd tea.Cmd
			m.sections[m.active].list, cmd = m.sections[m.active].list.Update(msg)
			ensureSelectable(&m.sections[m.active].list)
			return m, tea.Batch(cmd, m.notifPreviewCmd())
		}
	}
	return m, nil
}

// toggleTheme flips between the light and dark palettes live, threading the new
// theme into every open screen, re-rendering cached viewport content, and
// persisting the choice. The redraw clears first so old-palette backgrounds
// don't ghost behind the new ones.
func (m Model) toggleTheme() (tea.Model, tea.Cmd) {
	m.themeMode = theme.Toggle(m.themeMode)
	m.th = theme.Resolve(m.themeMode, m.cfg.Theme)
	m.spinner.Style = lipgloss.NewStyle().Foreground(m.th.Focus)

	// The list-row delegate carries the theme, so rebuild it; then push the new
	// theme into whichever sub-screen is live (detail caches rendered viewports,
	// so it needs an explicit re-render — stack and conflict render from th).
	m.resizeLists()
	// The inbox preview caches its rendered conversation (themed ANSI); its render
	// key includes the theme, so this re-renders it under the new palette — else a
	// dark-rendered conversation would show dark colors on the light page.
	m.loadPreviewVP()
	switch m.mode {
	case modeDetail:
		m.detail.restyle(m.th)
	case modeStack:
		m.stackMode.th = m.th
		m.stackMode.restyleComposer() // re-theme the textarea/title under the new palette
	case modeConflict:
		m.conflict.th = m.th
	}

	// Persist out-of-band; a write failure must not break the live toggle.
	mode := m.themeMode
	persist := func() tea.Msg { _ = config.SaveThemeMode(mode); return nil }
	return m, tea.Batch(tea.ClearScreen, persist)
}

// toggleSelectedGroup folds or unfolds the OPEN/CLOSED group whose header is
// highlighted, rebuilding the list in place and keeping the cursor on the header
// so it can be toggled back. Reports whether a foldable header was acted on (so
// the caller can fall through to opening a PR when it wasn't). It mutates the
// active section through the shared slice backing array, so the value receiver is
// fine — same pattern as the loading/refresh handlers.
func (m Model) toggleSelectedGroup() bool {
	if len(m.sections) == 0 {
		return false
	}
	s := &m.sections[m.active]
	h, ok := s.list.SelectedItem().(sectionHeader)
	if !ok || !h.collapsible {
		return false
	}
	idx := s.list.Index()
	// A repo subheader folds by its scoped key (distinct from its display label);
	// OPEN/CLOSED/org headers set key == label, so this is uniform.
	key := h.key
	if key == "" {
		key = h.label
	}
	s.collapsed[key] = !s.collapsed[key]
	s.rebuildRows(m.sortByRepo)
	// The toggled header keeps its index (only rows below it appear/disappear), so
	// the cursor stays put; ensureSelectable is a safety net.
	s.list.Select(idx)
	ensureSelectable(&s.list)
	return true
}

// openSelected opens the highlighted dashboard row in the detail screen, if it
// is a pull request. Non-PR rows (issues, number-less notifications) are
// ignored for now — the review pane is PR-only.
func (m Model) openSelected() (tea.Model, tea.Cmd) {
	if len(m.sections) == 0 {
		return m, nil
	}
	sel := m.sections[m.active].list.SelectedItem()
	// A PR (a PR row or a PR notification) opens the in-app detail screen, where
	// the full conversation lives and you can participate.
	if it := selectedAsItem(sel); it.IsPR && it.Number > 0 {
		m.detail = newDetail(m.th, it)
		m.detail, _ = m.detail.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
		m.mode = modeDetail
		return m, m.detail.Init()
	}
	// A non-PR notification (Issue, Release, Discussion, …) has no in-app view, so
	// open it on GitHub.
	if n, ok := sel.(notifItem); ok {
		return m, openBrowser(notifWebURL(n.Notification))
	}
	return m, nil
}

// selectedAsItem normalizes the selected row to a gh.Item the detail screen can
// open. A PR row passes through; a PR notification is mapped to a minimal Item
// (the detail screen re-fetches by owner/repo/number). Anything else returns a
// zero Item (not a PR), so the caller ignores it.
func selectedAsItem(sel list.Item) gh.Item {
	switch it := sel.(type) {
	case prItem:
		return it.Item
	case notifItem:
		if it.Type == "PullRequest" && it.Number > 0 {
			return gh.Item{
				IsPR:   true,
				Repo:   it.Repo,
				Number: it.Number,
				Title:  it.Title,
				URL:    fmt.Sprintf("https://github.com/%s/pull/%d", it.Repo, it.Number),
			}
		}
	}
	return gh.Item{}
}

// chrome heights, used for both layout and list sizing. The tab strip is three
// rows: the active tab's top border, the labels, and the body's top line. The
// header is two rows: the brand line and the session/status line.
const (
	headerH    = 2
	tabsH      = 3
	footerH    = 3 // legend line + rule + keybinding line
	colHeaderH = 1 // the PR-list column-label row above each section
)

// appMarginLeft is the left gutter for body content so it clears the terminal's
// left edge. It is applied per-region via indentBody (body content only) — the
// full-width Surface bars (header, footer, statusline) stay flush to the edge.
const appMarginLeft = 2

// logoGlyph is Cairn's mark in the header brand line. Pulled out so it's a
// one-line swap; ⟁ (a small triangle within a triangle) reads as a cairn/peak.
const logoGlyph = "⟁"

func (m *Model) resizeLists() {
	bodyH := m.height - headerH - tabsH - footerH
	if bodyH < 1 {
		bodyH = 1
	}
	bw := bodyWidth(m.width)
	listW := bw
	if m.sidebarVisible() {
		listW = bw - stackPaneW - 1 // sidebar + vertical separator
		if listW < 20 {
			listW = 20
		}
	}
	// The list pane always carries a section title + rule (the "Orgs ▴ 1/10 ▾"
	// subheader) above the column-label row, sidebar or not.
	listH := bodyH - 2 - colHeaderH
	if listH < 1 {
		listH = 1
	}
	delegate := itemDelegate{th: m.th, width: listW}
	// The inbox list is a fixed, compact left pane (the preview takes the rest), so
	// it sizes independently of the PR-list/sidebar geometry.
	notifW := notifListW
	if notifW > bw-30 {
		notifW = bw / 2
	}
	notifDelegate := itemDelegate{th: m.th, width: notifW}
	notifH := bodyH - 2 // title + rule
	if notifH < 1 {
		notifH = 1
	}
	for i := range m.sections {
		if m.sections[i].isNotif() {
			m.sections[i].list.SetDelegate(notifDelegate)
			m.sections[i].list.SetSize(notifW, notifH)
			continue
		}
		m.sections[i].list.SetDelegate(delegate)
		m.sections[i].list.SetSize(listW, listH)
	}

	// Size the inbox preview viewport to its pane (and re-render its content at the
	// new width, since wrapping changed).
	_, previewW, _ := notifPaneDims(bw)
	pvH := bodyH - notifPreviewHeaderH
	if pvH < 1 {
		pvH = 1
	}
	if m.notifPrev.vp.Width != previewW || m.notifPrev.vp.Height != pvH {
		m.notifPrev.vp.Width = previewW
		m.notifPrev.vp.Height = pvH
		m.loadPreviewVP() // render key includes width → re-renders at the new size
	}
}

// stackPaneW is the base width of the stack sidebar (dashboard) and the floor for
// the stack-mode tree, which grows past it to fit long branch names. A compact
// default that keeps room for the main content; toggle the dashboard sidebar off
// with `s` on a narrow monitor.
const stackPaneW = 36

// notifListW is the fixed width of the inbox's left (list) pane; the preview pane
// takes the remaining body width.
const notifListW = 46

// rebuildStacks reconstructs remote stacks from every loaded PR across all
// sections (deduped), so the sidebar can follow the selected PR.
func (m *Model) rebuildStacks() {
	seen := map[string]bool{}
	var refs []stack.PRRef
	// Iterate the stored matches, not the visible list rows, so folding a group
	// doesn't drop its PRs from the reconstructed sidebar.
	for i := range m.sections {
		s := &m.sections[i]
		items := append(append([]gh.Item{}, s.open...), s.closed...)
		for _, g := range s.groups {
			items = append(items, g.items...)
		}
		for _, it := range items {
			if !it.IsPR || it.HeadBranch == "" {
				continue
			}
			key := fmt.Sprintf("%s#%d", it.Repo, it.Number)
			if seen[key] {
				continue
			}
			seen[key] = true
			refs = append(refs, stack.PRRef{
				Repo: it.Repo, Number: it.Number,
				Head: it.HeadBranch, Base: it.BaseBranch,
				Review: string(it.Review), Checks: string(it.Checks),
			})
		}
	}
	m.stacks = stack.BuildRemoteStacks(refs)
}

// hasStacks reports whether any reconstructed or local stack is non-trivial (a
// trunk plus at least two branches) — the bar for reserving sidebar space.
func (m Model) hasStacks() bool {
	for _, t := range m.stacks {
		if len(t.Order) >= 3 {
			return true
		}
	}
	return m.localStack != nil && len(m.localStack.Order) >= 3
}

func (m Model) sidebarVisible() bool {
	return m.showStack && m.width >= 90 && m.hasStacks()
}

// View renders header + tab bar + active section + footer.
func (m Model) View() string {
	if m.width == 0 {
		return "starting…"
	}
	var body string
	switch {
	case m.showHelp:
		body = m.renderHelp()
	case m.mode == modeDetail:
		body = m.detail.View(m.spinner.View())
	case m.mode == modeStack:
		body = m.stackMode.View(m.spinner.View(), m.viewHeader())
	case m.mode == modeConflict:
		body = m.conflict.View()
	default:
		// Header and footer are full-width bars (flush to the edges); only the
		// tabs+body content is indented by the left gutter.
		mid := indentBody(lipgloss.JoinVertical(lipgloss.Left, m.viewTabs(), m.viewBody()))
		body = lipgloss.JoinVertical(lipgloss.Left,
			m.viewHeader(),
			mid,
			m.viewFooter(),
		)
	}
	return m.paintBackground(body)
}

// paintBackground fills the whole frame with the theme's Base background and Text
// foreground. lipgloss ends every styled run with a full reset (ESC[0m), which also
// clears the background, so plain text following a reset on the same line falls back
// to the terminal's own background. In dark mode that's invisible (terminal ≈ Base),
// but in light mode the body would show the dark terminal through. We reassert the
// document default (Text on Base) after every reset so unstyled regions keep the
// page color, then let the outer style pad each line to full width and the frame to
// full height.
func (m Model) paintBackground(view string) string {
	def := lipgloss.NewStyle().Foreground(m.th.Text).Background(m.th.Base)
	// The opening SGR for the default: render a marker and slice off the codes
	// before it. Empty when the color profile has no color (e.g. tests) — a no-op.
	stamped := def.Render("\x00")
	reassert := stamped[:strings.IndexByte(stamped, '\x00')]
	if reassert != "" {
		view = strings.ReplaceAll(view, "\x1b[0m", "\x1b[0m"+reassert)
		view = reassert + view
	}
	// Clip every row to the terminal width so an over-wide line can't WRAP: a wrapped
	// line adds rows, pushing the fixed-height frame past the screen height, which
	// scrolls the whole frame out of view (a blank screen). Truncating instead keeps
	// the frame exactly m.height rows tall. Defensive — views should already fit.
	if m.width > 0 {
		lines := strings.Split(view, "\n")
		for i, ln := range lines {
			lines[i] = ansi.Truncate(ln, m.width, "")
		}
		view = strings.Join(lines, "\n")
	}
	return def.Width(m.width).Height(m.height).Render(view)
}

// indentBody left-pads every line of a body block by appMarginLeft spaces so the
// content clears the terminal's left edge, while full-width bars (header, footer,
// statusline) rendered outside it stay flush to the edge. Plain spaces suffice:
// paintBackground keeps Base asserted at the start of every line, so the gutter
// renders in the page color. Callers must size the block to bodyWidth(width) so
// indent + content lands back at full width.
func indentBody(s string) string {
	if appMarginLeft <= 0 {
		return s
	}
	pad := strings.Repeat(" ", appMarginLeft)
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = pad + lines[i]
	}
	return strings.Join(lines, "\n")
}

// bodyWidth is the width available to indented body content: the full width minus
// the left gutter indentBody adds back.
func bodyWidth(width int) int {
	w := width - appMarginLeft
	if w < 1 {
		w = width
	}
	return w
}

// surfaceBar paints content as a full-width bar in the theme's Surface color —
// the shared primitive behind both the top header and every screen's bottom
// footer, so the eye reads them as one matched frame around the Base body (the
// action area "pops" from the center). Same mechanism as paintBackground but for
// Surface: lipgloss ends every styled run with ESC[0m, which clears the
// background, so any gap between fragments (or a fragment styled with only a
// foreground) would fall back to whatever bg is active — Base, after
// paintBackground's global reassert — and read as a second color on the bar.
// We reassert Surface after every reset inside content so the whole bar stays
// one color. Composition is order-safe: paintBackground later inserts its Base
// reassert immediately after each ESC[0m, i.e. *before* the Surface one we
// inject here, so Surface wins on the bar while Base still wins off it. Content
// must already carry its own left padding; the helper sets only width + colors.
func surfaceBar(th theme.Theme, width int, content string) string {
	return styledBar(th.Text, th.Surface, width, content)
}

// styledBar is surfaceBar generalized to any fg/bg: it paints content as a
// full-width bar and reasserts fg+bg after every ESC[0m inside it, so inner
// styled fragments (a colored CI dot, a draft tag) don't punch the background
// back to the page color after their own reset. Used for the selected list row
// (Primary-on-Surface), whose highlight must span the whole line in both themes.
func styledBar(fg, bg lipgloss.Color, width int, content string) string {
	s := lipgloss.NewStyle().Foreground(fg).Background(bg)
	stamped := s.Render("\x00")
	reassert := stamped[:strings.IndexByte(stamped, '\x00')]
	if reassert != "" {
		content = strings.ReplaceAll(content, "\x1b[0m", "\x1b[0m"+reassert)
		content = reassert + content
	}
	return s.Width(width).Render(content)
}

func (m Model) viewHeader() string {
	return renderBrandHeader(m.th, m.login, m.rate, m.headerLoading, m.headerErr, m.width, m.flash)
}

// renderBrandHeader draws Cairn's two-row masthead — the brand line over the
// session/status line — as a Surface bar. It is a standalone function (not a
// Model method) so full-screen modes that take over the dashboard, like stack
// mode, can show the same top bar without duplicating it. headerH (2) must match
// the line count this returns.
func renderBrandHeader(th theme.Theme, login string, rate int, loading bool, herr error, width int, flash string) string {
	// Row 1 — brand. The mark + name get their own line so they read as a
	// masthead rather than competing with the status text.
	brand := lipgloss.NewStyle().Foreground(th.Primary).Bold(true).Render(logoGlyph + "  Cairn")
	tagline := lipgloss.NewStyle().Foreground(th.Muted).Render("   keyboard cockpit for GitHub")
	brandRow := surfaceBar(th, width, " "+brand+tagline)

	// Row 2 — session/status. A flash (e.g. the post-then-sync notice) takes the
	// line so the user sees what's happening, overriding the usual session info.
	var status string
	switch {
	case flash != "":
		status = lipgloss.NewStyle().Foreground(th.Focus).Bold(true).Render(flash)
	case loading:
		status = lipgloss.NewStyle().Foreground(th.Focus).Render("connecting…")
	case herr != nil:
		status = lipgloss.NewStyle().Foreground(th.Danger).Render(herr.Error())
	default:
		who := lipgloss.NewStyle().Foreground(th.Info).Render(login)
		calls := lipgloss.NewStyle().Foreground(th.Muted).Render(fmt.Sprintf("%d API calls remaining", rate))
		status = lipgloss.NewStyle().Foreground(th.Text).Render("Logged in as ") + who +
			lipgloss.NewStyle().Foreground(th.Muted).Render(" · ") + calls
	}
	statusRow := surfaceBar(th, width, " "+status)

	return lipgloss.JoinVertical(lipgloss.Left, brandRow, statusRow)
}

func (m Model) viewTabs() string {
	// The active tab is a box whose bottom opens (┘ … └) into the body's top
	// line; inactive tabs are plain labels sitting on that same line.
	active := lipgloss.RoundedBorder()
	active.BottomLeft, active.Bottom, active.BottomRight = "┘", " ", "└"

	var cells []string
	for i, s := range m.sections {
		label := s.title
		if s.loaded && s.err == nil {
			label = fmt.Sprintf("%s (%d)", s.title, s.total)
		}
		if i == m.active {
			cells = append(cells, lipgloss.NewStyle().
				Border(active, true).BorderForeground(m.th.Focus).
				Foreground(m.th.Focus).Bold(true).
				Padding(0, 1).Render(label))
		} else {
			cells = append(cells, lipgloss.NewStyle().
				Border(lipgloss.Border{Bottom: "─"}, false, false, true, false).
				BorderForeground(m.th.Focus).
				Foreground(m.th.Muted).
				Padding(0, 1).Render(label))
		}
	}
	row := lipgloss.JoinHorizontal(lipgloss.Bottom, cells...)

	// Extend the body's top line to the full width past the last tab — the
	// whole rule reads as one continuous focus-colored line.
	gap := bodyWidth(m.width) - lipgloss.Width(row)
	if gap < 0 {
		gap = 0
	}
	filler := lipgloss.NewStyle().Foreground(m.th.Focus).Render(strings.Repeat("─", gap))
	return lipgloss.JoinHorizontal(lipgloss.Bottom, row, filler)
}

func (m Model) viewBody() string {
	bodyH := m.height - headerH - tabsH - footerH
	if bodyH < 1 {
		bodyH = 1
	}
	bw := bodyWidth(m.width)
	if len(m.sections) == 0 {
		return lipgloss.NewStyle().Width(bw).Height(bodyH).
			Foreground(m.th.Muted).Render("  no sections configured")
	}

	// The Notifications inbox owns the whole body with its own two-pane layout.
	if m.sections[m.active].isNotif() {
		return m.viewNotifications(bodyH, bw)
	}

	sidebar := m.sidebarVisible()
	listW := bw
	if sidebar {
		listW = bw - stackPaneW - 1
	}
	listH := bodyH - 2 - colHeaderH // section title + rule always sit above the list
	if listH < 1 {
		listH = 1
	}

	s := m.sections[m.active]
	// Column labels above the list. The author column reads "Opened by" — at the
	// list level GitHub gives us the PR author, not who requested the review.
	colHead := columnHeader(m.th, listW, "Opened by")
	box := lipgloss.NewStyle().Width(listW).Height(listH)
	var listBody string
	switch {
	case s.loading:
		listBody = box.Render(fmt.Sprintf("  %s loading %s…", m.spinner.View(), s.title))
	case s.err != nil:
		listBody = box.Render(lipgloss.NewStyle().Foreground(m.th.Danger).
			Render("  error: " + s.err.Error()))
	case len(s.list.Items()) == 0:
		listBody = box.Render(lipgloss.NewStyle().Foreground(m.th.Muted).Render("  nothing here"))
	default:
		listBody = s.list.View()
	}
	// The section subheader (a blue title with a position counter so it's obvious
	// you're moving the PR list, not the stack) over a blue rule — always shown,
	// whether or not the stack sidebar is open.
	label := s.title
	if pos, n := selectablePos(&s.list); n > 0 {
		label = fmt.Sprintf("%s  ▴ %d/%d ▾", s.title, pos, n)
	}
	// Flag the non-default order so it's clear why the list is grouped by repo (and
	// that `o` flips it back). The default newest-updated order needs no label.
	if m.sortByRepo {
		label += "  · by repo"
	}
	listTitle := lipgloss.NewStyle().Width(listW).Foreground(m.th.Focus).Bold(true).Render(label)
	listRule := lipgloss.NewStyle().Foreground(m.th.Focus).Render(strings.Repeat("─", listW))
	listPane := lipgloss.JoinVertical(lipgloss.Left, listTitle, listRule, colHead, listBody)

	if !sidebar {
		return listPane
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, m.renderStackSidebar(stackPaneW, bodyH), stackVBar(m.th, bodyH), listPane)
}

// Source icons (Nerd Font): a GitHub mark for remote-reconstructed nodes, a
// laptop for branches also present in the local git-town config.
const (
	iconRemote = "" // nf-fa-github
	iconLocal  = "" // nf-fa-laptop
)

func stackVBar(th theme.Theme, h int) string {
	bar := lipgloss.NewStyle().Foreground(th.Overlay).Render("│")
	lines := make([]string, h)
	for i := range lines {
		lines[i] = bar
	}
	return strings.Join(lines, "\n")
}

func (m Model) selectedItem() (gh.Item, bool) {
	if len(m.sections) == 0 {
		return gh.Item{}, false
	}
	if it, ok := m.sections[m.active].list.SelectedItem().(prItem); ok {
		return it.Item, true
	}
	return gh.Item{}, false
}

// renderStackSidebar draws the stack of the currently-selected PR — its chain
// reconstructed from PR bases (remote), with local git-town drift overlaid where
// the cwd repo matches.
func (m Model) renderStackSidebar(w, h int) string {
	// Muted title/rule: the sidebar is passive — it mirrors the PR list's
	// selection rather than being scrolled itself.
	title := lipgloss.NewStyle().Width(w).Foreground(m.th.Muted).Render("Stack (follows selection)")
	rule := lipgloss.NewStyle().Foreground(m.th.Overlay).Render(strings.Repeat("─", w))

	var nodes []*stack.Node
	repo := ""
	it, ok := m.selectedItem()
	if ok && it.IsPR {
		if tree := stack.FindStackInRepo(m.stacks, it.Repo, it.HeadBranch); tree != nil {
			nodes = tree.Focused(it.HeadBranch) // only this PR's lineage, not siblings
			repo = it.Repo
		}
	}

	var body string
	if len(nodes) < 2 {
		body = mutedStyle(m.th).Render("  No stack for this PR.\n  Select a stacked PR to\n  see its lineage.")
	} else {
		legend := mutedStyle(m.th).Render(fmt.Sprintf("  %s remote  %s local", iconRemote, iconLocal))
		body = m.renderStackTree(nodes, repo, it.HeadBranch, w) + "\n" + legend
	}
	bodyBox := lipgloss.NewStyle().Width(w).Height(h - 2).MaxHeight(h - 2).Render(body)
	return lipgloss.JoinVertical(lipgloss.Left, title, rule, bodyBox)
}

func (m Model) renderStackTree(nodes []*stack.Node, repo, selectedHead string, w int) string {
	var b strings.Builder
	for _, n := range nodes {
		// Local overlay: if this branch exists in the cwd repo's git-town config,
		// mark it local and surface drift.
		var local *stack.Node
		if repo == m.localRepo {
			local = m.localStack.NodeByName(n.Name)
		}
		icon := lipgloss.NewStyle().Foreground(m.th.Muted).Render(iconRemote)
		if local != nil {
			icon = lipgloss.NewStyle().Foreground(m.th.Info).Render(iconLocal)
		}

		indent := strings.Repeat("  ", n.Depth)
		drifted := local != nil && local.Drifted

		name := n.Name
		nameStyle := lipgloss.NewStyle().Foreground(m.th.Text)
		switch {
		case n.Name == selectedHead:
			// Selection wins over drift: the current PR is always pink.
			nameStyle = lipgloss.NewStyle().Foreground(m.th.Primary).Bold(true)
		case n.IsTrunk:
			nameStyle = lipgloss.NewStyle().Foreground(m.th.Muted)
		case drifted:
			nameStyle = lipgloss.NewStyle().Foreground(m.th.Warning) // amber drift
		}

		// PR suffix + status glyphs.
		suffix := ""
		if n.HasPR {
			suffix = " " + lipgloss.NewStyle().Foreground(m.th.Info).Render(fmt.Sprintf("#%d", n.PRNumber))
			suffix += " " + reviewGlyph(m.th, gh.Item{Review: gh.ReviewState(n.Review)})
			suffix += ciGlyph(m.th, gh.CheckState(n.Checks))
		}
		if drifted {
			suffix += " " + lipgloss.NewStyle().Foreground(m.th.Warning).Render("⚠")
		}

		// Budget the branch name to the remaining width.
		used := 2 + len(indent) + lipgloss.Width(suffix) + 1
		nameMax := w - used
		if nameMax < 4 {
			nameMax = 4
		}
		line := icon + " " + indent + nameStyle.Render(truncate(name, nameMax)) + suffix
		b.WriteString(line + "\n")
	}
	return b.String()
}

// themeFooterHint renders the light/dark toggle affordance for any footer: a sun
// and moon glyph flanking the ^t key, with the active mode's glyph lit (amber sun
// in light mode, cyan moon in dark) and the other muted. The mode is read from
// the theme itself, so every footer can share this without tracking it.
func themeFooterHint(th theme.Theme) string {
	sun := lipgloss.NewStyle().Foreground(th.Muted).Render("☀")
	moon := lipgloss.NewStyle().Foreground(th.Muted).Render("☾")
	if theme.IsLight(th) {
		sun = lipgloss.NewStyle().Foreground(th.Warning).Render("☀")
	} else {
		moon = lipgloss.NewStyle().Foreground(th.Info).Render("☾")
	}
	muted := lipgloss.NewStyle().Foreground(th.Muted)
	return sun + muted.Render(" / ") + moon + muted.Render(" ctrl + t theme")
}

func (m Model) viewFooter() string {
	dim := lipgloss.NewStyle().Foreground(m.th.Muted)
	sep := dim.Render(" · ")

	// Stack mode is Cairn's headline feature — render its hint in the Primary
	// accent (bold) so it pops out of the otherwise-muted utility keys.
	stackHint := lipgloss.NewStyle().Foreground(m.th.Primary).Bold(true).Render("S stack mode")

	// On the inbox with the preview focused, the navigation keys change meaning:
	// arrows scroll the conversation and ←/esc return to the list.
	previewFocused := len(m.sections) > 0 && m.sections[m.active].isNotif() && m.notifPrev.focused
	var parts []string
	if previewFocused {
		parts = []string{
			dim.Render("↑/↓ scroll"),
			dim.Render("enter " + m.enterHint()),
			dim.Render("←/esc back"),
			dim.Render("r sync all"),
			themeFooterHint(m.th),
			dim.Render("? help"),
			dim.Render("q quit"),
		}
	} else {
		parts = []string{
			dim.Render("↑/↓ move"),
			dim.Render("n/N hunk"), // hop between headers, like n/N in the conflict resolver
			dim.Render("←/→ section"),
			dim.Render("enter " + m.enterHint()),
		}
		// The stack sidebar and the by-repo grouping toggle are PR-list-tab features
		// with no place on the Notifications inbox, so don't advertise them there.
		// (o's current state also shows as "· by repo" in the section subheader.)
		if !(len(m.sections) > 0 && m.sections[m.active].isNotif()) {
			parts = append(parts, dim.Render("s sidebar"), dim.Render("o group"))
		}
		parts = append(parts,
			stackHint,
			dim.Render("r sync all"),
			themeFooterHint(m.th),
			dim.Render("? help"),
			dim.Render("q quit"),
		)
	}
	keys := strings.Join(parts, sep)

	// Second line: a legend for the row status glyphs, grouped by the column they
	// come from — the leading dot (checks, or lifecycle once closed) and the review
	// mark — so the colors read at a glance. Glyphs keep their row color; labels muted.
	mark := func(c lipgloss.Color, glyph, label string) string {
		return lipgloss.NewStyle().Foreground(c).Render(glyph) + dim.Render(" "+label)
	}
	diamond := lipgloss.NewStyle().Foreground(m.th.Focus).Bold(true).Render("◆") + dim.Render(" yours")
	group := func(name string, items ...string) string {
		label := lipgloss.NewStyle().Foreground(m.th.Muted).Bold(true).Render(name)
		return label + dim.Render(" — ") + strings.Join(items, dim.Render(" · "))
	}
	groupSep := lipgloss.NewStyle().Foreground(m.th.Overlay).Render("   │   ")

	var legend string
	if len(m.sections) > 0 && m.sections[m.active].isNotif() {
		// The inbox shows its own glyphs (subject type + why you got it), not the
		// PR checks/review/state cues, which don't apply here.
		typeMark := func(typ, label string) string {
			return lipgloss.NewStyle().Foreground(notifColor(m.th, typ)).Render(notifGlyph(typ)) + dim.Render(" "+label)
		}
		whyMark := func(reason, label string) string {
			return lipgloss.NewStyle().Foreground(reasonColor(m.th, reason)).Render(reasonGlyph(reason)) + dim.Render(" "+label)
		}
		legend = strings.Join([]string{
			group("TYPE", typeMark("PullRequest", "PR"), typeMark("Issue", "issue")),
			group("WHY", whyMark("review_requested", "review"), whyMark("mention", "mention"),
				whyMark("comment", "comment"), whyMark("author", "authored"), whyMark("assign", "assigned")),
		}, groupSep)
	} else {
		legend = strings.Join([]string{
			group("CHECKS", mark(m.th.Success, "●", "passing"), mark(m.th.Danger, "●", "failing"), mark(m.th.Muted, "○", "none")),
			group("REVIEW", diamond, mark(m.th.Success, "✓", "approved"), mark(m.th.Danger, "✗", "changes"), mark(m.th.Muted, "◇", "others")),
			group("STATE", mark(m.th.Primary, "●", "merged"), mark(m.th.Muted, "●", "closed")),
		}, groupSep)
	}

	box := lipgloss.NewStyle().Width(m.width).Padding(0, 1)
	// Legend on top, a rule, then keybindings — the whole stack painted as one
	// Surface bar so it matches the header and pops off the Base body.
	rule := lipgloss.NewStyle().Foreground(m.th.Overlay).Render(strings.Repeat("─", m.width))
	return surfaceBar(m.th, m.width,
		lipgloss.JoinVertical(lipgloss.Left, box.Render(legend), rule, box.Render(keys)))
}
