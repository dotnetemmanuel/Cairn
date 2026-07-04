package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/dotnetemmanuel/cairn/internal/gh"
	"github.com/dotnetemmanuel/cairn/internal/stack"
	"github.com/dotnetemmanuel/cairn/internal/townie"
)

// remoteMergePollMax caps the automatic re-fetches that wait out GitHub's
// asynchronous mergeability computation (UNKNOWN right after a merge/retarget), so
// the view resolves itself without a manual refresh but never polls forever.
const remoteMergePollMax = 4

// remotePollMsg fires after a short delay to re-fetch a repo whose mergeability
// was still computing on the previous fetch.
type remotePollMsg struct {
	repo    string
	attempt int
}

// Remote stack mode lets you land a stack you have NOT checked out — a teammate's
// stack whose PRs you can see on GitHub. It renders the PR-reconstructed tree
// (BuildRemoteStacks, the same data the sidebar uses) and its ship actions are
// pure GitHub API (merge / retarget children / delete), skipping the local
// git-town sync that local mode runs. You reach it with an explicit toggle (never
// auto-detected), pick a repo, then M / G / Y like local mode.

// remoteRepoEntry is one selectable repo in the chooser: its slug, owning org (for
// grouping), and how many open PRs it has (for the annotation).
type remoteRepoEntry struct {
	owner string
	repo  string // full owner/name slug
	prs   int
}

// remoteRepoMsg carries a freshly-fetched repo's reconstructed trees + landing
// readiness — used both when a repo is chosen and to refresh it after a ship.
// attempt counts the auto-poll retries that wait out GitHub's async mergeability.
type remoteRepoMsg struct {
	repo    string
	trees   []*stack.Tree
	merge   map[string]gh.PRMergeability
	attempt int
}

// fetchRemoteRepo loads a repo's open PRs, reconstructs its stack tree(s), and
// builds the landing-readiness map — one GraphQL round-trip for the whole remote
// view. Best-effort: a failure yields an empty result (the screen shows nothing
// to ship rather than erroring out). attempt is the poll counter (0 on a fresh
// fetch), carried through so the model can bound the auto-refresh.
func fetchRemoteRepo(repo string, attempt int) tea.Cmd {
	return func() tea.Msg {
		owner, name, ok := gh.SplitRepo(repo)
		if !ok {
			return remoteRepoMsg{repo: repo, attempt: attempt}
		}
		prs, err := gh.OpenPRs(owner, name)
		if err != nil {
			return remoteRepoMsg{repo: repo, attempt: attempt}
		}
		refs := make([]stack.PRRef, 0, len(prs))
		merge := make(map[string]gh.PRMergeability, len(prs))
		for _, p := range prs {
			refs = append(refs, stack.PRRef{Repo: repo, Number: p.Number, Head: p.Head, Base: p.Base})
			if _, seen := merge[p.Head]; !seen {
				merge[p.Head] = p.Mergeability()
			}
		}
		return remoteRepoMsg{repo: repo, trees: stack.BuildRemoteStacks(refs), merge: merge, attempt: attempt}
	}
}

// mergeStillComputing reports whether any PR in the map is in GitHub's transient
// UNKNOWN mergeable state — the cue to poll again shortly.
func mergeStillComputing(m map[string]gh.PRMergeability) bool {
	for _, v := range m {
		if v.Mergeable == "UNKNOWN" {
			return true
		}
	}
	return false
}

// toggleRemote flips between the local git-town tree and remote mode. Leaving
// remote restores the local stack untouched; entering it opens the repo chooser.
func (s stackModel) toggleRemote() (stackModel, tea.Cmd) {
	s.phase = stackBrowsing
	s.output, s.runErr, s.opName = "", nil, ""
	if s.remote {
		s.remote = false
		s.remoteRepo = ""
		return s, nil
	}
	s.remote = true
	s.remoteRepo = ""
	s.chooserCursor = 0
	return s, nil
}

