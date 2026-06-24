package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/dotnetemmanuel/cairn/internal/gh"
	"github.com/dotnetemmanuel/cairn/internal/stack"
	"github.com/dotnetemmanuel/cairn/internal/theme"
	"github.com/dotnetemmanuel/cairn/internal/townie"
)

// stackPhase is the sub-state of the stack mode's right column.
type stackPhase int

const (
	stackBrowsing   stackPhase = iota // navigating the action list
	stackNaming                       // typing a branch name (new / insert)
	stackConfirming                   // the explained confirmation is shown
	stackRunning                      // the git-town op is in flight
	stackDone                         // op finished; output + result shown
)

// stackExitMsg tells the root model to return to the dashboard.
type stackExitMsg struct{}

// stackStreamMsg carries one unit of a delegated op's streamed output: a line as
// it arrives, or the terminal completion event. ch lets Update re-arm the next
// read without the model holding channel state.
type stackStreamMsg struct {
	ch <-chan townie.StreamEvent
	ev townie.StreamEvent
}

// stackModel is the dedicated, local-context stack authoring screen — Cairn's
// mutation surface. It operates on the cwd repo's CURRENT branch (never the
// browsed PR), gated on being in a git-town repo on a real branch. Its three
// zones: a statusline ("here you are"), a navigable action list ("what you can
// do"), and an explained confirmation ("what it means") that streams the op's
// output and reloads the tree.
type stackModel struct {
	th     theme.Theme
	width  int
	height int

	repo   string
	status stack.RepoStatus
	tree   *stack.Tree // local git-town tree (drift-aware); nil when no git-town
	trunk  string      // best-effort trunk guess, for the git-town init prompt

	commands []townie.Command
	cursor   int

	// Pane focus: the action list (right) or the branch tree (left, for checkout).
	focus      stackFocus
	treeCursor int // index into tree.Order when the tree is focused

	phase    stackPhase
	pending  townie.Command
	affected []string
	name     textinput.Model
	opName   string // branch name of the op currently running/shown

	ops    townie.Ops
	output string
	runErr error
}

// stackFocus selects which pane receives navigation keys.
type stackFocus int

const (
	focusActions stackFocus = iota
	focusTree
)

// newStackModelBare builds the screen without touching git — its tree/status are
// left zero. Used by newStackModel (which then reloads) and by tests that inject
// a known fixture.
func newStackModelBare(th theme.Theme, repo string) stackModel {
	ti := textinput.New()
	ti.Placeholder = "branch-name"
	ti.Prompt = "› "
	ti.CharLimit = 60

	return stackModel{
		th:       th,
		repo:     repo,
		commands: townie.Catalog(),
		name:     ti,
		ops:      townie.New(""), // cwd
	}
}

// newStackModel builds the screen for the cwd repo, reading the local tree +
// working-tree status up front.
func newStackModel(th theme.Theme, repo string) stackModel {
	s := newStackModelBare(th, repo)
	s.reload()
	return s
}

// reload re-reads the local lineage and working-tree status. Called on entry and
// after every mutation so the tree and statusline reflect git-town's new reality.
func (s *stackModel) reload() {
	s.status = stack.Status("")
	if t, err := stack.Load(""); err == nil {
		s.tree = t
	} else {
		s.tree = nil
	}
	// Park the tree cursor on the current branch so checkout starts from "here".
	if s.tree != nil {
		if i := s.tree.IndexOf(s.status.Branch); i >= 0 {
			s.treeCursor = i
		} else if s.treeCursor >= len(s.tree.Order) {
			s.treeCursor = 0
		}
	}
	// When the repo lacks git-town, guess a trunk to pre-fill the init prompt.
	if s.status.InRepo && s.tree == nil {
		s.trunk = stack.DetectTrunk("")
	}
}

// needsInit reports whether the cwd is a git repo that has no git-town config —
// the state where Cairn offers to initialize git-town instead of dead-ending.
func (s stackModel) needsInit() bool {
	return s.status.InRepo && !s.hasGitTown()
}

// initCommand is the synthetic command behind the "set up git-town" affordance.
// It is not in townie.Catalog() (it's contextual, shown only when git-town is
// missing), but it routes through the same naming → confirm → run flow.
func initCommand() townie.Command {
	return townie.Command{
		Verb: "init", Title: "set up git-town", NeedsName: true, Mutates: true,
		Short: "configure this repo for stacks",
		Long: "Sets up git-town for this repo: marks a trunk branch (your stack's " +
			"base, usually main) and standardizes on rebase syncing. This only writes " +
			"local .git/config — nothing is committed, staged, or pushed, and no file " +
			"is added to the repo. Afterwards the stack commands light up.",
	}
}

