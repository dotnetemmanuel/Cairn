// Package tui holds the Bubble Tea application. Phase 1 is the read-only
// dashboard: config-driven sections of PRs/issues from GraphQL, cycled with
// tab, navigated with j/k, refreshed with r.
package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
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

// section is one board tab — either a search filter or the notifications feed.
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

	// Raw matches kept apart from the list so the OPEN/CLOSED groups can be
	// re-folded without re-fetching: rebuildRows reassembles list items from these
	// plus the collapse flags.
	open            []gh.Item
	closed          []gh.Item
	openCollapsed   bool // OPEN group folded (its items hidden under the header)
	closedCollapsed bool // CLOSED group folded
}

// rebuildRows reassembles the section's list rows from its stored open/closed
// matches and current fold state. Called on load and whenever a group is toggled.
func (s *section) rebuildRows() {
	s.list.SetItems(sectionRows(s.open, s.closed, s.openCollapsed, s.closedCollapsed))
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

	// Stack tree: remote stacks reconstructed from loaded PRs (any repo), plus
	// the local git-town tree for the cwd repo (drift-aware overlay).
	stacks     []*stack.Tree
	localStack *stack.Tree
	localRepo  string // owner/name of the cwd repo, "" if none
	showStack  bool
	showHelp   bool

	spinner spinner.Model

	// header state
	headerLoading bool
	login         string
	rate          int
	limit         int
	headerErr     error
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

func loadSection(idx int, typ, filter string, showClosed bool, limit int) tea.Cmd {
	return func() tea.Msg {
		if typ == config.SectionNotifications {
			items, total, err := gh.FetchNotifications(searchLimit)
			return sectionLoadedMsg{idx: idx, items: items, total: total, err: err}
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

// sectionRows assembles a section's list rows: open items, then any recently
// closed/merged items beneath a divider. Whenever a closed tail is present the
// OPEN/CLOSED structure is shown — including when there are zero open PRs, where a
// muted "nothing open" placeholder sits under the OPEN header — so an all-closed
// section reads as "nothing open + N closed" rather than an unlabeled list that
// looks miscounted. A lone open group (no closed tail) needs no header.
//
// The OPEN/CLOSED headers are collapsible: when openCollapsed/closedCollapsed is
// set the group's items (and the "nothing open" note) are omitted, leaving just
// the foldable header. The headers are only emitted when a closed tail exists —
// a lone open group stays a flat, unfoldable list.
func sectionRows(open, closed []gh.Item, openCollapsed, closedCollapsed bool) []list.Item {
	rows := make([]list.Item, 0, len(open)+len(closed)+4)
	labeled := len(closed) > 0
	if !labeled {
		for _, it := range open {
			rows = append(rows, prItem{it})
		}
		return rows
	}
	rows = append(rows, sectionHeader{label: "OPEN", collapsible: true, collapsed: openCollapsed, count: len(open)})
	if !openCollapsed {
		if len(open) == 0 {
			rows = append(rows, listNote{"nothing open"})
		}
		for _, it := range open {
			rows = append(rows, prItem{it})
		}
	}
	// A blank spacer sets the closed group apart from the open list.
	rows = append(rows, sectionHeader{}, sectionHeader{label: "CLOSED", collapsible: true, collapsed: closedCollapsed, count: len(closed)})
	if !closedCollapsed {
		for _, it := range closed {
			rows = append(rows, prItem{it})
		}
	}
	return rows
}

// navigable reports whether the cursor may rest on this row: real items and
// collapsible group headers (so the user can fold/unfold them with enter), but
// not the blank spacer or muted notes.
func navigable(li list.Item) bool {
	switch h := li.(type) {
	case prItem:
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
	if _, ok := items[lst.Index()].(prItem); ok {
		return
	}
	for i := 0; i < n; i++ {
		idx := (lst.Index() + i) % n
		if _, ok := items[idx].(prItem); ok {
			lst.Select(idx)
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
		if _, ok := li.(prItem); !ok {
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
			}
			return m, nil
		}
		// Open help on ? — unless a text field is capturing input, where ? is a
		// literal character.
		capturing := (m.mode == modeDetail && m.detail.composing()) ||
			(m.mode == modeStack && m.stackMode.capturing()) ||
			(m.mode == modeConflict && m.conflict.capturing())
		if msg.String() == "?" && !capturing {
			m.showHelp = true
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
			s.rebuildRows()
			preferItem(&s.list)
			m.rebuildStacks()
			m.resizeLists() // sidebar visibility may have changed
		}
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "enter":
			// On a foldable OPEN/CLOSED header, enter collapses/expands the group;
			// on a PR row it opens the detail screen.
			if m.toggleSelectedGroup() {
				return m, nil
			}
			return m.openSelected()
		case "tab", "l", "right":
			m.active = (m.active + 1) % len(m.sections)
			return m, nil
		case "shift+tab", "h", "left":
			m.active = (m.active - 1 + len(m.sections)) % len(m.sections)
			return m, nil
		case "n":
			// Hop straight to the next OPEN/CLOSED group header — quick travel
			// between the two selectors even with many PRs between them.
			if len(m.sections) > 0 {
				selectAdjacentHeader(&m.sections[m.active].list, +1)
			}
			return m, nil
		case "N":
			if len(m.sections) > 0 {
				selectAdjacentHeader(&m.sections[m.active].list, -1)
			}
			return m, nil
		case "s":
			m.showStack = !m.showStack
			m.resizeLists()
			return m, nil
		case "S":
			// Enter the dedicated, local-context stack authoring mode.
			m.stackMode = newStackModel(m.th, m.localRepo)
			m.stackMode, _ = m.stackMode.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
			m.mode = modeStack
			return m, nil
		case "r":
			cmds := []tea.Cmd{fetchViewer}
			s := &m.sections[m.active]
			s.loading = true
			s.err = nil
			cmds = append(cmds, loadSection(m.active, s.typ, s.filter, s.showClosed, s.closedLimit))
			m.headerLoading = true
			return m, tea.Batch(cmds...)
		}
		// Forward navigation to the active section's list. j/k/arrows move to the
		// next selectable row, skipping divider headers and wrapping at the ends.
		if len(m.sections) > 0 {
			lst := &m.sections[m.active].list
			if len(lst.Items()) > 0 && lst.FilterState() != list.Filtering {
				switch msg.String() {
				case "down", "j":
					selectAdjacent(lst, +1)
					return m, nil
				case "up", "k":
					selectAdjacent(lst, -1)
					return m, nil
				}
			}
			var cmd tea.Cmd
			m.sections[m.active].list, cmd = m.sections[m.active].list.Update(msg)
			ensureSelectable(&m.sections[m.active].list)
			return m, cmd
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
	switch m.mode {
	case modeDetail:
		m.detail.restyle(m.th)
	case modeStack:
		m.stackMode.th = m.th
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
	switch h.label {
	case "OPEN":
		s.openCollapsed = !s.openCollapsed
	case "CLOSED":
		s.closedCollapsed = !s.closedCollapsed
	}
	s.rebuildRows()
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
	it, ok := sel.(prItem)
	if !ok || !it.IsPR || it.Number == 0 {
		return m, nil
	}
	m.detail = newDetail(m.th, it.Item)
	m.detail, _ = m.detail.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
	m.mode = modeDetail
	return m, m.detail.Init()
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
	listH := bodyH - colHeaderH // the column-label row sits above the list
	if m.sidebarVisible() {
		listW = bw - stackPaneW - 1 // sidebar + vertical separator
		if listW < 20 {
			listW = 20
		}
		listH = bodyH - 2 - colHeaderH // the list pane also gains a title + rule
	}
	if listH < 1 {
		listH = 1
	}
	delegate := itemDelegate{th: m.th, width: listW}
	for i := range m.sections {
		m.sections[i].list.SetDelegate(delegate)
		m.sections[i].list.SetSize(listW, listH)
	}
}

// stackPaneW is the fixed width of the stack sidebar.
const stackPaneW = 34

// rebuildStacks reconstructs remote stacks from every loaded PR across all
// sections (deduped), so the sidebar can follow the selected PR.
func (m *Model) rebuildStacks() {
	seen := map[string]bool{}
	var refs []stack.PRRef
	// Iterate the stored matches, not the visible list rows, so folding a group
	// doesn't drop its PRs from the reconstructed sidebar.
	for i := range m.sections {
		for _, it := range append(append([]gh.Item{}, m.sections[i].open...), m.sections[i].closed...) {
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
	return renderBrandHeader(m.th, m.login, m.rate, m.headerLoading, m.headerErr, m.width)
}

// renderBrandHeader draws Cairn's two-row masthead — the brand line over the
// session/status line — as a Surface bar. It is a standalone function (not a
// Model method) so full-screen modes that take over the dashboard, like stack
// mode, can show the same top bar without duplicating it. headerH (2) must match
// the line count this returns.
func renderBrandHeader(th theme.Theme, login string, rate int, loading bool, herr error, width int) string {
	// Row 1 — brand. The mark + name get their own line so they read as a
	// masthead rather than competing with the status text.
	brand := lipgloss.NewStyle().Foreground(th.Primary).Bold(true).Render(logoGlyph + "  Cairn")
	tagline := lipgloss.NewStyle().Foreground(th.Muted).Render("   keyboard cockpit for GitHub")
	brandRow := surfaceBar(th, width, " "+brand+tagline)

	// Row 2 — session/status.
	var status string
	switch {
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

	sidebar := m.sidebarVisible()
	listW, listH := bw, bodyH-colHeaderH
	if sidebar {
		listW = bw - stackPaneW - 1
		listH = bodyH - 2 - colHeaderH
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
	body := lipgloss.JoinVertical(lipgloss.Left, colHead, listBody)

	if !sidebar {
		return body
	}

	// The list is the focused pane: a blue title (with a position counter so
	// it's obvious you're moving the PR list, not the stack) over a blue rule.
	label := s.title
	if pos, n := selectablePos(&s.list); n > 0 {
		label = fmt.Sprintf("%s  ▴ %d/%d ▾", s.title, pos, n)
	}
	listTitle := lipgloss.NewStyle().Width(listW).Foreground(m.th.Focus).Bold(true).Render(label)
	listRule := lipgloss.NewStyle().Foreground(m.th.Focus).Render(strings.Repeat("─", listW))
	listPane := lipgloss.JoinVertical(lipgloss.Left, listTitle, listRule, body)

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

	parts := []string{
		dim.Render("↑/↓ move"),
		dim.Render("n/N group"),
		dim.Render("←/→ section"),
		dim.Render("enter open / fold"),
		dim.Render("s sidebar"),
		stackHint,
		dim.Render("r refresh"),
		themeFooterHint(m.th),
		dim.Render("? help"),
		dim.Render("q quit"),
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
		label := lipgloss.NewStyle().Foreground(m.th.Muted).Bold(true).Render(name + " ")
		return label + strings.Join(items, dim.Render(" · "))
	}
	groupSep := lipgloss.NewStyle().Foreground(m.th.Overlay).Render("   │   ")
	legend := strings.Join([]string{
		group("CHECKS", mark(m.th.Success, "●", "passing"), mark(m.th.Danger, "●", "failing"), mark(m.th.Muted, "○", "none")),
		group("REVIEW", diamond, mark(m.th.Success, "✓", "approved"), mark(m.th.Danger, "✗", "changes"), mark(m.th.Muted, "◇", "others")),
		group("STATE", mark(m.th.Primary, "●", "merged"), mark(m.th.Muted, "●", "closed")),
	}, groupSep)

	box := lipgloss.NewStyle().Width(m.width).Padding(0, 1)
	// Legend on top, a rule, then keybindings — the whole stack painted as one
	// Surface bar so it matches the header and pops off the Base body.
	rule := lipgloss.NewStyle().Foreground(m.th.Overlay).Render(strings.Repeat("─", m.width))
	return surfaceBar(m.th, m.width,
		lipgloss.JoinVertical(lipgloss.Left, box.Render(legend), rule, box.Render(keys)))
}