// remoteChooserList returns the repos with at least one open PR, grouped by org
// (owner) then repo — the chooser's ordered, flat entry list. Built from the
// entry snapshot of the app's reconstructed stacks; no new inventory fetch.
func (s stackModel) remoteChooserList() []remoteRepoEntry {
	counts := map[string]int{}
	for _, t := range s.remoteStacks {
		if t == nil || t.Repo == "" {
			continue
		}
		for _, n := range t.Order {
			if n.HasPR {
				counts[t.Repo]++
			}
		}
	}
	out := make([]remoteRepoEntry, 0, len(counts))
	for slug, c := range counts {
		owner, _, _ := gh.SplitRepo(slug)
		out = append(out, remoteRepoEntry{owner: owner, repo: slug, prs: c})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].owner != out[j].owner {
			return out[i].owner < out[j].owner
		}
		return out[i].repo < out[j].repo
	})
	return out
}

// remoteChosenTrees returns the reconstructed trees for the chosen repo.
func (s stackModel) remoteChosenTrees() []*stack.Tree {
	var out []*stack.Tree
	for _, t := range s.remoteStacks {
		if t != nil && t.Repo == s.remoteRepo {
			out = append(out, t)
		}
	}
	return out
}

// remoteNodes flattens the chosen repo's trees into one navigable node list
// (trunk-first per tree), the cursor space for the remote tree.
func (s stackModel) remoteNodes() []*stack.Node {
	var out []*stack.Node
	for _, t := range s.remoteChosenTrees() {
		out = append(out, t.Order...)
	}
	return out
}

// remoteCursorNode is the node under the remote tree cursor, or nil.
func (s stackModel) remoteCursorNode() *stack.Node {
	nodes := s.remoteNodes()
	if s.remoteCursor >= 0 && s.remoteCursor < len(nodes) {
		return nodes[s.remoteCursor]
	}
	return nil
}

// remoteTreeOf returns the chosen-repo tree that contains branch, or nil.
func (s stackModel) remoteTreeOf(branch string) *stack.Tree {
	for _, t := range s.remoteChosenTrees() {
		if t.NodeByName(branch) != nil {
			return t
		}
	}
	return nil
}

// remoteShipChain returns branch's tree trunk and the bottom-up merge order from
// the stack's bottom up to and including branch — the remote analogue of
// shipChain. Empty chain when branch is the trunk or off-tree.
func (s stackModel) remoteShipChain(branch string) (trunk string, chain []string) {
	t := s.remoteTreeOf(branch)
	if t == nil || t.Root == nil {
		return "", nil
	}
	trunk = t.Root.Name
	n := t.NodeByName(branch)
	if n == nil || n.IsTrunk {
		return trunk, nil
	}
	for cur := n; cur != nil && !cur.IsTrunk; cur = t.NodeByName(cur.Parent) {
		chain = append(chain, cur.Name)
	}
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return trunk, chain
}

// remoteFullChain returns the ENTIRE linear stack containing branch — from the
// bottom (the trunk's direct child) up to the top leaf, following single children
// — in bottom-up merge order. This is what remote "merge whole stack" (G) lands:
// unlike local G (which ships bottom-up to the CHECKED-OUT branch), remote mode
// has no HEAD anchor, so the cursor only selects WHICH stack and G takes all of
// it. If the stack forks above some node (a branch with >1 child), the linear
// sequence stops there — a fork can't be squash-merged as one chain. Empty when
// branch is the trunk or off-tree.
func (s stackModel) remoteFullChain(branch string) (trunk string, chain []string) {
	t := s.remoteTreeOf(branch)
	if t == nil || t.Root == nil {
		return "", nil
	}
	trunk = t.Root.Name
	n := t.NodeByName(branch)
	if n == nil || n.IsTrunk {
		return trunk, nil
	}
	// Walk up to the bottom (the trunk's direct child).
	bottom := n
	for p := t.NodeByName(bottom.Parent); p != nil && !p.IsTrunk; p = t.NodeByName(bottom.Parent) {
		bottom = p
	}
	// Walk down through single children to the top leaf.
	for cur := bottom; cur != nil && !cur.IsTrunk; {
		chain = append(chain, cur.Name)
		if len(cur.Children) == 1 {
			cur = cur.Children[0]
		} else {
			break // leaf (0) or fork (>1): the linear stack ends here
		}
	}
	return trunk, chain
}