// hasGitTown reports whether the cwd repo has a git-town stack configured.
func (s stackModel) hasGitTown() bool { return s.tree != nil }

// enabled reports whether stack mutations can run: in a git-town repo, on a real
// (non-detached) branch.
func (s stackModel) enabled() bool {
	return s.status.InRepo && s.hasGitTown() && !s.status.Detached && s.status.Branch != ""
}

// onTrackedBranch reports whether the current branch is a node git-town knows
// (in the lineage tree, trunk included). The stack-maintenance verbs need this.
func (s stackModel) onTrackedBranch() bool {
	return s.tree != nil && s.tree.NodeByName(s.status.Branch) != nil
}

// isBottomBranch reports whether the current branch is the bottom of its stack —
// a direct child of the trunk. Only the bottom can be shipped (merged): its PR
// targets the trunk, while higher branches target the branch below them.
func (s stackModel) isBottomBranch() bool {
	if s.tree == nil || s.tree.Root == nil {
		return false
	}
	n := s.tree.NodeByName(s.status.Branch)
	return n != nil && !n.IsTrunk && n.Parent == s.tree.Root.Name
}

// actionEnabled reports whether a specific command is currently actionable.
func (s stackModel) actionEnabled(c townie.Command) bool {
	if !s.enabled() {
		return false
	}
	switch c.Verb {
	case "amend":
		// Folds STAGED changes (nudge `git add` first) into a tracked branch.
		return s.onTrackedBranch() && s.status.Staged > 0
	case "restack", "sync":
		// Maintain an existing stack — only meaningful on a branch git-town
		// tracks; otherwise it'd act on a stack it doesn't know.
		return s.onTrackedBranch()
	case "ship":
		// Only the bottom of the stack can be merged: a stacked PR targets the
		// branch below it, so lower branches must land first.
		return s.isBottomBranch()
	default:
		// new / insert can start or extend a stack from any branch.
		return true
	}
}

// capturing reports whether a text field should receive raw keys (so global
// shortcuts like '?' are typed literally, not intercepted).
func (s stackModel) capturing() bool { return s.phase == stackNaming }

func (s stackModel) Update(msg tea.Msg) (stackModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		s.width, s.height = msg.Width, msg.Height
		return s, nil

	case stackStreamMsg:
		if msg.ev.Done {
			s.runErr = msg.ev.Err
			s.phase = stackDone
			s.reload() // tree + status now reflect what git-town did
			// A failed op that left unmerged paths is a conflict — hand off to the
			// full-screen resolver instead of just showing the error.
			if s.runErr != nil && s.status.Conflicts > 0 {
				return s, func() tea.Msg { return enterConflictMsg{dir: "", gitTown: true} }
			}
			return s, nil
		}
		// Append the line as it streams in; keep reading the next.
		if s.output != "" {
			s.output += "\n"
		}
		s.output += msg.ev.Line
		return s, waitStream(msg.ch)

	case tea.KeyMsg:
		switch s.phase {
		case stackNaming:
			return s.updateNaming(msg)
		case stackConfirming:
			return s.updateConfirming(msg)
		case stackRunning:
			return s, nil // ignore input while the op runs
		case stackDone:
			// Any key dismisses the result and returns to the list.
			s.phase = stackBrowsing
			s.output, s.runErr = "", nil
			return s, nil
		default:
			return s.updateBrowsing(msg)
		}
	}
	return s, nil
}

func (s stackModel) updateBrowsing(msg tea.KeyMsg) (stackModel, tea.Cmd) {
	// Mid-conflict, R opens the resolver. This takes precedence over the restack
	// accelerator (also R) — which is inert during a conflict — but only while
	// conflicts exist, so restack keeps R the rest of the time. The working dir is
	// cwd (""), the dir townie/stack operate in; s.repo is owner/name, not a path.
	if msg.String() == "R" && s.status.Conflicts > 0 {
		return s, func() tea.Msg { return enterConflictMsg{dir: ""} }
	}
	switch msg.String() {
	case "esc", "q":
		return s, func() tea.Msg { return stackExitMsg{} }
	case "r":
		// Re-read lineage + working-tree status, so external git changes (a
		// checkout/commit in another terminal) show without leaving stack mode.
		s.reload()
		return s, nil
	case "tab", "shift+tab", "h", "l", "left", "right":
		// Toggle focus between the action list and the branch tree (only when
		// there's a tree to check out from).
		if s.tree != nil {
			if s.focus == focusActions {
				s.focus = focusTree
			} else {
				s.focus = focusActions
			}
		}
		return s, nil
	}

	// Direct command-key accelerators (n/I/S/R/A) — the keys the help and footer
	// advertise. They work from either pane: a mutation always acts on the
	// checked-out branch (the HEAD model), independent of the tree cursor. Guard
	// with !needsInit so they stay inert until git-town is set up.
	if !s.needsInit() {
		if c := townie.Find(msg.String()); c != nil {
			s.cursor = s.commandIndex(c.Key)
			return s.triggerAction(*c)
		}
	}

	if s.focus == focusTree {
		return s.updateTree(msg)
	}

	// No git-town here: the right pane is the init call-to-action, so enter starts
	// the guided setup (pre-filled with the detected trunk) rather than the list.
	if s.needsInit() {
		if msg.String() == "enter" {
			s.pending = initCommand()
			s.name.SetValue(s.trunk)
			s.name.CursorEnd()
			s.name.Focus()
			s.phase = stackNaming
			return s, textinput.Blink
		}
		return s, nil
	}

	switch msg.String() {
	case "j", "down":
		s.cursor = (s.cursor + 1) % len(s.commands)
	case "k", "up":
		s.cursor = (s.cursor - 1 + len(s.commands)) % len(s.commands)
	case "enter":
		return s.triggerAction(s.commands[s.cursor])
	}
	return s, nil
}

