package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
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
	stackComposing                    // titling + writing a proposed PR (with live preview)
	stackConfirming                   // the explained confirmation is shown
	stackRunning                      // the git-town op is in flight
	stackDone                         // op finished; output + result shown
)

// composeFocus selects which field of the PR composer receives keys.
type composeFocus int

const (
	composeTitle composeFocus = iota
	composeBody
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

	// PR composer state (the propose flow): a title field + a Markdown body with a
	// live preview, plus the auto-detected base and a draft toggle.
	titleInput   textinput.Model
	body         textarea.Model
	composeFocus composeFocus
	proposeBase  string         // base branch the proposed PR targets (from lineage)
	proposeDraft bool           // open as a draft PR
	prNums       map[string]int // branch -> open PR number, for the tree's #N flags

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

	title := textinput.New()
	title.Placeholder = "PR title"
	title.Prompt = "› "
	title.CharLimit = 160

	body := textarea.New()
	body.Placeholder = "Describe the change (GitHub-flavored Markdown)…"
	body.CharLimit = 0 // no limit — PR bodies can be long
	body.ShowLineNumbers = false

	// Theme the composer here, once, on the real models — textarea caches its
	// active style via an internal pointer (&FocusedStyle), which a value copy
	// would leave dangling, so styling a render-time copy has NO effect. Setting it
	// at construction makes every copy carry identical themed styles.
	styleComposer(&body, th)
	styleTitleInput(&title, th)

	return stackModel{
		th:         th,
		repo:       repo,
		commands:   townie.Catalog(),
		name:       ti,
		titleInput: title,
		body:       body,
		ops:        townie.New(""), // cwd
	}
}

// newStackModel builds the screen for the cwd repo, reading the local tree +
// working-tree status up front.
func newStackModel(th theme.Theme, repo string) stackModel {
	s := newStackModelBare(th, repo)
	s.reload()
	return s
}

// clearOp drops the output/error of the last run op and returns to the browsing
// phase. Used when leaving the conflict resolver so a since-resolved sync failure
// doesn't linger on the stack screen.
func (s *stackModel) clearOp() {
	s.phase = stackBrowsing
	s.output = ""
	s.runErr = nil
	s.opName = ""
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

// currentUntracked reports whether git-town is configured here but the current
// branch isn't part of the stack yet — on a real branch git-town has no recorded
// parent for (and which isn't the trunk). This is the state where Cairn offers to
// add the branch to the stack ("track" it).
func (s stackModel) currentUntracked() bool {
	return s.enabled() && s.tree.NodeByName(s.status.Branch) == nil
}

// trackParent is the parent a freshly-tracked branch is filed under: the trunk
// (the common case for a feature branch cut from main). The user can re-parent
// afterwards if it actually belongs higher in a stack.
func (s stackModel) trackParent() string {
	if s.tree != nil && s.tree.Root != nil {
		return s.tree.Root.Name
	}
	return ""
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
	case "propose":
		// Open a PR for the branch under the tree cursor: it must be a tracked,
		// non-trunk node that doesn't already have an open PR.
		t := s.proposeTarget()
		if t == "" || s.tree == nil {
			return false
		}
		n := s.tree.NodeByName(t)
		return n != nil && !n.IsTrunk && s.prNums[t] == 0
	case "amend":
		// Folds STAGED changes (nudge `git add` first) into a tracked branch.
		return s.onTrackedBranch() && s.status.Staged > 0
	case "restack", "sync":
		// Maintain an existing stack — only meaningful on a branch git-town
		// tracks; otherwise it'd act on a stack it doesn't know.
		return s.onTrackedBranch()
	case "ship":
		// Only the bottom of the stack can be merged: a stacked PR targets the
		// branch below it, so lower branches must land first. And only on a clean
		// tree — merge squash-merges the PR (irreversible) then rebases the whole
		// stack via sync, which a dirty working tree would derail. (Untracked files
		// are fine; they don't interfere with the rebase.)
		return s.isBottomBranch() && !s.status.Dirty()
	default:
		// new / insert can start or extend a stack from any branch.
		return true
	}
}

// capturing reports whether a text field should receive raw keys (so global
// shortcuts like '?' are typed literally, not intercepted).
func (s stackModel) capturing() bool {
	return s.phase == stackNaming || s.phase == stackComposing
}