// remoteIsBottom reports whether n is the bottom of its stack (a direct child of
// its tree's trunk) — the only branch a single remote merge (M) can land.
func (s stackModel) remoteIsBottom(n *stack.Node) bool {
	if n == nil || n.IsTrunk {
		return false
	}
	t := s.remoteTreeOf(n.Name)
	return t != nil && t.Root != nil && n.Parent == t.Root.Name
}

// remoteBranchMergeable reports no known hard blocker for branch's PR. Permissive
// on missing data (readiness not loaded → allow the attempt, like local mode).
func (s stackModel) remoteBranchMergeable(branch string) bool {
	m, ok := s.remoteMerge[branch]
	if !ok {
		return s.remoteMerge == nil // loaded and absent = no open PR = not mergeable
	}
	return !m.Blocked()
}

// remoteShipEnabled / remoteShipStackEnabled / remoteReadyEnabled gate the three
// remote actions on the cursor node.
func (s stackModel) remoteShipEnabled(n *stack.Node) bool {
	return s.remoteIsBottom(n) && n.HasPR && s.remoteBranchMergeable(n.Name)
}

func (s stackModel) remoteShipStackEnabled(n *stack.Node) bool {
	if n == nil || n.IsTrunk {
		return false
	}
	_, chain := s.remoteFullChain(n.Name)
	return len(chain) > 0 && s.remoteBranchMergeable(chain[0])
}

func (s stackModel) remoteReadyEnabled(n *stack.Node) bool {
	if n == nil || n.IsTrunk || !n.HasPR {
		return false
	}
	m, ok := s.remoteMerge[n.Name]
	return ok && m.Draft
}

// --- remote key handling ---

// updateRemote routes keys while in remote mode: ctrl+r toggles back to local,
// then the chooser or the tree handler depending on whether a repo is selected.
func (s stackModel) updateRemote(msg tea.KeyMsg) (stackModel, tea.Cmd) {
	if msg.String() == "ctrl+r" {
		return s.toggleRemote()
	}
	if s.remoteRepo == "" {
		return s.updateRemoteChooser(msg)
	}
	return s.updateRemoteTree(msg)
}

func (s stackModel) updateRemoteChooser(msg tea.KeyMsg) (stackModel, tea.Cmd) {
	list := s.remoteChooserList()
	switch msg.String() {
	case "q":
		return s, func() tea.Msg { return stackExitMsg{} }
	case "esc":
		return s.toggleRemote() // step back to local mode
	case "j", "down":
		if n := len(list); n > 0 {
			s.chooserCursor = (s.chooserCursor + 1) % n
		}
	case "k", "up":
		if n := len(list); n > 0 {
			s.chooserCursor = (s.chooserCursor - 1 + n) % n
		}
	case "enter":
		if s.chooserCursor >= 0 && s.chooserCursor < len(list) {
			return s.selectRemoteRepo(list[s.chooserCursor].repo)
		}
	}
	return s, nil
}

// selectRemoteRepo opens a repo's remote stack view and fetches its fresh tree +
// readiness (the chooser snapshot may be stale).
func (s stackModel) selectRemoteRepo(slug string) (stackModel, tea.Cmd) {
	s.remoteRepo = slug
	s.remoteMerge = nil
	s.remoteCursor = 0
	// Park the cursor on the first feature branch (skip the trunk).
	for i, n := range s.remoteNodes() {
		if !n.IsTrunk {
			s.remoteCursor = i
			break
		}
	}
	return s, fetchRemoteRepo(slug, 0)
}