// commandIndex returns the action-list index for a command key (for cursor
// feedback when a key accelerator fires), falling back to the current cursor.
func (s stackModel) commandIndex(key string) int {
	for i := range s.commands {
		if s.commands[i].Key == key {
			return i
		}
	}
	return s.cursor
}

// triggerAction starts a command: opens the name prompt for new/insert, or the
// explained confirmation otherwise. Inert when the command isn't currently
// actionable (gated). Shared by Enter and the key accelerators.
func (s stackModel) triggerAction(c townie.Command) (stackModel, tea.Cmd) {
	if !s.actionEnabled(c) {
		return s, nil
	}
	s.pending = c
	if c.NeedsName {
		s.name.SetValue("")
		s.name.Focus()
		s.phase = stackNaming
		return s, textinput.Blink
	}
	// No name for this verb — clear any value left over from a previous new/insert
	// so the confirmation headline reads "restack" not "restack <stale-name>".
	s.name.SetValue("")
	s.affected = s.affectedBranches(c, "")
	s.phase = stackConfirming
	return s, nil
}

// updateTree handles navigation in the branch tree: j/k move the cursor, enter
// checks out the cursored branch. Checkout is cheap and reversible, so it skips
// the confirmation dialog — it just runs and reloads.
func (s stackModel) updateTree(msg tea.KeyMsg) (stackModel, tea.Cmd) {
	if s.tree == nil || len(s.tree.Order) == 0 {
		return s, nil
	}
	n := len(s.tree.Order)
	switch msg.String() {
	case "j", "down":
		s.treeCursor = (s.treeCursor + 1) % n
	case "k", "up":
		s.treeCursor = (s.treeCursor - 1 + n) % n
	case "enter":
		target := s.tree.Order[s.treeCursor]
		if target.Name == s.status.Branch {
			return s, nil // already here
		}
		return s.runOp("checkout", target.Name, "checkout")
	}
	return s, nil
}

// runOp launches a delegated op (mutation or checkout) and moves to the running
// phase. opName is the branch the op concerns (for the output header).
func (s stackModel) runOp(verb, name, title string) (stackModel, tea.Cmd) {
	s.phase = stackRunning
	s.pending = townie.Command{Verb: verb, Title: title}
	s.opName = name
	s.output = ""
	ops := s.ops
	// Start streaming inside the Cmd (not here) so the process only launches when
	// bubbletea runs the command — keeps unit tests that merely assert a non-nil
	// Cmd from shelling out for real.
	return s, func() tea.Msg {
		return readStream(ops.Stream(verb, name))
	}
}

// waitStream reads the next stream event as a tea.Cmd.
func waitStream(ch <-chan townie.StreamEvent) tea.Cmd {
	return func() tea.Msg { return readStream(ch) }
}

// readStream blocks for one event and wraps it, carrying ch so Update can re-arm
// the next read. A closed channel is treated as completion.
func readStream(ch <-chan townie.StreamEvent) stackStreamMsg {
	ev, ok := <-ch
	if !ok {
		ev = townie.StreamEvent{Done: true}
	}
	return stackStreamMsg{ch: ch, ev: ev}
}

// runShip merges branch's PR (via gh) then syncs the stack (via git-town),
// streaming progress through the same machinery as runOp. ship is not a git-town
// verb — merging a PR is a remote action (gh), re-parenting the stack is local
// (git town sync) — so it's orchestrated here rather than in townie.
func (s stackModel) runShip(branch string) (stackModel, tea.Cmd) {
	s.phase = stackRunning
	s.pending = townie.Command{Verb: "ship", Title: "merge"}
	s.opName = branch
	s.output = ""
	owner, repo, _ := gh.SplitRepo(s.repo)
	trunk := ""
	if s.tree != nil && s.tree.Root != nil {
		trunk = s.tree.Root.Name
	}
	ops := s.ops
	return s, func() tea.Msg {
		return readStream(shipStream(owner, repo, branch, trunk, ops))
	}
}