func (s stackModel) Update(msg tea.Msg) (stackModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		s.width, s.height = msg.Width, msg.Height
		s.sizeCompose()
		return s, nil

	case stackPRNumsMsg:
		if msg.nums != nil {
			s.prNums = msg.nums
		}
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
			// Refresh the tree's PR flags — a propose just added one; a ship/sync may
			// have removed one.
			return s, fetchStackPRNums(s.repo)
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
		case stackComposing:
			return s.updateComposing(msg)
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
		// Also refresh the PR flags so a PR opened elsewhere shows its #N.
		s.reload()
		return s, fetchStackPRNums(s.repo)
	case "t":
		// Add the current (untracked) branch to the stack under the trunk. Inert
		// otherwise (only meaningful when the branch has no recorded parent).
		if s.currentUntracked() {
			return s.runTrack()
		}
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
	if c.Verb == "propose" {
		return s.startPropose()
	}
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

// --- propose (open a PR) flow ---

// proposeTarget is the branch the propose action acts on: the one under the tree
// cursor (so you can propose any branch in the stack, not just HEAD), falling
// back to the current branch.
func (s stackModel) proposeTarget() string {
	if s.tree != nil && s.treeCursor >= 0 && s.treeCursor < len(s.tree.Order) {
		return s.tree.Order[s.treeCursor].Name
	}
	return s.status.Branch
}

// proposeBaseFor returns the base branch a PR for branch should target: its
// recorded parent in the lineage — the branch below it in the stack, or the trunk
// for the bottom branch. "" when the branch isn't a known node.
func (s stackModel) proposeBaseFor(branch string) string {
	if n := s.tree.NodeByName(branch); n != nil {
		return n.Parent
	}
	return ""
}

// startPropose opens the PR composer for the targeted branch: it pre-fills the
// title with the branch's latest commit subject, blanks the body, and records the
// auto-detected base so the confirmation and the create call agree.
func (s stackModel) startPropose() (stackModel, tea.Cmd) {
	target := s.proposeTarget()
	s.opName = target
	s.proposeBase = s.proposeBaseFor(target)
	s.proposeDraft = false

	subject := stack.LatestCommitSubject("", target)
	if subject == "" {
		subject = target
	}
	s.titleInput.SetValue(subject)
	s.titleInput.CursorEnd()
	s.body.SetValue("")
	s.composeFocus = composeTitle
	s.titleInput.Focus()
	s.body.Blur()
	s.sizeCompose()
	s.phase = stackComposing
	return s, textinput.Blink
}

// sizeCompose sizes the composer's fields to the current window, so the title
// input and body textarea fit the editor pane and wrap correctly.
func (s *stackModel) sizeCompose() {
	if s.width == 0 {
		return
	}
	editorW, _ := composeWidths(bodyWidth(s.width))
	bodyH := s.height - headerH - 1 /*statusline*/ - 1 /*footer*/
	if bodyH < 6 {
		bodyH = 6
	}
	s.titleInput.Width = editorW - 4
	s.body.SetWidth(editorW)
	s.body.SetHeight(bodyH - 6) // room for the header, rule, labels, title field, hint
}

func (s stackModel) updateComposing(msg tea.KeyMsg) (stackModel, tea.Cmd) {
	switch msg.String() {
	case "esc":
		s.titleInput.Blur()
		s.body.Blur()
		s.phase = stackBrowsing
		return s, nil
	case "tab", "shift+tab":
		if s.composeFocus == composeTitle {
			s.composeFocus = composeBody
			s.titleInput.Blur()
			s.body.Focus()
			return s, textarea.Blink
		}
		s.composeFocus = composeTitle
		s.body.Blur()
		s.titleInput.Focus()
		return s, textinput.Blink
	case "ctrl+s":
		// Continue to the confirmation. A title is required; an empty body is fine.
		if strings.TrimSpace(s.titleInput.Value()) == "" {
			s.composeFocus = composeTitle
			s.body.Blur()
			s.titleInput.Focus()
			return s, textinput.Blink
		}
		s.titleInput.Blur()
		s.body.Blur()
		s.affected = nil
		s.phase = stackConfirming
		return s, nil
	}
	// enter in the title jumps to the body; otherwise route to the focused field.
	if s.composeFocus == composeTitle {
		if msg.String() == "enter" {
			s.composeFocus = composeBody
			s.titleInput.Blur()
			s.body.Focus()
			return s, textarea.Blink
		}
		var cmd tea.Cmd
		s.titleInput, cmd = s.titleInput.Update(msg)
		return s, cmd
	}
	var cmd tea.Cmd
	s.body, cmd = s.body.Update(msg)
	return s, cmd
}

// runPropose pushes the target branch and opens its PR, streaming progress through
// the same machinery as the other ops.
func (s stackModel) runPropose() (stackModel, tea.Cmd) {
	s.phase = stackRunning
	s.output = ""
	owner, repo, _ := gh.SplitRepo(s.repo)
	head := s.opName
	base := s.proposeBase
	title := strings.TrimSpace(s.titleInput.Value())
	body := s.body.Value()
	draft := s.proposeDraft
	ops := s.ops
	return s, func() tea.Msg {
		return readStream(proposeStream(owner, repo, head, base, title, body, draft, ops))
	}
}

// proposeStream opens a PR for head: guard against an existing PR, push the branch
// to origin (so GitHub can see it), then create the PR with the lineage-derived
// base. base is the branch head is stacked on (the branch below it, or the trunk).
func proposeStream(owner, repo, head, base, title, body string, draft bool, ops townie.Ops) <-chan townie.StreamEvent {
	ch := make(chan townie.StreamEvent, 64)
	go func() {
		defer close(ch)
		if owner == "" || repo == "" {
			ch <- townie.StreamEvent{Done: true, Err: fmt.Errorf("can't propose: unknown repo")}
			return
		}
		if base == "" {
			ch <- townie.StreamEvent{Done: true, Err: fmt.Errorf("can't tell what %s is stacked on — no recorded parent", head)}
			return
		}
		ch <- townie.StreamEvent{Line: "Checking for an existing PR for " + head + "…"}
		if num, err := gh.FindPROpenForBranch(owner, repo, head); err != nil {
			ch <- townie.StreamEvent{Done: true, Err: err}
			return
		} else if num != 0 {
			ch <- townie.StreamEvent{Done: true, Err: fmt.Errorf("PR #%d already exists for %s", num, head)}
			return
		}
		ch <- townie.StreamEvent{Line: "Pushing " + head + " to origin…"}
		for ev := range ops.Stream("push", head) {
			if ev.Done {
				if ev.Err != nil {
					ch <- townie.StreamEvent{Done: true, Err: ev.Err}
					return
				}
				break
			}
			ch <- ev
		}
		ch <- townie.StreamEvent{Line: fmt.Sprintf("Opening a pull request: %s → %s…", head, base)}
		num, url, err := gh.CreatePR(owner, repo, head, base, title, body, draft)
		if err != nil {
			ch <- townie.StreamEvent{Done: true, Err: err}
			return
		}
		kind := "PR"
		if draft {
			kind = "draft PR"
		}
		ch <- townie.StreamEvent{Line: fmt.Sprintf("✓ Opened %s #%d  %s", kind, num, url)}
		ch <- townie.StreamEvent{Done: true}
	}()
	return ch
}

// runTrack records the current branch's git-town parent (the trunk), adding it to
// the stack tree, streaming progress through the same machinery as the other ops.
func (s stackModel) runTrack() (stackModel, tea.Cmd) {
	s.phase = stackRunning
	s.pending = townie.Command{Verb: "track", Title: "track"}
	s.opName = s.status.Branch
	s.output = ""
	branch := s.status.Branch
	parent := s.trackParent()
	ops := s.ops
	return s, func() tea.Msg {
		return readStream(trackStream(branch, parent, ops))
	}
}

// trackStream records branch's parent in local git-town config and brackets the
// (silent) git config call with a friendly before/after line.
func trackStream(branch, parent string, ops townie.Ops) <-chan townie.StreamEvent {
	ch := make(chan townie.StreamEvent, 8)
	go func() {
		defer close(ch)
		if parent == "" {
			ch <- townie.StreamEvent{Done: true, Err: fmt.Errorf("can't track %s: no trunk to file it under", branch)}
			return
		}
		ch <- townie.StreamEvent{Line: fmt.Sprintf("Recording %s's parent as %s…", branch, parent)}
		for ev := range ops.SetParent(branch, parent) {
			if ev.Done {
				if ev.Err != nil {
					ch <- townie.StreamEvent{Done: true, Err: ev.Err}
					return
				}
				break
			}
			ch <- ev
		}
		ch <- townie.StreamEvent{Line: fmt.Sprintf("✓ %s is now part of the stack (parent: %s).", branch, parent)}
		ch <- townie.StreamEvent{Done: true}
	}()
	return ch
}

// stackPRNumsMsg carries the branch→open-PR-number map for the tree's #N flags.
type stackPRNumsMsg struct{ nums map[string]int }

// fetchStackPRNums loads the repo's open PRs (branch → number) so the local tree
// can flag which branches already have a PR. Best-effort: a failure leaves the
// flags as they were.
func fetchStackPRNums(repo string) tea.Cmd {
	return func() tea.Msg {
		owner, name, ok := gh.SplitRepo(repo)
		if !ok {
			return stackPRNumsMsg{}
		}
		nums, _ := gh.OpenPRNumbersByBranch(owner, name)
		return stackPRNumsMsg{nums: nums}
	}
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
	// propose has its own confirm: esc returns to the editor (not all the way out),
	// d toggles draft, enter opens the PR.
	if s.pending.Verb == "propose" {
		switch msg.String() {
		case "esc":
			s.phase = stackComposing
			s.composeFocus = composeTitle
			s.titleInput.Focus()
			s.body.Blur()
			return s, textinput.Blink
		case "d":
			s.proposeDraft = !s.proposeDraft
			return s, nil
		case "enter":
			return s.runPropose()
		}
		return s, nil
	}
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
// header is the app's brand masthead (rendered by the root model, which owns the
// login/rate it shows) so stack mode keeps the same top bar as the dashboard.
func (s stackModel) View(spinnerFrame, header string) string {
	statusline := renderStatusline(s.th, s.repo, s.status, s.hasGitTown(), s.width)

	bodyH := s.height - headerH /*brand*/ - 1 /*statusline*/ - 1 /*footer*/
	if bodyH < 1 {
		bodyH = 1
	}

	var body string
	if s.phase == stackComposing {
		// The PR composer takes the whole body: editor on the left, live Markdown
		// preview on the right — the tree isn't needed while you write.
		body = indentBody(s.renderCompose(bodyWidth(s.width), bodyH))
	} else {
		treeW := s.treeWidth()
		rightW := bodyWidth(s.width) - treeW - 1
		if rightW < 20 {
			rightW = 20
		}
		left := lipgloss.NewStyle().Width(treeW).Height(bodyH).Render(s.renderLocalTree(treeW))
		right := lipgloss.NewStyle().Width(rightW).Height(bodyH).Render(s.renderRight(rightW))
		// Header/statusline/footer are full-width bars (flush); only the panes indent.
		body = indentBody(lipgloss.JoinHorizontal(lipgloss.Top, left, stackVBar(s.th, bodyH), right))
	}

	return lipgloss.JoinVertical(lipgloss.Left, header, statusline, body, s.viewFooter(spinnerFrame))
}

// treeWidth sizes the local-stack pane to fit its longest row (depth indent +
// branch name + PR #flag + drift mark), so long branch names aren't truncated.
// It's clamped between the sidebar floor (stackPaneW) and ~half the body, so the
// action pane keeps room on narrow terminals.
func (s stackModel) treeWidth() int {
	w := stackPaneW
	if s.tree != nil {
		for _, n := range s.tree.Order {
			need := 2 + 2*n.Depth + lipgloss.Width(n.Name) + 1 // marker + indent + name + slack
			if num := s.prNums[n.Name]; num > 0 {
				need += len(fmt.Sprintf(" #%d", num))
			}
			if n.Drifted {
				need += 2
			}
			if need > w {
				w = need
			}
		}
	}
	maxW := bodyWidth(s.width) / 2 // keep at least half the body for the action pane
	if maxW < stackPaneW {
		maxW = stackPaneW // tiny terminals: hold the floor (View clamps the right pane)
	}
	if w > maxW {
		w = maxW
	}
	return w
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
			marker = lipgloss.NewStyle().Foreground(s.th.Focus).Bold(true).Render(focusGlyph + " ")
		case n.Name == s.status.Branch:
			marker = lipgloss.NewStyle().Foreground(s.th.Primary).Render("▌ ")
		}
		suffix := ""
		// Flag branches that already have an open PR with its #number, so you can
		// see at a glance what's proposed and cross-reference it on GitHub.
		if num := s.prNums[n.Name]; num > 0 {
			suffix += " " + lipgloss.NewStyle().Foreground(s.th.Accent2).Render(fmt.Sprintf("#%d", num))
		}
		if n.Drifted {
			suffix += " " + lipgloss.NewStyle().Foreground(s.th.Warning).Render("⚠")
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

	// Peach call-to-action when you're on a branch git-town doesn't track yet — it
	// won't appear in the stack tree and the maintenance verbs stay dim until you
	// add it. One key (t) files it under the trunk.
	banner := ""
	if s.currentUntracked() {
		msg := fmt.Sprintf("⚠ %s isn't in the stack yet — press t to add it under %s.",
			s.status.Branch, val(s.trackParent()))
		banner = lipgloss.NewStyle().Foreground(s.th.Warning).Render(wrapPlain(msg, proseWidth(w), "  ")) + "\n\n"
	}

	var rows []string
	for i, c := range s.commands {
		on := s.actionEnabled(c)
		key := lipgloss.NewStyle().Foreground(s.th.Accent2).Render(c.Key)
		label := c.Title
		short := c.Short
		// Name the branch propose will act on (the tree cursor), so "which branch"
		// is never ambiguous from the action list.
		if c.Verb == "propose" {
			if t := s.proposeTarget(); t != "" {
				short = "open a PR for " + t
			}
		}
		labelStyle := lipgloss.NewStyle().Foreground(s.th.Text).Bold(true)
		shortStyle := lipgloss.NewStyle().Foreground(s.th.Muted)
		if !on {
			// Disabled rows: use Muted (inactive TEXT), not Overlay (divider color),
			// which was too faint to read in both light and dark.
			key = lipgloss.NewStyle().Foreground(s.th.Muted).Render(c.Key)
			labelStyle = lipgloss.NewStyle().Foreground(s.th.Muted)
			shortStyle = lipgloss.NewStyle().Foreground(s.th.Muted)
		}
		line := fmt.Sprintf("%s  %s — %s", key, labelStyle.Render(label), shortStyle.Render(short))
		if i == s.cursor && s.focus == focusActions {
			// Same full-width Surface highlight as the focused PR row: styledBar
			// reasserts the background after each fragment's reset so the bar spans
			// the whole line (both themes), with the cursor glyph in Primary.
			cursor := lipgloss.NewStyle().Foreground(s.th.Primary).Render(focusGlyph + " ")
			line = styledBar(s.th.Text, s.th.Surface, w, cursor+line)
		} else {
			line = "  " + line
		}
		rows = append(rows, line)
	}

	help := ""
	if !s.enabled() {
		help = "\n" + mutedStyle(s.th).Render("  Stack actions need a git-town repo on a branch.\n  See the statusline above for what's missing.")
	}
	return lipgloss.JoinVertical(lipgloss.Left, title, rule, banner+strings.Join(rows, "\n"), help)
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
	if s.pending.Verb == "propose" {
		return s.renderProposeConfirm(w)
	}
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

// composeWidths splits the body width into the editor pane and the preview pane.
func composeWidths(w int) (editorW, previewW int) {
	editorW = w * 45 / 100
	if editorW < 24 {
		editorW = 24
	}
	previewW = w - editorW - 1
	if previewW < 16 {
		previewW = 16
	}
	return editorW, previewW
}

// styleComposer paints a textarea from Cairn's theme instead of bubbles' default
// adaptive styles — whose focused cursor-line background is chosen from the
// TERMINAL's real background, so a light Cairn theme on a dark terminal renders an
// unreadable black band. Must be applied to the persistent model (not a copy):
// textarea caches its active style via &FocusedStyle, which a value copy leaves
// pointing at the original.
func styleComposer(ta *textarea.Model, th theme.Theme) {
	text := lipgloss.NewStyle().Foreground(th.Text)
	muted := lipgloss.NewStyle().Foreground(th.Muted)
	eob := lipgloss.NewStyle().Foreground(th.Overlay)

	ta.FocusedStyle.Base = text
	ta.FocusedStyle.Text = text
	ta.FocusedStyle.CursorLine = text // no current-line highlight band — plain text
	ta.FocusedStyle.Placeholder = muted
	ta.FocusedStyle.Prompt = muted
	ta.FocusedStyle.EndOfBuffer = eob

	ta.BlurredStyle.Base = text
	ta.BlurredStyle.Text = text
	ta.BlurredStyle.CursorLine = text
	ta.BlurredStyle.Placeholder = muted
	ta.BlurredStyle.Prompt = muted
	ta.BlurredStyle.EndOfBuffer = eob

	// With no line highlight, the cursor must carry visibility itself. The cursor
	// view renders Style with Reverse(true) (it SWAPS fg/bg), so set the pre-reverse
	// colors swapped — fg Primary, bg Base — to display as a solid Primary block
	// with a Base-colored glyph, obvious in both themes.
	ta.Cursor.Style = lipgloss.NewStyle().Foreground(th.Primary).Background(th.Base)
}

// restyleComposer re-themes the composer in place after a live theme toggle. It
// re-points the textarea's cached active-style pointer (via Focus/Blur) to the
// freshly-themed style so the change actually takes effect on the next render.
func (s *stackModel) restyleComposer() {
	styleComposer(&s.body, s.th)
	styleTitleInput(&s.titleInput, s.th)
	if s.composeFocus == composeBody {
		s.body.Focus()
	} else {
		s.body.Blur()
	}
}

// styleTitleInput themes the single-line title field to match (cursor, text,
// placeholder), for the same reason as styleComposer.
func styleTitleInput(ti *textinput.Model, th theme.Theme) {
	ti.TextStyle = lipgloss.NewStyle().Foreground(th.Text)
	ti.PlaceholderStyle = lipgloss.NewStyle().Foreground(th.Muted)
	ti.PromptStyle = lipgloss.NewStyle().Foreground(th.Muted)
	ti.Cursor.Style = lipgloss.NewStyle().Foreground(th.Primary).Background(th.Base)
}

// fieldLabel renders a composer field's label, brightened when that field has focus.
func fieldLabel(th theme.Theme, label string, focused bool) string {
	st := lipgloss.NewStyle().Foreground(th.Muted)
	if focused {
		st = lipgloss.NewStyle().Foreground(th.Focus).Bold(true)
	}
	return st.Render(label)
}

// renderCompose draws the PR composer: a title field + a Markdown body on the
// left, and a live rendered preview of what you're typing on the right.
func (s stackModel) renderCompose(w, h int) string {
	editorW, previewW := composeWidths(w)

	headline := lipgloss.NewStyle().Foreground(s.th.Primary).Bold(true).Render("propose " + s.opName)
	sub := mutedStyle(s.th).Render("  base ← " + val(s.proposeBase))
	rule := lipgloss.NewStyle().Foreground(s.th.Focus).Render(strings.Repeat("─", editorW))

	editor := lipgloss.JoinVertical(lipgloss.Left,
		headline+sub, rule, "",
		fieldLabel(s.th, "Title", s.composeFocus == composeTitle),
		s.titleInput.View(), "",
		fieldLabel(s.th, "Description — Markdown", s.composeFocus == composeBody),
		s.body.View(),
	)
	editorPane := lipgloss.NewStyle().Width(editorW).Height(h).Render(editor)

	// Live preview: the title as a bold heading, then the rendered body.
	pvHead := lipgloss.NewStyle().Foreground(s.th.Muted).Render("Preview")
	pvRule := lipgloss.NewStyle().Foreground(s.th.Overlay).Render(strings.Repeat("─", previewW))
	pvTitle := lipgloss.NewStyle().Foreground(s.th.Text).Bold(true).
		Render(truncate(strings.TrimSpace(s.titleInput.Value()), previewW-1))
	var pvBody string
	if strings.TrimSpace(s.body.Value()) == "" {
		pvBody = mutedStyle(s.th).Render("(nothing to preview yet — write a\ndescription on the left)")
	} else {
		pvBody = renderMarkdown(s.body.Value(), previewW-1, s.th)
	}
	preview := lipgloss.JoinVertical(lipgloss.Left, pvHead, pvRule, "", pvTitle, "", pvBody)
	previewPane := lipgloss.NewStyle().Width(previewW).Height(h).Render(preview)

	return lipgloss.JoinHorizontal(lipgloss.Top, editorPane, stackVBar(s.th, h), previewPane)
}

// renderProposeConfirm is the final gate before opening a PR: it shows the head,
// the auto-detected base, the title, and the draft toggle.
func (s stackModel) renderProposeConfirm(w int) string {
	title := lipgloss.NewStyle().Foreground(s.th.Primary).Bold(true).Render("propose " + s.opName + " — confirm")
	rule := lipgloss.NewStyle().Foreground(s.th.Focus).Render(strings.Repeat("─", w))

	label := lipgloss.NewStyle().Foreground(s.th.Muted)
	value := lipgloss.NewStyle().Foreground(s.th.Text)
	row := func(k, v string) string { return label.Render(pad(k, 8)) + value.Render(v) }

	draftState := "no (ready for review)"
	if s.proposeDraft {
		draftState = "yes"
	}
	draftLine := label.Render(pad("draft", 8)) +
		lipgloss.NewStyle().Foreground(s.th.Warning).Render(draftState) +
		mutedStyle(s.th).Render("   (d to toggle)")

	textW := proseWidth(w)
	effect := fmt.Sprintf("Pushes %s to origin, then opens a pull request targeting %s.", s.opName, val(s.proposeBase))
	effectLine := lipgloss.NewStyle().Foreground(s.th.Warning).Render(wrapPlain("• "+effect, textW, "  "))

	cmdLine := lipgloss.NewStyle().Foreground(s.th.Muted).Render("runs:  " + commandLine(s.pending, s.opName))
	confirm := lipgloss.NewStyle().Foreground(s.th.Success).Render("[enter] open PR") +
		mutedStyle(s.th).Render("    [esc] back to edit")

	return lipgloss.JoinVertical(lipgloss.Left, title, rule, "",
		row("head", s.opName),
		row("base", val(s.proposeBase)),
		row("title", truncate(strings.TrimSpace(s.titleInput.Value()), w-9)),
		draftLine, "",
		effectLine, "", cmdLine, "", confirm)
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
	return styleRunLog(s.th, out, w)
}

// styleRunLog is the shared run-log styler used by the stack run screen and the
// conflict resolver's done screen. git-town colorizes its own output (e.g. bold
// command echoes wrapped in \x1b[1m…\x1b[0m), so strip that first — then Cairn
// fully controls styling and the "[branch] …" command detection sees plain text,
// not an escape prefix.
func styleRunLog(th theme.Theme, out string, w int) string {
	cmd := lipgloss.NewStyle().Foreground(th.Focus).Bold(true)
	res := lipgloss.NewStyle().Foreground(th.Text)
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
	base := lipgloss.NewStyle().Width(s.width).Padding(0, 1)
	// A pending conflict is the one thing worth shouting about — render the count in
	// danger red, the rest of the hint muted.
	if s.phase == stackBrowsing && s.status.Conflicts > 0 {
		red := errStyle(s.th).Bold(true).Render(fmt.Sprintf("%d conflict(s) — R resolve", s.status.Conflicts))
		return surfaceBar(s.th, s.width, base.Render(red+mutedStyle(s.th).Render(" · r refresh · esc dashboard")))
	}
	var help string
	switch s.phase {
	case stackBrowsing:
		switch {
		case s.needsInit():
			help = "enter set up git-town · r refresh · esc dashboard"
		case s.currentUntracked():
			help = "t track this branch · ↑/↓ j/k move · enter choose · r refresh · esc dashboard"
		case s.focus == focusTree:
			help = "↑/↓ j/k branch · enter checkout · tab ←/→ actions · r refresh · esc dashboard"
		default:
			help = "↑/↓ j/k move · enter choose · tab ←/→ tree (checkout) · r refresh · esc dashboard"
		}
	case stackNaming:
		help = "type a name · enter confirm · esc cancel"
	case stackComposing:
		help = "tab switch field · ctrl+s continue · esc cancel"
	case stackConfirming:
		if s.pending.Verb == "propose" {
			help = "enter open PR · d toggle draft · esc back to edit"
		} else {
			help = "enter run · esc cancel"
		}
	case stackRunning:
		help = spinnerFrame + " running git-town — please wait"
	case stackDone:
		help = "any key to return to actions"
	}
	return surfaceBar(s.th, s.width, base.Foreground(s.th.Muted).Render(help))
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
		line = strings.ReplaceAll(line, "<branch>", name)
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