func (s stackModel) updateRemoteTree(msg tea.KeyMsg) (stackModel, tea.Cmd) {
	nodes := s.remoteNodes()
	switch msg.String() {
	case "q":
		return s, func() tea.Msg { return stackExitMsg{} }
	case "esc":
		s.remoteRepo = "" // back to the chooser
		return s, nil
	case "r":
		return s, fetchRemoteRepo(s.remoteRepo, 0)
	case "j", "down":
		if n := len(nodes); n > 0 {
			s.remoteCursor = (s.remoteCursor + 1) % n
		}
		return s, nil
	case "k", "up":
		if n := len(nodes); n > 0 {
			s.remoteCursor = (s.remoteCursor - 1 + n) % n
		}
		return s, nil
	case "M":
		if n := s.remoteCursorNode(); n != nil && s.remoteShipEnabled(n) {
			return s.startRemoteShip(n.Name)
		}
		return s, nil
	case "G":
		if n := s.remoteCursorNode(); n != nil && s.remoteShipStackEnabled(n) {
			return s.startRemoteShipStack(n.Name)
		}
		return s, nil
	case "Y":
		if n := s.remoteCursorNode(); n != nil && s.remoteReadyEnabled(n) {
			return s.startRemoteReady(n.Name)
		}
		return s, nil
	}
	return s, nil
}

func (s stackModel) startRemoteShip(branch string) (stackModel, tea.Cmd) {
	s.pending = townie.Command{Verb: "ship", Title: "merge", Mutates: true}
	s.opName = branch
	s.affected = nil
	s.phase = stackConfirming
	return s, nil
}

func (s stackModel) startRemoteShipStack(branch string) (stackModel, tea.Cmd) {
	_, chain := s.remoteFullChain(branch)
	s.pending = townie.Command{Verb: "shipstack", Title: "merge whole stack", Mutates: true}
	s.opName = branch
	s.affected = chain
	s.phase = stackConfirming
	return s, nil
}

func (s stackModel) startRemoteReady(branch string) (stackModel, tea.Cmd) {
	s.pending = townie.Command{Verb: "ready", Title: "mark ready", Mutates: true}
	s.opName = branch
	s.affected = nil
	s.phase = stackConfirming
	return s, nil
}

// runRemoteShip lands one bottom PR via the GitHub API (merge → retarget children
// → delete), skipping any local sync (no checkout of this repo).
func (s stackModel) runRemoteShip(branch string) (stackModel, tea.Cmd) {
	s.phase = stackRunning
	s.output = ""
	owner, repo, _ := gh.SplitRepo(s.remoteRepo)
	trunk, _ := s.remoteShipChain(branch)
	ops := s.ops
	return s, func() tea.Msg {
		return readStream(shipStream(owner, repo, branch, trunk, ops, false))
	}
}

// runRemoteShipStack lands a whole remote stack bottom-up, API-only.
func (s stackModel) runRemoteShipStack(anchor string) (stackModel, tea.Cmd) {
	s.phase = stackRunning
	s.opName = ""
	s.output = ""
	owner, repo, _ := gh.SplitRepo(s.remoteRepo)
	trunk, chain := s.remoteFullChain(anchor)
	ops := s.ops
	c := append([]string(nil), chain...)
	return s, func() tea.Msg {
		return readStream(shipStackStream(owner, repo, c, trunk, ops, false))
	}
}

// runRemoteReady marks a remote branch's draft PR ready for review.
func (s stackModel) runRemoteReady(branch string) (stackModel, tea.Cmd) {
	s.phase = stackRunning
	s.output = ""
	owner, repo, _ := gh.SplitRepo(s.remoteRepo)
	num := 0
	if m, ok := s.remoteMerge[branch]; ok {
		num = m.Number
	}
	return s, func() tea.Msg {
		return readStream(readyStream(owner, repo, branch, num))
	}
}

// --- remote rendering ---

// remoteStatusline is the remote-mode breadcrumb: the mode, the chosen repo (or a
// "choose a repo" prompt), and the toggle-back hint.
func (s stackModel) remoteStatusline(width int) string {
	pad := func(x string) string {
		return lipgloss.NewStyle().Width(width).Background(s.th.Surface).Padding(0, 1).Render(x)
	}
	sep := lipgloss.NewStyle().Foreground(s.th.Overlay).Render(" " + chevron + " ")
	mode := lipgloss.NewStyle().Foreground(s.th.Primary).Bold(true).Render("remote")
	target := lipgloss.NewStyle().Foreground(s.th.Muted).Render("choose a repo")
	if s.remoteRepo != "" {
		target = lipgloss.NewStyle().Foreground(s.th.Success).Render(iconRepo + " " + s.remoteRepo)
	}
	hint := lipgloss.NewStyle().Foreground(s.th.Muted).Render("ctrl+r → local")
	return pad(mode + sep + target + sep + hint)
}