// shipStream lands a stack's bottom branch: merge its PR, retarget the child PRs
// to the trunk, delete the branch, then git town sync to re-parent locally —
// forwarding output. Stops (with the error) if the lookup or merge fails, so a
// failed merge never proceeds.
func shipStream(owner, repo, branch, trunk string, ops townie.Ops) <-chan townie.StreamEvent {
	ch := make(chan townie.StreamEvent, 64)
	go func() {
		defer close(ch)
		if owner == "" || repo == "" {
			ch <- townie.StreamEvent{Done: true, Err: fmt.Errorf("can't ship: unknown repo")}
			return
		}
		ch <- townie.StreamEvent{Line: "Looking up the open PR for " + branch + "…"}
		num, err := gh.FindPROpenForBranch(owner, repo, branch)
		if err != nil {
			ch <- townie.StreamEvent{Done: true, Err: err}
			return
		}
		if num == 0 {
			ch <- townie.StreamEvent{Done: true, Err: fmt.Errorf("no open PR found for %s", branch)}
			return
		}
		ch <- townie.StreamEvent{Line: fmt.Sprintf("Merging PR #%d (%s) on GitHub…", num, branch)}
		if err := gh.MergePR(owner, repo, num, "squash"); err != nil {
			ch <- townie.StreamEvent{Done: true, Err: err}
			return
		}
		// Retarget the PRs that pointed at this branch onto the trunk BEFORE
		// deleting it — otherwise deleting the branch closes those child PRs
		// instead of letting them follow the stack down.
		if trunk != "" {
			if kids, err := gh.PRsWithBase(owner, repo, branch); err == nil {
				for _, kid := range kids {
					ch <- townie.StreamEvent{Line: fmt.Sprintf("Retargeting PR #%d onto %s…", kid, trunk)}
					if err := gh.RetargetPR(owner, repo, kid, trunk); err != nil {
						ch <- townie.StreamEvent{Line: "  (could not retarget #" + fmt.Sprint(kid) + ": " + err.Error() + ")"}
					}
				}
			}
		}
		// Delete the merged branch's remote so sync treats it as shipped (delete +
		// re-parent children) rather than rebasing the squashed commits — which
		// would conflict. Best-effort: if the repo already auto-deleted it, fine.
		ch <- townie.StreamEvent{Line: fmt.Sprintf("Merged PR #%d. Removing the merged branch…", num)}
		if err := gh.DeleteRemoteBranch(owner, repo, branch); err != nil {
			ch <- townie.StreamEvent{Line: "  (could not delete remote branch: " + err.Error() + ")"}
		}
		ch <- townie.StreamEvent{Line: "Syncing the stack…"}
		for ev := range ops.Stream("sync", "") {
			if ev.Done {
				if ev.Err != nil {
					ch <- townie.StreamEvent{Done: true, Err: ev.Err}
					return
				}
				break
			}
			ch <- ev
		}
		ch <- townie.StreamEvent{Done: true}
	}()
	return ch
}

func (s stackModel) updateNaming(msg tea.KeyMsg) (stackModel, tea.Cmd) {
	switch msg.String() {
	case "esc":
		s.name.Blur()
		s.phase = stackBrowsing
		return s, nil
	case "enter":
		if strings.TrimSpace(s.name.Value()) == "" {
			return s, nil
		}
		s.name.Blur()
		s.affected = s.affectedBranches(s.pending, strings.TrimSpace(s.name.Value()))
		s.phase = stackConfirming
		return s, nil
	}
	var cmd tea.Cmd
	s.name, cmd = s.name.Update(msg)
	return s, cmd
}

func (s stackModel) updateConfirming(msg tea.KeyMsg) (stackModel, tea.Cmd) {
	switch msg.String() {
	case "esc":
		s.phase = stackBrowsing
		return s, nil
	case "enter":
		// ship isn't a git-town verb: merge the PR via gh, then sync. It always
		// acts on the current (bottom) branch.
		if s.pending.Verb == "ship" {
			return s.runShip(s.status.Branch)
		}
		return s.runOp(s.pending.Verb, strings.TrimSpace(s.name.Value()), s.pending.Title)
	}
	return s, nil
}

