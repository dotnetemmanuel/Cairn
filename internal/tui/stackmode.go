package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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

// stackRanMsg carries the result of a delegated git-town op.
type stackRanMsg struct {
	out string
	err error
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

	case stackRanMsg:
		s.output = strings.TrimRight(msg.out, "\n")
		s.runErr = msg.err
		s.phase = stackDone
		s.reload() // tree + status now reflect what git-town did
		return s, nil

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
	switch msg.String() {
	case "esc", "q":
		return s, func() tea.Msg { return stackExitMsg{} }
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

	if s.focus == focusTree {
		return s.updateTree(msg)
	}

	switch msg.String() {
	case "j", "down":
		s.cursor = (s.cursor + 1) % len(s.commands)
	case "k", "up":
		s.cursor = (s.cursor - 1 + len(s.commands)) % len(s.commands)
	case "enter":
		c := s.commands[s.cursor]
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
		s.affected = s.affectedBranches(c, "")
		s.phase = stackConfirming
	}
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
	ops := s.ops
	return s, func() tea.Msg {
		out, err := ops.Run(verb, name)
		return stackRanMsg{out: out, err: err}
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
	switch msg.String() {
	case "esc":
		s.phase = stackBrowsing
		return s, nil
	case "enter":
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
	case "amend", "restack":
		return s.descendants(cur)
	case "insert":
		// The new branch slots under cur; cur and everything above it rebases.
		return append([]string{cur}, s.descendants(cur)...)
	case "sync":
		// The whole stack rebases onto trunk.
		var all []string
		if s.tree != nil {
			for _, n := range s.tree.Order {
				if !n.IsTrunk {
					all = append(all, n.Name)
				}
			}
		}
		return all
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
		return s.renderActions(w)
	}
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
	title := lipgloss.NewStyle().Foreground(s.th.Focus).Bold(true).Render(s.pending.Title + " — name the new branch")
	rule := lipgloss.NewStyle().Foreground(s.th.Focus).Render(strings.Repeat("─", w))
	explain := mutedStyle(s.th).Render(wrapPlain(s.pending.Long, w-2, ""))
	field := lipgloss.NewStyle().Foreground(s.th.Text).Render(s.name.View())
	hint := mutedStyle(s.th).Render("enter confirm · esc cancel")
	return lipgloss.JoinVertical(lipgloss.Left, title, rule, "", explain, "", field, "", hint)
}

func (s stackModel) renderConfirm(w int) string {
	c := s.pending
	name := strings.TrimSpace(s.name.Value())
	headline := c.Title
	if name != "" {
		headline += " " + name
	}
	title := lipgloss.NewStyle().Foreground(s.th.Primary).Bold(true).Render(headline + " — confirm")
	rule := lipgloss.NewStyle().Foreground(s.th.Focus).Render(strings.Repeat("─", w))

	what := lipgloss.NewStyle().Foreground(s.th.Text).Render(wrapPlain(c.Long, w-2, ""))

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
			effect = fmt.Sprintf("Slots %s beneath %s; %s and %s re-stack onto it.",
				val(name), cur, cur, humanList(desc))
		} else {
			effect = fmt.Sprintf("Slots %s beneath %s; %s re-stacks onto it.", val(name), cur, cur)
		}
	case "amend":
		if len(desc) > 0 {
			effect = fmt.Sprintf("Rewrites %s's commit, then rebases %s onto it.", cur, humanList(desc))
		} else {
			effect = fmt.Sprintf("Rewrites %s's commit (no branches above to restack).", cur)
		}
	case "restack":
		switch len(desc) {
		case 0:
			effect = fmt.Sprintf("Nothing stacked above %s to restack.", cur)
		case 1:
			effect = fmt.Sprintf("Re-rebases %s onto its parent — no push (still fetches trunk).", desc[0])
		default:
			effect = fmt.Sprintf("Re-rebases %s onto their parents — no push (still fetches trunk).", humanList(desc))
		}
	case "sync":
		if len(s.affected) > 0 {
			effect = "Rebases the whole stack (" + humanList(s.affected) + ") onto the updated trunk, then pushes."
		} else {
			effect = "Pulls the trunk and pushes; no other stack branches to move."
		}
	default:
		effect = "Affects " + cur + "."
	}
	effectLine := lipgloss.NewStyle().Foreground(s.th.Warning).Render("• " + effect)

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
	body := lipgloss.NewStyle().Foreground(s.th.Text).Render(out)

	foot := ""
	if s.phase == stackDone {
		foot = "\n" + mutedStyle(s.th).Render("any key to return")
	}
	return lipgloss.JoinVertical(lipgloss.Left, title, rule, "", body, foot)
}

func (s stackModel) viewFooter(spinnerFrame string) string {
	var help string
	switch s.phase {
	case stackBrowsing:
		if s.focus == focusTree {
			help = "↑/↓ j/k branch · enter checkout · tab ←/→ actions · esc dashboard"
		} else {
			help = "↑/↓ j/k move · enter choose · tab ←/→ tree (checkout) · esc dashboard"
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

// commandLine renders the concrete shell invocation for the confirmation, with
// the real branch name substituted in.
func commandLine(c townie.Command, name string) string {
	if c.Verb == "amend" {
		return "git commit --amend --no-edit  →  git town sync --stack --no-push"
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