func (s stackModel) renderRemoteChooser(w int) string {
	title := lipgloss.NewStyle().Foreground(s.th.Focus).Bold(true).Render("Remote stacks — choose a repo")
	rule := lipgloss.NewStyle().Foreground(s.th.Focus).Render(strings.Repeat("─", min(w, 80)))
	intro := mutedStyle(s.th).Render(wrapPlain(
		"Land a stack you have not checked out. These are the repos with open PRs "+
			"Cairn has loaded, grouped by org. Pick one to see its stacks and ship (M / G).",
		proseWidth(w), ""))

	list := s.remoteChooserList()
	if len(list) == 0 {
		empty := mutedStyle(s.th).Render(wrapPlain(
			"No repos with open PRs are loaded yet. Browse the Orgs / Involved tabs "+
				"first so their PRs load, then switch back to remote.", proseWidth(w), ""))
		return lipgloss.JoinVertical(lipgloss.Left, title, rule, "", intro, "", empty)
	}

	var rows []string
	lastOwner := ""
	for i, e := range list {
		if e.owner != lastOwner {
			rows = append(rows, lipgloss.NewStyle().Foreground(s.th.Accent2).Bold(true).Render(e.owner))
			lastOwner = e.owner
		}
		_, name, _ := gh.SplitRepo(e.repo)
		count := fmt.Sprintf("%d PR", e.prs)
		if e.prs != 1 {
			count += "s"
		}
		label := lipgloss.NewStyle().Foreground(s.th.Text).Render(name) +
			"  " + mutedStyle(s.th).Render(count)
		if i == s.chooserCursor {
			cursor := lipgloss.NewStyle().Foreground(s.th.Primary).Render(focusGlyph + " ")
			rows = append(rows, styledBar(s.th.Text, s.th.Surface, w, cursor+label))
		} else {
			rows = append(rows, "    "+label)
		}
	}
	return lipgloss.JoinVertical(lipgloss.Left, title, rule, "", intro, "", strings.Join(rows, "\n"))
}

// renderRemoteTree draws the chosen repo's PR-reconstructed stack(s), with the
// cursor branch highlighted and each PR flagged with its #number and readiness.
func (s stackModel) renderRemoteTree(w int) string {
	title := lipgloss.NewStyle().Foreground(s.th.Focus).Bold(true).Render("Remote stack")
	rule := lipgloss.NewStyle().Foreground(s.th.Focus).Render(strings.Repeat("─", w))
	nodes := s.remoteNodes()
	if len(nodes) == 0 {
		body := mutedStyle(s.th).Render("  no open PRs\n  in this repo")
		return lipgloss.JoinVertical(lipgloss.Left, title, rule, body)
	}
	var b strings.Builder
	for i, n := range nodes {
		indent := strings.Repeat("  ", n.Depth)
		nameStyle := lipgloss.NewStyle().Foreground(s.th.Text)
		if n.IsTrunk {
			nameStyle = lipgloss.NewStyle().Foreground(s.th.Muted)
		}
		marker := "  "
		if i == s.remoteCursor {
			marker = lipgloss.NewStyle().Foreground(s.th.Focus).Bold(true).Render(focusGlyph + " ")
			nameStyle = nameStyle.Bold(true)
		}
		suffix := ""
		if n.PRNumber > 0 {
			suffix += " " + lipgloss.NewStyle().Foreground(s.th.Accent2).Render(fmt.Sprintf("#%d", n.PRNumber))
		}
		if !n.IsTrunk {
			if m, ok := s.remoteMerge[n.Name]; ok {
				if m.Draft {
					suffix += " " + lipgloss.NewStyle().Foreground(s.th.Warning).Render("draft")
				} else if m.Mergeable == "CONFLICTING" {
					suffix += " " + lipgloss.NewStyle().Foreground(s.th.Warning).Render("conflicts")
				}
			} else if s.remoteMerge != nil && n.PRNumber == 0 {
				suffix += " " + lipgloss.NewStyle().Foreground(s.th.Muted).Render("no PR")
			}
		}
		used := 2 + len(indent) + lipgloss.Width(suffix)
		b.WriteString(marker + indent + nameStyle.Render(truncate(n.Name, w-used-1)) + suffix + "\n")
	}
	return lipgloss.JoinVertical(lipgloss.Left, title, rule, b.String())
}