// affectedBranches names the branches a verb will rebase/rewrite, computed from
// the local lineage so the confirmation can be concrete ("rebases X and Y")
// rather than generic. name is the new branch for new/insert.
func (s stackModel) affectedBranches(c townie.Command, name string) []string {
	cur := s.status.Branch
	switch c.Verb {
	case "amend":
		// The amend rewrites cur's commit; only the branches ABOVE it must rebase.
		return s.descendants(cur)
	case "insert":
		// The new branch slots under cur; cur and everything above it re-parents.
		return append([]string{cur}, s.descendants(cur)...)
	case "restack", "sync":
		// sync --stack [--no-push] operates on the WHOLE stack the branch belongs
		// to (ancestors included), not just descendants of cur.
		var all []string
		if s.tree != nil {
			for _, n := range s.tree.Order {
				if !n.IsTrunk {
					all = append(all, n.Name)
				}
			}
		}
		return all
	case "ship":
		// Merging cur re-parents the branches above it onto the trunk.
		return s.descendants(cur)
	default: // new — creates a leaf, rebases nothing
		return nil
	}
}

// View renders the three zones. spinnerFrame is threaded from the root model so
// the running state animates with the same spinner as the rest of the app.
func (s stackModel) View(spinnerFrame string) string {
	statusline := renderStatusline(s.th, s.repo, s.status, s.hasGitTown(), s.width)

	bodyH := s.height - 1 /*statusline*/ - 1 /*footer*/
	if bodyH < 1 {
		bodyH = 1
	}

	treeW := stackPaneW
	rightW := s.width - treeW - 1
	if rightW < 20 {
		rightW = 20
	}

	left := lipgloss.NewStyle().Width(treeW).Height(bodyH).Render(s.renderLocalTree(treeW))
	right := lipgloss.NewStyle().Width(rightW).Height(bodyH).Render(s.renderRight(rightW))
	body := lipgloss.JoinHorizontal(lipgloss.Top, left, stackVBar(s.th, bodyH), right)

	return lipgloss.JoinVertical(lipgloss.Left, statusline, body, s.viewFooter(spinnerFrame))
}

// renderLocalTree draws the cwd repo's git-town stack with the CURRENT branch
// highlighted — the visual companion to the statusline's textual "where am I".
func (s stackModel) renderLocalTree(w int) string {
	focused := s.focus == focusTree
	titleStyle := lipgloss.NewStyle().Width(w).Foreground(s.th.Muted)
	ruleColor := s.th.Overlay
	label := "Local stack (cwd)"
	if focused {
		titleStyle = titleStyle.Foreground(s.th.Focus).Bold(true)
		ruleColor = s.th.Focus
	}
	title := titleStyle.Render(label)
	rule := lipgloss.NewStyle().Foreground(ruleColor).Render(strings.Repeat("─", w))
	if s.tree == nil {
		body := mutedStyle(s.th).Render("  no git-town stack\n  in this repo")
		return lipgloss.JoinVertical(lipgloss.Left, title, rule, body)
	}

	var b strings.Builder
	for i, n := range s.tree.Order {
		indent := strings.Repeat("  ", n.Depth)
		nameStyle := lipgloss.NewStyle().Foreground(s.th.Text)
		switch {
		case n.Name == s.status.Branch:
			nameStyle = lipgloss.NewStyle().Foreground(s.th.Primary).Bold(true) // you are here
		case n.IsTrunk:
			nameStyle = lipgloss.NewStyle().Foreground(s.th.Muted)
		case n.Drifted:
			nameStyle = lipgloss.NewStyle().Foreground(s.th.Warning)
		}
		// Marker slot: checkout cursor (when the tree is focused) wins the slot;
		// otherwise the current-branch bar.
		marker := "  "
		switch {
		case focused && i == s.treeCursor:
			marker = lipgloss.NewStyle().Foreground(s.th.Focus).Bold(true).Render("▸ ")
		case n.Name == s.status.Branch:
			marker = lipgloss.NewStyle().Foreground(s.th.Primary).Render("▌ ")
		}
		suffix := ""
		if n.Drifted {
			suffix = " " + lipgloss.NewStyle().Foreground(s.th.Warning).Render("⚠")
		}
		used := 2 + len(indent) + lipgloss.Width(suffix)
		b.WriteString(marker + indent + nameStyle.Render(truncate(n.Name, w-used-1)) + suffix + "\n")
	}
	return lipgloss.JoinVertical(lipgloss.Left, title, rule, b.String())
}

// renderRight switches between the action list, the name prompt, the explained
// confirmation, and the run output, depending on phase.
func (s stackModel) renderRight(w int) string {
	switch s.phase {
	case stackNaming:
		return s.renderNaming(w)
	case stackConfirming:
		return s.renderConfirm(w)
	case stackRunning, stackDone:
		return s.renderOutput(w)
	default:
		if s.needsInit() {
			return s.renderInitCTA(w)
		}
		return s.renderActions(w)
	}
}