// renderRemoteActions is the right pane in remote tree mode: the ship actions for
// the cursor branch, each enabled or dimmed-and-explained like local mode.
func (s stackModel) renderRemoteActions(w int) string {
	title := lipgloss.NewStyle().Foreground(s.th.Focus).Bold(true).Render("Remote actions")
	rule := lipgloss.NewStyle().Foreground(s.th.Focus).Render(strings.Repeat("─", w))
	n := s.remoteCursorNode()
	if n == nil {
		return lipgloss.JoinVertical(lipgloss.Left, title, rule, "",
			mutedStyle(s.th).Render("  select a branch"))
	}

	trunk := ""
	if t := s.remoteTreeOf(n.Name); t != nil && t.Root != nil {
		trunk = t.Root.Name
	}
	row := func(key, label, desc string, on bool) string {
		k := lipgloss.NewStyle().Foreground(s.th.Accent2)
		lab := lipgloss.NewStyle().Foreground(s.th.Text).Bold(true)
		d := lipgloss.NewStyle().Foreground(s.th.Muted)
		if !on {
			k = lipgloss.NewStyle().Foreground(s.th.Muted)
			lab = lipgloss.NewStyle().Foreground(s.th.Muted)
		}
		return fmt.Sprintf("  %s  %s — %s", k.Render(key), lab.Render(label),
			colorBranches(d, s.th, desc, s.allBranchNames()))
	}

	shipDesc := fmt.Sprintf("merge %s and retarget its children onto %s", n.Name, val(trunk))
	if !s.remoteShipEnabled(n) {
		shipDesc = s.remoteShipReason(n)
	}
	stackDesc := "merge the whole stack, bottom to top"
	if !s.remoteShipStackEnabled(n) {
		stackDesc = s.remoteShipStackReason(n)
	}
	readyDesc := fmt.Sprintf("take %s out of draft", n.Name)
	if !s.remoteReadyEnabled(n) {
		readyDesc = s.remoteReadyReason(n)
	}

	rows := []string{
		row("M", "merge", shipDesc, s.remoteShipEnabled(n)),
		row("G", "merge whole stack", stackDesc, s.remoteShipStackEnabled(n)),
		row("Y", "mark ready", readyDesc, s.remoteReadyEnabled(n)),
	}
	help := mutedStyle(s.th).Render(wrapPlain(
		"Remote actions are GitHub-only: merge, retarget children, delete branch. "+
			"Local-only steps (sync/restack/amend) are not available here.", proseWidth(w), "  "))
	return lipgloss.JoinVertical(lipgloss.Left, title, rule, "", strings.Join(rows, "\n"), "", help)
}

func (s stackModel) remoteShipReason(n *stack.Node) string {
	if n.IsTrunk {
		return "the trunk can't be merged"
	}
	if !s.remoteIsBottom(n) {
		if _, chain := s.remoteShipChain(n.Name); len(chain) > 0 {
			return fmt.Sprintf("only the bottom branch (%s) can merge alone — use G for the stack", chain[0])
		}
		return "only the bottom branch can merge alone"
	}
	if !n.HasPR {
		return "no open PR to merge"
	}
	if m, ok := s.remoteMerge[n.Name]; ok && m.Reason() != "" {
		return fmt.Sprintf("%s — %s", m.Reason(), remoteFixHint(m))
	}
	return "can't merge right now"
}