// renderInitCTA is shown when the repo has no git-town config: a single, guided
// call-to-action to initialize it, so stack mode isn't a dead end.
func (s stackModel) renderInitCTA(w int) string {
	title := lipgloss.NewStyle().Foreground(s.th.Focus).Bold(true).Render("Set up git-town")
	rule := lipgloss.NewStyle().Foreground(s.th.Focus).Render(strings.Repeat("─", w))

	intro := mutedStyle(s.th).Render(wrapPlain(
		"This repo isn't configured for stacks yet. Cairn can initialize git-town "+
			"for you — it only writes local .git/config, nothing is committed or pushed.", proseWidth(w), ""))

	trunk := s.trunk
	if trunk == "" {
		trunk = "(none detected — you'll name it)"
	}
	bullets := []string{
		"mark the trunk branch — detected: " + trunk,
		"standardize on rebase syncing (right for stacks)",
	}
	var bl strings.Builder
	for _, b := range bullets {
		bl.WriteString(lipgloss.NewStyle().Foreground(s.th.Text).Render("• "+b) + "\n")
	}

	cta := lipgloss.NewStyle().Foreground(s.th.Success).Bold(true).Render("[enter] set up git-town")
	return lipgloss.JoinVertical(lipgloss.Left, title, rule, "", intro, "", bl.String(), cta)
}

func (s stackModel) renderActions(w int) string {
	// Dim the heading when focus is on the tree, so the active pane is obvious.
	titleColor, ruleColor := s.th.Focus, s.th.Focus
	if s.focus == focusTree {
		titleColor, ruleColor = s.th.Muted, s.th.Overlay
	}
	title := lipgloss.NewStyle().Foreground(titleColor).Bold(true).Render("Stack actions")
	rule := lipgloss.NewStyle().Foreground(ruleColor).Render(strings.Repeat("─", w))

	var rows []string
	for i, c := range s.commands {
		on := s.actionEnabled(c)
		key := lipgloss.NewStyle().Foreground(s.th.Accent2).Render(c.Key)
		label := c.Title
		labelStyle := lipgloss.NewStyle().Foreground(s.th.Text).Bold(true)
		shortStyle := lipgloss.NewStyle().Foreground(s.th.Muted)
		if !on {
			key = lipgloss.NewStyle().Foreground(s.th.Overlay).Render(c.Key)
			labelStyle = lipgloss.NewStyle().Foreground(s.th.Overlay)
			shortStyle = lipgloss.NewStyle().Foreground(s.th.Overlay)
		}
		line := fmt.Sprintf("%s  %s — %s", key, labelStyle.Render(label), shortStyle.Render(c.Short))
		if i == s.cursor && s.focus == focusActions {
			line = lipgloss.NewStyle().Foreground(s.th.Primary).Render("▌ ") + line
		} else {
			line = "  " + line
		}
		rows = append(rows, line)
	}

	help := ""
	if !s.enabled() {
		help = "\n" + mutedStyle(s.th).Render("  Stack actions need a git-town repo on a branch.\n  See the statusline above for what's missing.")
	}
	return lipgloss.JoinVertical(lipgloss.Left, title, rule, strings.Join(rows, "\n"), help)
}

func (s stackModel) renderNaming(w int) string {
	prompt := " — name the new branch"
	if s.pending.Verb == "init" {
		prompt = " — which branch is the trunk?"
	}
	title := lipgloss.NewStyle().Foreground(s.th.Focus).Bold(true).Render(s.pending.Title + prompt)
	rule := lipgloss.NewStyle().Foreground(s.th.Focus).Render(strings.Repeat("─", w))
	explain := mutedStyle(s.th).Render(wrapPlain(s.pending.Long, proseWidth(w), ""))
	field := lipgloss.NewStyle().Foreground(s.th.Text).Render(s.name.View())
	hint := mutedStyle(s.th).Render("enter confirm · esc cancel")
	return lipgloss.JoinVertical(lipgloss.Left, title, rule, "", explain, "", field, "", hint)
}