// remoteFixHint is the remote-mode fix for a blocked PR. Unlike the local hint it
// never says "sync (S)" — there is no checkout of this repo to sync. A draft is
// fixed with Y (which works remotely); a conflict needs a local rebase by whoever
// has the branch, so remote mode can only report it, not fix it.
func remoteFixHint(m gh.PRMergeability) string {
	switch {
	case m.Draft:
		return "press Y to mark it ready"
	case m.Mergeable == "CONFLICTING":
		return "it needs a local rebase to resolve the conflict before it can land"
	default:
		return ""
	}
}

func (s stackModel) remoteShipStackReason(n *stack.Node) string {
	if n.IsTrunk {
		return "the trunk can't be merged"
	}
	_, chain := s.remoteFullChain(n.Name)
	if len(chain) == 0 {
		return "nothing to ship"
	}
	if m, ok := s.remoteMerge[chain[0]]; ok && m.Reason() != "" {
		return fmt.Sprintf("bottom branch %s %s — %s", chain[0], m.Reason(), remoteFixHint(m))
	}
	return "the bottom PR can't land yet"
}

func (s stackModel) remoteReadyReason(n *stack.Node) string {
	if n.IsTrunk {
		return "the trunk has no PR"
	}
	if !n.HasPR {
		return "no open PR"
	}
	return "not a draft"
}

// renderRemoteShipConfirm / renderRemoteShipStackConfirm are the remote ship gates
// — like the local ones but phrased for the API-only flow (no local sync), with
// the partial-ship cosmetic caveat spelled out for a single merge.
func (s stackModel) renderRemoteShipConfirm(w int) string {
	trunk, _ := s.remoteShipChain(s.opName)
	textW := proseWidth(w)
	title := lipgloss.NewStyle().Foreground(s.th.Primary).Bold(true).Render("merge " + s.opName + " (remote) — confirm")
	rule := lipgloss.NewStyle().Foreground(s.th.Focus).Render(strings.Repeat("─", w))
	what := lipgloss.NewStyle().Foreground(s.th.Text).Render(wrapPlain(
		"Squash-merges this PR into "+val(trunk)+" on GitHub, retargets its child PRs onto "+
			val(trunk)+", and deletes the branch. Nothing on your machine is touched — this "+
			"repo isn't checked out.", textW, ""))
	caveat := lipgloss.NewStyle().Foreground(s.th.Warning).Render(wrapPlain(
		"⚠ Merging just this branch leaves the retargeted child PRs carrying its "+
			"commits until someone rebases them; the PR diffs GitHub shows stay correct. "+
			"Use G to land the whole stack and avoid it.", textW, "  "))
	cmdLine := lipgloss.NewStyle().Foreground(s.th.Muted).Render("runs:  gh: merge PR (squash) → retarget children → delete branch")
	confirm := lipgloss.NewStyle().Foreground(s.th.Success).Render("[enter] merge") +
		mutedStyle(s.th).Render("    [esc] cancel")
	return lipgloss.JoinVertical(lipgloss.Left, title, rule, "",
		lipgloss.NewStyle().Foreground(s.th.Muted).Render("What this does:"), what, "",
		caveat, "", cmdLine, "", confirm)
}