func (s stackModel) renderConfirm(w int) string {
	c := s.pending
	name := strings.TrimSpace(s.name.Value())
	headline := c.Title
	// init's "name" is the trunk, already shown in the effect + runs lines, so
	// keep its headline clean ("set up git-town — confirm").
	if name != "" && c.Verb != "init" {
		headline += " " + name
	}
	title := lipgloss.NewStyle().Foreground(s.th.Primary).Bold(true).Render(headline + " — confirm")
	rule := lipgloss.NewStyle().Foreground(s.th.Focus).Render(strings.Repeat("─", w))

	textW := proseWidth(w)
	what := lipgloss.NewStyle().Foreground(s.th.Text).Render(wrapPlain(c.Long, textW, ""))

	// Concrete effect on the local branches — phrased per verb so it's accurate
	// (insert re-parents; it doesn't move commits yet, unlike amend/restack/sync).
	cur := val(s.status.Branch)
	desc := s.descendants(s.status.Branch)
	var effect string
	switch c.Verb {
	case "new":
		effect = fmt.Sprintf("Creates %s as a child of %s; also syncs the affected branches if your tree is clean.", val(name), cur)
	case "insert":
		if len(desc) > 0 {
			effect = fmt.Sprintf("Wedges empty %s beneath %s; %s and %s are re-parented onto it — no commits move yet.",
				val(name), cur, cur, humanList(desc))
		} else {
			effect = fmt.Sprintf("Wedges empty %s beneath %s; %s is re-parented onto it — no commits move yet.", val(name), cur, cur)
		}
	case "amend":
		if len(desc) > 0 {
			effect = fmt.Sprintf("Rewrites %s's latest commit, then rebases %s onto it.", cur, humanList(desc))
		} else {
			effect = fmt.Sprintf("Rewrites %s's latest commit (no branches above to restack).", cur)
		}
	case "restack":
		if len(s.affected) > 0 {
			effect = "Rebases the whole stack (" + humanList(s.affected) + ") locally onto the latest trunk — each branch onto its parent. No push."
		} else {
			effect = fmt.Sprintf("Rebases %s onto the latest trunk locally. No push.", cur)
		}
	case "sync":
		if len(s.affected) > 0 {
			effect = "Rebases the stack (" + humanList(s.affected) + ") onto the updated trunk — each branch onto its parent — then pushes."
		} else {
			effect = "Pulls the trunk and pushes; no other stack branches to move."
		}
	case "ship":
		if len(desc) > 0 {
			effect = fmt.Sprintf("Merges %s's PR into the trunk on GitHub and deletes it, then re-parents %s onto the trunk.", cur, humanList(desc))
		} else {
			effect = fmt.Sprintf("Merges %s's PR into the trunk on GitHub and deletes it.", cur)
		}
	case "init":
		effect = fmt.Sprintf("Marks %s as the trunk and sets rebase syncing — writes local .git/config only, nothing committed or pushed.", val(name))
	default:
		effect = "Affects " + cur + "."
	}
	effectLine := lipgloss.NewStyle().Foreground(s.th.Warning).Render(wrapPlain("• "+effect, textW, "  "))

	cmdLine := lipgloss.NewStyle().Foreground(s.th.Muted).Render("runs:  " + commandLine(c, name))
	confirm := lipgloss.NewStyle().Foreground(s.th.Success).Render("[enter] do it") +
		mutedStyle(s.th).Render("    [esc] cancel")

	return lipgloss.JoinVertical(lipgloss.Left, title, rule, "",
		lipgloss.NewStyle().Foreground(s.th.Muted).Render("What this does:"),
		what, "", effectLine, "", cmdLine, "", confirm)
}

func (s stackModel) renderOutput(w int) string {
	headline := s.pending.Title
	if s.opName != "" {
		headline += " " + s.opName
	}
	var status string
	switch {
	case s.phase == stackRunning:
		status = lipgloss.NewStyle().Foreground(s.th.Focus).Render("running…")
	case s.runErr != nil:
		status = lipgloss.NewStyle().Foreground(s.th.Danger).Render("failed: " + s.runErr.Error())
	default:
		status = lipgloss.NewStyle().Foreground(s.th.Success).Render("done ✓")
	}
	title := lipgloss.NewStyle().Foreground(s.th.Primary).Bold(true).Render(headline+"  ") + status
	rule := lipgloss.NewStyle().Foreground(s.th.Focus).Render(strings.Repeat("─", w))

	out := s.output
	if out == "" && s.phase == stackRunning {
		out = "git-town " + s.pending.Verb + "…"
	}
	body := s.renderRunLog(out, w-1)

	foot := ""
	if s.phase == stackDone {
		foot = "\n" + mutedStyle(s.th).Render("any key to return")
	}
	return lipgloss.JoinVertical(lipgloss.Left, title, rule, "", body, foot)
}

// renderRunLog styles delegated git output for scannability: the git-town command
// echoes (lines like "[feat-mid] git …") get an accent so the eye can find each
// step, while the results below them stay plain text. Carriage returns are
// sanitized and long lines hard-wrapped to the pane first.
func (s stackModel) renderRunLog(out string, w int) string {
	cmd := lipgloss.NewStyle().Foreground(s.th.Focus).Bold(true)
	res := lipgloss.NewStyle().Foreground(s.th.Text)
	// git-town colorizes its own output (e.g. bold command echoes wrapped in
	// \x1b[1m…\x1b[0m). Strip that first so Cairn fully controls styling and the
	// "[branch] …" command detection sees plain text, not an escape prefix.
	var b strings.Builder
	for i, ln := range strings.Split(sanitizeCR(ansi.Strip(out)), "\n") {
		if i > 0 {
			b.WriteByte('\n')
		}
		if isCommandEcho(ln) {
			// Structural marker (color-independent) + accent so command rows stand
			// out even where a terminal flattens truecolor.
			b.WriteString(cmd.Render("❯ " + ansi.Wrap(ln, w-2, "")))
		} else {
			b.WriteString(res.Render(ansi.Wrap(ln, w, "")))
		}
	}
	return b.String()
}