func (s stackModel) renderRemoteShipStackConfirm(w int) string {
	trunk, _ := s.remoteFullChain(s.opName)
	branches := s.affected
	textW := proseWidth(w)
	noun := "branches"
	if len(branches) == 1 {
		noun = "branch"
	}
	warn := lipgloss.NewStyle().Foreground(s.th.Danger).Bold(true).Render(
		wrapPlain("⚠ SERIOUS — this merges the ENTIRE stack on the remote and cannot be undone.", textW, "  "))
	title := lipgloss.NewStyle().Foreground(s.th.Primary).Bold(true).Render(
		fmt.Sprintf("merge whole stack (%d %s, remote) — confirm", len(branches), noun))
	rule := lipgloss.NewStyle().Foreground(s.th.Danger).Render(strings.Repeat("─", w))

	var rows []string
	for i, b := range branches {
		numStr := "no PR"
		mark, markColor, note := "✓", s.th.Success, ""
		if m, ok := s.remoteMerge[b]; ok {
			numStr = fmt.Sprintf("#%d", m.Number)
			switch {
			case m.Draft:
				mark, markColor, note = "⚠", s.th.Danger, "still a draft — stops here"
			case m.Mergeable == "CONFLICTING":
				if i == 0 {
					mark, markColor, note = "⚠", s.th.Danger, "conflicts with the trunk — stops here"
				} else {
					mark, markColor, note = "⚠", s.th.Warning, "conflicts with its base — may stop here"
				}
			case m.Mergeable == "UNKNOWN":
				mark, markColor, note = "?", s.th.Warning, "mergeability still computing"
			case m.Caution() != "":
				mark, markColor, note = "?", s.th.Warning, m.Caution()
			}
		} else if s.remoteMerge != nil {
			mark, markColor, note = "⚠", s.th.Danger, "no open PR — stops here"
		} else {
			mark, markColor, note = "?", s.th.Warning, "readiness not loaded"
		}
		line := fmt.Sprintf("%2d. %s %s  %s", i+1,
			lipgloss.NewStyle().Foreground(markColor).Render(mark),
			lipgloss.NewStyle().Foreground(s.th.Accent2).Render(b),
			lipgloss.NewStyle().Foreground(s.th.Muted).Render(numStr))
		if note != "" {
			line += "  " + lipgloss.NewStyle().Foreground(markColor).Render(note)
		}
		rows = append(rows, line)
	}
	orderNote := mutedStyle(s.th).Render(wrapPlain(fmt.Sprintf(
		"Merged bottom-up into %s, in this order. If one can't merge, the branches "+
			"below it still land and everything above is left untouched. No local sync — "+
			"this repo isn't checked out.", val(trunk)), textW, ""))
	cmdLine := lipgloss.NewStyle().Foreground(s.th.Muted).Render("runs:  for each branch bottom-up: gh merge (squash) → retarget children → delete")
	confirm := lipgloss.NewStyle().Foreground(s.th.Danger).Bold(true).Render(fmt.Sprintf("[enter] merge %d %s", len(branches), noun)) +
		mutedStyle(s.th).Render("    [esc] cancel")
	return lipgloss.JoinVertical(lipgloss.Left, warn, "", title, rule, "",
		lipgloss.NewStyle().Foreground(s.th.Muted).Render("Merge order (bottom-up) — readiness:"),
		strings.Join(rows, "\n"), "", orderNote, "", cmdLine, "", confirm)
}

// renderRemote is the whole remote-mode body (below the statusline): the chooser,
// or the chosen repo's tree + right pane (actions / confirm / output by phase).
func (s stackModel) renderRemote(w, bodyH int) string {
	if s.remoteRepo == "" {
		return indentBody(lipgloss.NewStyle().Width(bodyWidth(w)).Height(bodyH).Render(s.renderRemoteChooser(bodyWidth(w))))
	}
	treeW := s.treeWidth()
	rightW := bodyWidth(w) - treeW - 1
	if rightW < 20 {
		rightW = 20
	}
	left := lipgloss.NewStyle().Width(treeW).Height(bodyH).Render(s.renderRemoteTree(treeW))
	right := lipgloss.NewStyle().Width(rightW).Height(bodyH).Render(s.renderRemoteRight(rightW))
	return indentBody(lipgloss.JoinHorizontal(lipgloss.Top, left, stackVBar(s.th, bodyH), right))
}

// renderRemoteRight switches the remote right pane by phase.
func (s stackModel) renderRemoteRight(w int) string {
	switch s.phase {
	case stackConfirming:
		switch s.pending.Verb {
		case "ship":
			return s.renderRemoteShipConfirm(w)
		case "shipstack":
			return s.renderRemoteShipStackConfirm(w)
		default:
			return s.renderConfirm(w) // ready reuses the generic confirm
		}
	case stackRunning, stackDone:
		return s.renderOutput(w)
	default:
		return s.renderRemoteActions(w)
	}
}