// isCommandEcho reports whether a run-log line is one of git-town's command
// echoes, which it prints as "[<branch>] <command>".
func isCommandEcho(line string) bool {
	return strings.HasPrefix(line, "[") && strings.Contains(line, "] ")
}

func (s stackModel) viewFooter(spinnerFrame string) string {
	var help string
	switch s.phase {
	case stackBrowsing:
		switch {
		case s.status.Conflicts > 0:
			help = fmt.Sprintf("%d conflict(s) — R resolve · r refresh · esc dashboard", s.status.Conflicts)
		case s.needsInit():
			help = "enter set up git-town · r refresh · esc dashboard"
		case s.focus == focusTree:
			help = "↑/↓ j/k branch · enter checkout · tab ←/→ actions · r refresh · esc dashboard"
		default:
			help = "↑/↓ j/k move · enter choose · tab ←/→ tree (checkout) · r refresh · esc dashboard"
		}
	case stackNaming:
		help = "type a name · enter confirm · esc cancel"
	case stackConfirming:
		help = "enter run · esc cancel"
	case stackRunning:
		help = spinnerFrame + " running git-town — please wait"
	case stackDone:
		help = "any key to return to actions"
	}
	return lipgloss.NewStyle().Width(s.width).Foreground(s.th.Muted).Padding(0, 1).Render(help)
}

// --- small text helpers ---

// proseMaxCol caps explanatory text at a comfortable reading width even when the
// pane is much wider, so confirmations don't stretch into one giant line.
const proseMaxCol = 88

// proseWidth returns the wrap width for prose in a pane of width w: the pane
// minus padding, but never wider than proseMaxCol.
func proseWidth(w int) int {
	tw := w - 2
	if tw > proseMaxCol {
		tw = proseMaxCol
	}
	if tw < 8 {
		tw = 8
	}
	return tw
}

// sanitizeCR collapses carriage returns the way a terminal would: git (rebase,
// fetch) overwrites progress with '\r', and prints "Successfully rebased…" with a
// leading '\r' to clear the progress line. Left raw, that '\r' yanks the cursor
// to column 0 and the text escapes the pane's left padding. Keep only what's
// after the last '\r' on each line.
func sanitizeCR(s string) string {
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		if idx := strings.LastIndex(ln, "\r"); idx >= 0 {
			ln = ln[idx+1:]
		}
		lines[i] = ln
	}
	return strings.Join(lines, "\n")
}

// wrapBlock hard-wraps every line of s to width w (word-aware, breaking tokens
// longer than w) so command output can't overflow its pane and get reflowed to
// column 0 by the terminal. Carriage returns are sanitized first.
func wrapBlock(s string, w int) string {
	if w < 8 {
		w = 8
	}
	lines := strings.Split(sanitizeCR(s), "\n")
	for i, ln := range lines {
		lines[i] = ansi.Wrap(ln, w, "")
	}
	return strings.Join(lines, "\n")
}

// commandLine renders the concrete shell invocation for the confirmation, with
// the real branch name substituted in.
func commandLine(c townie.Command, name string) string {
	if c.Verb == "amend" {
		return "git commit --amend --no-edit  →  git town sync --stack --no-push"
	}
	if c.Verb == "init" {
		return fmt.Sprintf("git config git-town.main-branch %s  →  git config git-town.sync-feature-strategy rebase", val(name))
	}
	line := c.Hint()
	if name != "" {
		line = strings.ReplaceAll(line, "<name>", name)
	}
	return line
}

func val(s string) string {
	if s == "" {
		return "(?)"
	}
	return s
}

// humanList renders ["a","b","c"] as "a, b and c".
func humanList(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	case 2:
		return xs[0] + " and " + xs[1]
	default:
		return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
	}
}

// descendants returns the branches stacked above branch, in tree order.
func (s stackModel) descendants(branch string) []string {
	if s.tree == nil {
		return nil
	}
	start := s.tree.NodeByName(branch)
	if start == nil {
		return nil
	}
	var out []string
	var dfs func(n *stack.Node)
	dfs = func(n *stack.Node) {
		for _, c := range n.Children {
			out = append(out, c.Name)
			dfs(c)
		}
	}
	dfs(start)
	return out
}
