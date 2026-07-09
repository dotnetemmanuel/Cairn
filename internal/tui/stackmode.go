package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

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
	loose  []string    // local branches not in the stack (no git-town parent) — "not in a stack"
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
	// prMerge is branch -> PR landing readiness (mergeable/draft/review), loaded
	// alongside prNums so ship / merge-whole-stack can be dimmed-and-explained when
	// a PR can't land, and the ship-stack confirmation can annotate each branch.
	prMerge map[string]gh.PRMergeability
	// drift is branch -> remote landing state for stack branches whose PR is no
	// longer open (merged or closed) — the signal that someone landed/closed part
	// of this stack on the remote (a teammate ship, a GitHub-UI merge) while the
	// local tree still carries it. Drives the "reconcile with remote" affordance.
	drift map[string]gh.PRLanding

	// Remote stack mode: operate on a repo you have NOT checked out, from its PR-
	// reconstructed stacks (BuildRemoteStacks). Ship actions are pure GitHub API
	// (merge / retarget / delete), skipping the local git-town sync local mode runs.
	// The default (remote=false) is the local git-town tree above.
	remote        bool                        // in remote mode
	remoteStacks  []*stack.Tree               // snapshot of the app's PR-reconstructed stacks (all repos)
	remoteRepo    string                      // chosen repo slug; "" = show the repo chooser
	chooserCursor int                         // index into the chooser's flat repo list
	remoteCursor  int                         // index into the chosen repo's flattened node list
	remoteMerge   map[string]gh.PRMergeability // landing readiness for the chosen repo's PRs

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
	// Land on the branch tree (not the action list): the first thing you usually do
	// is pick which branch to act on. But ONLY when there's a tree — a no-git-town
	// repo shows the "Set up git-town" call-to-action instead, which owns the focus
	// (there's nothing else to focus), so its enter fires the init.
	if s.tree != nil {
		s.focus = focusTree
	}
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
	// Local branches not in the stack ("not in a stack"), shown under the tree so
	// they're visible and can be checked out + added with t.
	s.loose = stack.LooseBranches("", s.tree)
	// Park the tree cursor on the current branch so checkout starts from "here" —
	// searching the combined nav list (stack nodes THEN loose branches), so it also
	// lands on a loose current branch.
	if s.tree != nil {
		nav := s.navBranches()
		s.treeCursor = 0
		for i, name := range nav {
			if name == s.status.Branch {
				s.treeCursor = i
				break
			}
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

// shipChain returns the branches to merge for a whole-stack ship: the ENTIRE
// linear stack the current branch belongs to — from the bottom (the trunk's
// child) up to the top leaf, following single children — in bottom-up merge
// order. "Whole stack" means the whole stack regardless of where HEAD sits in it
// (a blocker like a draft/conflict still halts the run and is surfaced up front).
// If the stack forks above some branch (a branch with >1 child), the linear
// sequence stops there — a fork can't be squash-merged as one chain. Empty when
// the current branch isn't a tracked, non-trunk node.
func (s stackModel) shipChain() []string {
	if s.tree == nil {
		return nil
	}
	n := s.tree.NodeByName(s.status.Branch)
	if n == nil || n.IsTrunk {
		return nil
	}
	// Walk up to the bottom (the trunk's direct child).
	bottom := n
	for p := s.tree.NodeByName(bottom.Parent); p != nil && !p.IsTrunk; p = s.tree.NodeByName(bottom.Parent) {
		bottom = p
	}
	// Walk down through single children to the top leaf, in bottom-up merge order.
	var chain []string
	for cur := bottom; cur != nil && !cur.IsTrunk; {
		chain = append(chain, cur.Name)
		if len(cur.Children) == 1 {
			cur = cur.Children[0]
		} else {
			break // leaf (0) or fork (>1): the linear stack ends here
		}
	}
	return chain
}

// shipBlock returns a hard reason branch's PR can't be merged, plus the concrete
// fix, or ("", "") when it can (as far as we can tell). Blockers: a draft or a
// conflict (from the readiness map), or — once the map has loaded — no open PR at
// all. It stays permissive on missing data: if the readiness fetch hasn't landed
// (map nil), we allow the attempt and let the merge itself report any problem, so
// a slow/failed fetch never wrongly dims a shippable branch.
func (s stackModel) shipBlock(branch string) (reason, hint string) {
	// A branch whose PR left the open state (merged/closed remotely) has no open PR
	// NOT because it needs proposing but because it already landed — point at
	// reconcile, not propose, so a drifted stack never says "propose it (p)".
	if d, ok := s.drift[branch]; ok {
		if d.Merged {
			return "was merged on the remote", "reconcile (X) to update your local copy"
		}
		return "was closed on the remote", "reconcile (X) to update your local copy"
	}
	m, ok := s.prMerge[branch]
	if !ok {
		if s.prMerge != nil { // readiness loaded, and this branch has no open PR
			return "no open PR yet", "propose it (p) first"
		}
		return "", "" // not loaded — don't dim on incomplete info
	}
	return m.Reason(), m.FixHint()
}

// branchMergeable reports no known hard blocker for branch's PR — the ship gate.
func (s stackModel) branchMergeable(branch string) bool {
	reason, _ := s.shipBlock(branch)
	return reason == ""
}

// actionEnabled reports whether a specific command is currently actionable.
func (s stackModel) actionEnabled(c townie.Command) bool {
	if !s.enabled() {
		return false
	}
	// Standing on a branch that itself landed remotely is a degenerate state: the
	// branch is a ghost that reconcile will drop. Funnel the create/maintain verbs
	// to reconcile (X) so the whole pane points at the one sane move. (propose/ready
	// act on the tree cursor and ship/shipstack are already drift-gated elsewhere.)
	if _, landed := s.driftState(s.status.Branch); landed {
		switch c.Verb {
		case "new", "insert", "amend", "restack", "sync":
			return false
		}
	}
	switch c.Verb {
	case "propose":
		// Open a PR for the branch under the tree cursor: it must be a tracked,
		// non-trunk node that doesn't already have an open PR — and hasn't already
		// landed on the remote (a merged/closed branch has no open PR but must NOT be
		// re-proposed; reconcile drops it).
		t := s.proposeTarget()
		if t == "" || s.tree == nil {
			return false
		}
		if _, landed := s.driftState(t); landed {
			return false
		}
		n := s.tree.NodeByName(t)
		return n != nil && !n.IsTrunk && s.prNums[t] == 0
	case "ready":
		// Mark the tree-cursor branch's PR ready — only when it's a tracked, non-trunk
		// node whose open PR is currently a DRAFT (nothing to do otherwise).
		t := s.proposeTarget()
		if t == "" || s.tree == nil {
			return false
		}
		n := s.tree.NodeByName(t)
		if n == nil || n.IsTrunk {
			return false
		}
		m, ok := s.prMerge[t]
		return ok && m.Draft
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
		// are fine; they don't interfere with the rebase.) Finally, dim when we know
		// the PR can't land (draft / conflicting) so it fails up front, not mid-op.
		return s.isBottomBranch() && !s.status.Dirty() && s.branchMergeable(s.status.Branch)
	case "shipstack":
		// Merge the whole stack bottom-up. Needs a chain to ship (a tracked,
		// non-trunk branch), a clean tree (same rebase reason as ship), and the
		// bottom-most PR — the first to merge — must be landable. Higher blockers
		// don't dim it: the run lands what it can and stops, which the confirmation
		// spells out per branch.
		chain := s.shipChain()
		return len(chain) > 0 && !s.status.Dirty() && s.branchMergeable(chain[0])
	default:
		// new / insert can start or extend a stack from any branch.
		return true
	}
}

// currentBase is what the current branch is stacked on — its recorded parent (the
// branch below it, or the trunk for a bottom branch), which is also its PR's base.
// "" when on the trunk, off-tree, or without a git-town tree — nothing to point at.
func (s stackModel) currentBase() string {
	if s.tree == nil {
		return ""
	}
	n := s.tree.NodeByName(s.status.Branch)
	if n == nil || n.IsTrunk {
		return ""
	}
	return n.Parent
}

// driftedInCurrentStack returns the branches in the CURRENT branch's stack (its
// trunk→current chain plus its descendants) that have drifted off their parent —
// the ones a restack would rebase back on top. Scoped to the current stack so a
// drift in an unrelated stack under the same trunk doesn't warn here. Empty when
// on the trunk, off-tree, or nothing has drifted.
func (s stackModel) driftedInCurrentStack() []string {
	if s.tree == nil {
		return nil
	}
	cur := s.tree.NodeByName(s.status.Branch)
	if cur == nil || cur.IsTrunk {
		return nil
	}
	var out []string
	for _, n := range s.tree.Focused(s.status.Branch) {
		if n.Drifted {
			out = append(out, n.Name)
		}
	}
	return out
}

// mergeBottom returns the bottom branch of the current branch's stack — the only
// branch M can merge (its PR targets the trunk). "" when the current branch isn't
// a tracked, non-trunk node.
func (s stackModel) mergeBottom() string {
	if chain := s.shipChain(); len(chain) > 0 {
		return chain[0]
	}
	return ""
}

// actionDisabledReason returns a short, human reason a mutating command is
// currently unavailable — rendered dim in place of the command's description so
// the action list explains ITSELF ("why is this one dim?") instead of just
// greying out. "" when the command is enabled, or when git-town isn't set up at
// all (that whole-pane case is covered by the help line below the list).
func (s stackModel) actionDisabledReason(c townie.Command) string {
	if !s.enabled() || s.actionEnabled(c) {
		return ""
	}
	// On a ghost branch (the current branch landed remotely), the create/maintain
	// verbs are funnelled to reconcile — say so, taking precedence over their normal
	// reasons (e.g. amend's "nothing staged").
	if state, landed := s.driftState(s.status.Branch); landed {
		switch c.Verb {
		case "new", "insert", "amend", "restack", "sync":
			return fmt.Sprintf("%s was %s on the remote — reconcile (X) first", s.status.Branch, state)
		}
	}
	switch c.Verb {
	case "propose":
		t := s.proposeTarget()
		if t == "" || s.tree == nil {
			return ""
		}
		if state, landed := s.driftState(t); landed {
			return fmt.Sprintf("%s was %s on the remote — reconcile (X), don't re-propose", t, state)
		}
		if n := s.tree.NodeByName(t); n == nil {
			return fmt.Sprintf("%s isn't in a stack — check it out, then t", t)
		} else if n.IsTrunk {
			return "the trunk can't have a PR"
		}
		if num := s.prNums[t]; num > 0 {
			return fmt.Sprintf("%s already has PR #%d", t, num)
		}
		return ""
	case "ready":
		t := s.proposeTarget()
		if t == "" || s.tree == nil {
			return ""
		}
		n := s.tree.NodeByName(t)
		if n == nil {
			return fmt.Sprintf("%s isn't in a stack", t)
		}
		if n.IsTrunk {
			return "the trunk has no PR"
		}
		if state, landed := s.driftState(t); landed {
			return fmt.Sprintf("%s was %s on the remote — reconcile (X)", t, state)
		}
		if s.prNums[t] == 0 {
			return fmt.Sprintf("%s has no open PR", t)
		}
		return fmt.Sprintf("%s isn't a draft", t)
	case "amend":
		if !s.onTrackedBranch() {
			return "this branch isn't in a stack"
		}
		if s.status.Staged == 0 {
			return "nothing staged — git add your changes first"
		}
		return ""
	case "restack", "sync":
		if !s.onTrackedBranch() {
			return "this branch isn't in a stack"
		}
		return ""
	case "ship":
		if !s.onTrackedBranch() {
			return "this branch isn't in a stack"
		}
		if !s.isBottomBranch() {
			if b := s.mergeBottom(); b != "" {
				return fmt.Sprintf("only the bottom branch (%s) can merge", b)
			}
			return "only the bottom branch can merge"
		}
		if s.status.Dirty() {
			return "needs a clean working tree"
		}
		if reason, hint := s.shipBlock(s.status.Branch); reason != "" {
			return fmt.Sprintf("%s — %s", reason, hint)
		}
		return ""
	case "shipstack":
		chain := s.shipChain()
		if len(chain) == 0 {
			return "this branch isn't in a stack"
		}
		if s.status.Dirty() {
			return "needs a clean working tree"
		}
		if reason, hint := s.shipBlock(chain[0]); reason != "" {
			return fmt.Sprintf("bottom branch %s %s — %s", chain[0], reason, hint)
		}
		return ""
	}
	return ""
}

// shipStop is one predictable halt in a whole-stack ship: a branch that certainly
// can't merge — a draft, or no open PR — with the concrete fix the author applies.
type shipStop struct {
	branch    string
	reason    string // "still a draft" / "no open PR"
	fixAction string // "press Y to mark it ready" / "press p to propose it"
}

// shipStops walks the merge chain and returns EVERY certain halt (draft / no open
// PR), plus the branches that would land before the FIRST one (all G actually
// ships before it stops). A conflict above the bottom is only a risk — the run
// retargets each branch onto the trunk before merging — so it isn't listed here.
// Meaningful while G is enabled (a blocked bottom dims G and is explained apart).
func (s stackModel) shipStops() (landsFirst []string, stops []shipStop) {
	hit := false
	for _, b := range s.shipChain() {
		reason, fix := "", ""
		if m, ok := s.prMerge[b]; ok {
			if m.Draft {
				reason, fix = "still a draft", "press Y to mark it ready"
			}
		} else if s.prMerge != nil {
			reason, fix = "no open PR", "press p to propose it"
		}
		if reason == "" {
			if !hit {
				landsFirst = append(landsFirst, b)
			}
			continue
		}
		hit = true
		stops = append(stops, shipStop{branch: b, reason: reason, fixAction: fix})
	}
	return landsFirst, stops
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
		if msg.merge != nil {
			s.prMerge = msg.merge
		}
		return s, nil

	case stackDriftMsg:
		// Always assign (even nil): a refetch that finds nothing clears a stale
		// warning after a reconcile brought local back in step with the remote.
		s.drift = msg.drift
		return s, nil

	case remoteRepoMsg:
		// A chosen (or just-shipped) remote repo's fresh trees + readiness. Replace
		// this repo's trees in the snapshot and refresh readiness if it's the one open.
		if msg.trees != nil {
			kept := s.remoteStacks[:0]
			for _, t := range s.remoteStacks {
				if t == nil || t.Repo != msg.repo {
					kept = append(kept, t)
				}
			}
			s.remoteStacks = append(kept, msg.trees...)
		}
		if msg.repo == s.remoteRepo {
			s.remoteMerge = msg.merge
			if n := len(s.remoteNodes()); n > 0 && s.remoteCursor >= n {
				s.remoteCursor = n - 1
			}
			// GitHub computes `mergeable` asynchronously (UNKNOWN right after a merge or
			// retarget). If anything is still computing, poll again shortly so the view
			// resolves itself instead of waiting for a manual refresh — bounded so it
			// never loops forever.
			if msg.attempt < remoteMergePollMax && mergeStillComputing(msg.merge) {
				repo, next := msg.repo, msg.attempt+1
				return s, tea.Tick(1500*time.Millisecond, func(time.Time) tea.Msg {
					return remotePollMsg{repo: repo, attempt: next}
				})
			}
		}
		return s, nil

	case remotePollMsg:
		// The delayed re-fetch fires only if we're still on that remote repo.
		if s.remote && msg.repo == s.remoteRepo {
			return s, fetchRemoteRepo(msg.repo, msg.attempt)
		}
		return s, nil

	case stackStreamMsg:
		if msg.ev.Done {
			s.runErr = msg.ev.Err
			s.phase = stackDone
			// Remote mode has no local checkout to reload — refresh the shipped repo's
			// PRs/tree so the merged branches drop out and the tree reflects the remote.
			if s.remote {
				return s, fetchRemoteRepo(s.remoteRepo, 0)
			}
			s.reload() // tree + status now reflect what git-town did
			// A failed op that left unmerged paths is a conflict — hand off to the
			// full-screen resolver instead of just showing the error.
			if s.runErr != nil && s.status.Conflicts > 0 {
				return s, func() tea.Msg { return enterConflictMsg{dir: "", gitTown: true} }
			}
			// Refresh the tree's PR flags — a propose just added one; a ship/sync may
			// have removed one — and re-check remote drift (a reconcile clears it).
			return s, tea.Batch(fetchStackPRNums(s.repo), s.driftCmd())
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
			if s.remote {
				return s.updateRemote(msg)
			}
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
	// ctrl+r flips to remote stack mode — land a stack you haven't checked out.
	if msg.String() == "ctrl+r" {
		return s.toggleRemote()
	}
	switch msg.String() {
	case "esc", "q":
		return s, func() tea.Msg { return stackExitMsg{} }
	case "r":
		// Re-read lineage + working-tree status, so external git changes (a
		// checkout/commit in another terminal) show without leaving stack mode.
		// Also refresh the PR flags so a PR opened elsewhere shows its #N.
		s.reload()
		return s, tea.Batch(fetchStackPRNums(s.repo), s.driftCmd())
	case "t":
		// Add the current (untracked) branch to the stack under the trunk. Inert
		// otherwise (only meaningful when the branch has no recorded parent).
		if s.currentUntracked() {
			return s.runTrack()
		}
		return s, nil
	case "X":
		// Reconcile the local tree with the remote when part of the stack was landed
		// or closed there. Handled before the focus gate (like t) so it works from
		// either pane; inert when nothing has drifted.
		if s.hasRemoteDrift() {
			return s.startReconcile()
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

	// Direct command-key accelerators (n/p/I/S/R/A/M/G/Y) — but ONLY when the action
	// list is focused, so navigating the branch tree can never accidentally fire a
	// stack action (a stray M/G is destructive). Tab to the actions first. Guarded
	// with !needsInit so they stay inert until git-town is set up.
	if !s.needsInit() && s.focus == focusActions {
		if c := townie.Find(msg.String()); c != nil {
			s.cursor = s.commandIndex(c.Key)
			return s.triggerAction(*c)
		}
	}

	// Tree navigation only when there's a tree to navigate; otherwise (no git-town)
	// keys must fall through to the init call-to-action below, not be swallowed here.
	if s.focus == focusTree && s.tree != nil {
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
	if c.Verb == "ready" {
		// Acts on the tree-cursor branch (like propose), not HEAD — record it for the
		// confirmation and the run.
		s.opName = s.proposeTarget()
		s.name.SetValue("")
		s.affected = nil
		s.phase = stackConfirming
		return s, nil
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

// navBranches is the combined, ordered list the tree cursor moves over: the
// stack nodes (DFS order) first, then the loose "not in a stack" branches. Every
// entry is a checkout target; the loose ones just can't take stack actions until
// they're added to a stack (t). nil when there's no git-town tree.
func (s stackModel) navBranches() []string {
	if s.tree == nil {
		return nil
	}
	out := make([]string, 0, len(s.tree.Order)+len(s.loose))
	for _, n := range s.tree.Order {
		out = append(out, n.Name)
	}
	out = append(out, s.loose...)
	return out
}

// updateTree handles navigation in the branch tree: j/k move the cursor, enter
// checks out the cursored branch. Checkout is cheap and reversible, so it skips
// the confirmation dialog — it just runs and reloads.
func (s stackModel) updateTree(msg tea.KeyMsg) (stackModel, tea.Cmd) {
	nav := s.navBranches()
	if len(nav) == 0 {
		return s, nil
	}
	n := len(nav)
	if s.treeCursor >= n {
		s.treeCursor = 0
	}
	switch msg.String() {
	case "j", "down":
		s.treeCursor = (s.treeCursor + 1) % n
	case "k", "up":
		s.treeCursor = (s.treeCursor - 1 + n) % n
	case "enter":
		target := nav[s.treeCursor]
		if target == s.status.Branch {
			return s, nil // already here
		}
		return s.runOp("checkout", target, "checkout")
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

// runReady marks branch's draft PR ready for review (via gh), streaming progress
// through the same machinery as the other ops. branch is the tree-cursor target.
func (s stackModel) runReady(branch string) (stackModel, tea.Cmd) {
	s.phase = stackRunning
	s.pending = townie.Command{Verb: "ready", Title: "mark ready"}
	s.opName = branch
	s.output = ""
	owner, repo, _ := gh.SplitRepo(s.repo)
	num := s.prNums[branch]
	return s, func() tea.Msg {
		return readStream(readyStream(owner, repo, branch, num))
	}
}

// readyStream marks PR #num (branch's) ready for review, forwarding a friendly
// before/after line around the API call. Stops with the error if the repo or PR
// number is unknown, or the mutation fails.
func readyStream(owner, repo, branch string, num int) <-chan townie.StreamEvent {
	ch := make(chan townie.StreamEvent, 8)
	go func() {
		defer close(ch)
		if owner == "" || repo == "" {
			ch <- townie.StreamEvent{Done: true, Err: fmt.Errorf("can't mark ready: unknown repo")}
			return
		}
		if num == 0 {
			ch <- townie.StreamEvent{Done: true, Err: fmt.Errorf("no open PR found for %s", branch)}
			return
		}
		ch <- townie.StreamEvent{Line: fmt.Sprintf("Marking PR #%d (%s) ready for review…", num, branch)}
		if err := gh.MarkPRReady(owner, repo, num); err != nil {
			ch <- townie.StreamEvent{Done: true, Err: err}
			return
		}
		ch <- townie.StreamEvent{Line: fmt.Sprintf("✓ PR #%d is ready for review.", num)}
		ch <- townie.StreamEvent{Done: true}
	}()
	return ch
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
		return readStream(shipStream(owner, repo, branch, trunk, ops, true))
	}
}

// shipStream lands a stack's bottom branch: merge its PR, retarget the child PRs
// to the trunk, delete the branch, then (syncLocal) git town sync to re-parent
// locally — forwarding output. Stops (with the error) if the lookup or merge
// fails, so a failed merge never proceeds. In remote stack mode syncLocal is
// false: there is no local checkout of this repo, so the API steps (merge /
// retarget / delete) run and the local sync + cleanup are skipped.
func shipStream(owner, repo, branch, trunk string, ops townie.Ops, syncLocal bool) <-chan townie.StreamEvent {
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
			ch <- townie.StreamEvent{Done: true, Err: gh.FriendlyMergeError(err, branch)}
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
		// Remote mode: the API steps are the whole job — no local tree to re-parent.
		if !syncLocal {
			ch <- townie.StreamEvent{Line: fmt.Sprintf("✓ Landed %s. Child PRs now target %s.", branch, val(trunk))}
			ch <- townie.StreamEvent{Done: true}
			return
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
		// git-town's sync can't delete the branch we were ON (the one just shipped),
		// so it lingers as an orphan. Hop to the trunk and remove it so we truly land
		// clean on the trunk.
		for ev := range ops.RemoveMergedLocal(trunk, []string{branch}) {
			if ev.Done {
				if ev.Err != nil {
					ch <- townie.StreamEvent{Line: "  (cleanup: " + ev.Err.Error() + ")"}
				}
				break
			}
			ch <- ev
		}
		ch <- townie.StreamEvent{Done: true}
	}()
	return ch
}

// runShipStack merges a whole stack bottom-up: it loops ship's remote steps for
// each branch in the chain (lowest first), then syncs once at the end. Like
// runShip it's orchestrated here (gh merges + one git-town sync), not a townie
// verb. branches must already be in bottom-up merge order (from shipChain).
func (s stackModel) runShipStack(branches []string) (stackModel, tea.Cmd) {
	s.phase = stackRunning
	s.pending = townie.Command{Verb: "shipstack", Title: "merge whole stack"}
	s.opName = ""
	s.output = ""
	owner, repo, _ := gh.SplitRepo(s.repo)
	trunk := ""
	if s.tree != nil && s.tree.Root != nil {
		trunk = s.tree.Root.Name
	}
	ops := s.ops
	chain := append([]string(nil), branches...) // snapshot; the model is a value copy
	return s, func() tea.Msg {
		return readStream(shipStackStream(owner, repo, chain, trunk, ops, true))
	}
}

// shipStackStream lands a whole stack bottom-up. For each branch it merges the PR
// (squash), retargets that branch's child PRs onto the trunk, and deletes the
// merged branch — exactly the per-branch steps shipStream runs, looped in order.
// It STOPS at the first branch that can't merge (leaving the ones below landed and
// everything above untouched), then runs ONE git town sync so the local stack
// reflects whatever shipped. The Done event carries the blocking error, if any.
func shipStackStream(owner, repo string, branches []string, trunk string, ops townie.Ops, syncLocal bool) <-chan townie.StreamEvent {
	ch := make(chan townie.StreamEvent, 64)
	go func() {
		defer close(ch)
		if owner == "" || repo == "" {
			ch <- townie.StreamEvent{Done: true, Err: fmt.Errorf("can't ship: unknown repo")}
			return
		}
		if len(branches) == 0 {
			ch <- townie.StreamEvent{Done: true, Err: fmt.Errorf("nothing to ship")}
			return
		}
		mergedAny := false
		var merged []string   // branches whose PR actually merged (for local cleanup)
		var stopReason string // the branch we couldn't merge/retarget; the run halts here
		skipSync := false     // set when we halt with a just-merged branch still undeleted
		for i, branch := range branches {
			ch <- townie.StreamEvent{Line: fmt.Sprintf("[%d/%d] %s", i+1, len(branches), branch)}
			num, err := gh.FindPROpenForBranch(owner, repo, branch)
			if err != nil {
				stopReason = err.Error()
				break
			}
			if num == 0 {
				stopReason = "no open PR found for " + branch
				break
			}
			ch <- townie.StreamEvent{Line: fmt.Sprintf("  Merging PR #%d…", num)}
			if err := gh.MergePR(owner, repo, num, "squash"); err != nil {
				stopReason = gh.FriendlyMergeError(err, branch).Error()
				break
			}
			mergedAny = true
			merged = append(merged, branch)
			// Retarget the PRs that pointed at this branch onto the trunk BEFORE
			// deleting it — deleting a branch that still has a child PR pointing at it
			// CLOSES that PR. So if listing or any retarget fails, halt WITHOUT deleting
			// (and skip the sync, since this just-merged branch is still around): a
			// clean stop the user can finish by hand beats a silently-closed child PR.
			retargetOK := true
			if trunk != "" {
				kids, err := gh.PRsWithBase(owner, repo, branch)
				if err != nil {
					retargetOK = false
					ch <- townie.StreamEvent{Line: "    (could not list child PRs: " + err.Error() + ")"}
				}
				for _, kid := range kids {
					ch <- townie.StreamEvent{Line: fmt.Sprintf("  Retargeting PR #%d onto %s…", kid, trunk)}
					if err := gh.RetargetPR(owner, repo, kid, trunk); err != nil {
						retargetOK = false
						ch <- townie.StreamEvent{Line: "    (could not retarget #" + fmt.Sprint(kid) + ": " + err.Error() + ")"}
					}
				}
			}
			if !retargetOK {
				stopReason = fmt.Sprintf("couldn't retarget %s's child PR(s) onto %s — stopped before deleting %s so no child PR is closed; retarget it on GitHub, then re-run", branch, trunk, branch)
				skipSync = true
				break
			}
			ch <- townie.StreamEvent{Line: fmt.Sprintf("  Merged PR #%d. Removing the branch…", num)}
			if err := gh.DeleteRemoteBranch(owner, repo, branch); err != nil {
				ch <- townie.StreamEvent{Line: "    (could not delete remote branch: " + err.Error() + ")"}
			}
		}
		if stopReason != "" {
			ch <- townie.StreamEvent{Line: "Stopped: " + stopReason}
			if mergedAny {
				ch <- townie.StreamEvent{Line: "The branches below it were merged; the rest are untouched."}
			}
		}
		// Sync once so the local tree reflects whatever landed — even on a partial
		// ship, the merged branches must drop out and their children re-parent. Skip
		// only after a retarget halt, where a just-merged branch is still undeleted and
		// sync would try to rebase its squashed commits and conflict.
		var syncErr error
		if syncLocal && mergedAny && !skipSync {
			ch <- townie.StreamEvent{Line: "Syncing the stack…"}
			for ev := range ops.Stream("sync", "") {
				if ev.Done {
					syncErr = ev.Err
					break
				}
				ch <- ev
			}
			// If we shipped the branch we were ON (the top — whole-stack always includes
			// current), git-town's sync couldn't delete it (can't delete HEAD). Hop to
			// the trunk and remove the leftover so a complete ship lands clean on the
			// trunk. No-op (and no branch switch) on a partial ship, where the current
			// branch wasn't merged.
			if syncErr == nil {
				for ev := range ops.RemoveMergedLocal(trunk, merged) {
					if ev.Done {
						if ev.Err != nil {
							ch <- townie.StreamEvent{Line: "  (cleanup: " + ev.Err.Error() + ")"}
						}
						break
					}
					ch <- ev
				}
			}
		}
		// Terminal status. A halt AFTER landing something is a PARTIAL ship — real work
		// succeeded, so it is surfaced as "stopped" (amber), not "failed" (red); the log
		// carries the reason and what landed. A sync failure, or a halt with nothing
		// merged, is the error to show (a sync conflict also routes to the resolver).
		var finalErr error
		switch {
		case syncErr != nil:
			finalErr = syncErr
		case stopReason != "":
			finalErr = fmt.Errorf("%s", stopReason)
		}
		ch <- townie.StreamEvent{Done: true, Err: finalErr}
	}()
	return ch
}

// startReconcile opens the explained confirmation for reconciling the local tree
// with the remote — the drift-detected counterpart to ship's cleanup.
func (s stackModel) startReconcile() (stackModel, tea.Cmd) {
	s.pending = reconcileCommand()
	s.name.SetValue("")
	s.affected = nil
	s.phase = stackConfirming
	return s, nil
}

// runReconcile brings the local stack back in step with the remote: a stack sync
// (pull trunk, rebase survivors, drop merged branches) followed by cleanup of any
// merged branch still checked out — exactly ship's local tail, run on demand when
// the drift check finds branches that landed/closed remotely.
func (s stackModel) runReconcile() (stackModel, tea.Cmd) {
	s.phase = stackRunning
	s.pending = reconcileCommand()
	s.opName = ""
	s.output = ""
	trunk := ""
	if s.tree != nil && s.tree.Root != nil {
		trunk = s.tree.Root.Name
	}
	merged, _ := s.remoteDrift()
	ops := s.ops
	m := append([]string(nil), merged...) // snapshot; the model is a value copy
	return s, func() tea.Msg {
		return readStream(reconcileStream(trunk, m, ops))
	}
}

// reconcileStream syncs the stack, then removes any merged branch git-town's sync
// couldn't delete because it was checked out — forwarding output. A sync failure
// stops with its error (a conflict routes to the resolver, like the other ops).
func reconcileStream(trunk string, merged []string, ops townie.Ops) <-chan townie.StreamEvent {
	ch := make(chan townie.StreamEvent, 64)
	go func() {
		defer close(ch)
		ch <- townie.StreamEvent{Line: "Syncing the stack (pull trunk, rebase, drop merged branches)…"}
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
		// sync deletes merged-remote branches it isn't sitting on; a merged branch you
		// were checked out on lingers (git can't delete HEAD) — hop to trunk, remove it.
		for ev := range ops.RemoveMergedLocal(trunk, merged) {
			if ev.Done {
				if ev.Err != nil {
					ch <- townie.StreamEvent{Line: "  (cleanup: " + ev.Err.Error() + ")"}
				}
				break
			}
			ch <- ev
		}
		ch <- townie.StreamEvent{Line: "✓ Local stack is back in step with the remote."}
		ch <- townie.StreamEvent{Done: true}
	}()
	return ch
}

// --- propose (open a PR) flow ---

// proposeTarget is the branch the propose action acts on: the one under the tree
// cursor (so you can propose any branch in the stack, not just HEAD), falling
// back to the current branch.
func (s stackModel) proposeTarget() string {
	nav := s.navBranches()
	if s.treeCursor >= 0 && s.treeCursor < len(nav) {
		return nav[s.treeCursor]
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

// stackPRNumsMsg carries the branch→open-PR-number map for the tree's #N flags
// and the branch→landing-readiness map for the ship gating/confirmation.
type stackPRNumsMsg struct {
	nums  map[string]int
	merge map[string]gh.PRMergeability
}

// fetchStackPRNums loads the repo's open PRs so the local tree can flag which
// branches already have a PR (#N) and the ship actions can tell whether each PR
// can actually land. One GraphQL call yields both maps; on failure it falls back
// to the REST numbers-only lookup so the #N flags still work. Best-effort: a
// failure leaves the flags as they were.
func fetchStackPRNums(repo string) tea.Cmd {
	return func() tea.Msg {
		owner, name, ok := gh.SplitRepo(repo)
		if !ok {
			return stackPRNumsMsg{}
		}
		merge, err := gh.OpenPRsByBranch(owner, name)
		if err != nil {
			nums, _ := gh.OpenPRNumbersByBranch(owner, name)
			return stackPRNumsMsg{nums: nums}
		}
		nums := make(map[string]int, len(merge))
		for b, m := range merge {
			nums[b] = m.Number
		}
		return stackPRNumsMsg{nums: nums, merge: merge}
	}
}

// stackDriftMsg carries the branch→remote-landing map: the stack branches whose
// PR is merged or closed on the remote, so local stack mode can warn the tree is
// stale and offer to reconcile.
type stackDriftMsg struct {
	drift map[string]gh.PRLanding
}

// fetchStackDrift checks whether any of the local stack's branches were landed or
// closed on the remote (a teammate ship, a GitHub-UI merge) — one lookup per
// branch. Best-effort: a failure leaves the drift map empty (no false warning).
func fetchStackDrift(repo string, branches []string) tea.Cmd {
	return func() tea.Msg {
		owner, name, ok := gh.SplitRepo(repo)
		if !ok || len(branches) == 0 {
			return stackDriftMsg{}
		}
		d, _ := gh.LandedPRsByBranch(owner, name, branches)
		return stackDriftMsg{drift: d}
	}
}

// trackedFeatureBranches lists the CURRENT stack's tracked, non-trunk branches —
// the ones whose remote landing state the drift check looks up. Scoped to the
// current branch's stack (matching where remoteDrift displays it), so the fetch is
// a couple of calls, not one per branch across every stack. nil off-tree; a
// checkout re-fires the fetch for the newly-current stack. Falls back to the whole
// tree when the current branch isn't a node (so nothing is silently unchecked).
func (s stackModel) trackedFeatureBranches() []string {
	if s.tree == nil {
		return nil
	}
	nodes := s.tree.Focused(s.status.Branch)
	if len(nodes) == 0 {
		nodes = s.tree.Order
	}
	var out []string
	for _, n := range nodes {
		if !n.IsTrunk {
			out = append(out, n.Name)
		}
	}
	return out
}

// driftCmd builds the drift-refetch command for the current tree, or nil when
// there are no branches to check. Fired on entry, on refresh, and after any op.
func (s stackModel) driftCmd() tea.Cmd {
	br := s.trackedFeatureBranches()
	if len(br) == 0 {
		return nil
	}
	return fetchStackDrift(s.repo, br)
}

// remoteDrift splits the CURRENT stack's branches whose PR has left the open
// state (from the drift fetch) into those that MERGED and those that were CLOSED
// without merging — the stale branches a reconcile brings back in line. Scoped to
// the current branch's stack (its trunk→current chain plus descendants), matching
// driftedInCurrentStack and the sync --stack the reconcile runs, so drift in an
// unrelated stack doesn't warn here or get swept into this stack's sync. Both
// empty when nothing in this stack has drifted.
func (s stackModel) remoteDrift() (merged, closed []string) {
	if s.tree == nil || len(s.drift) == 0 {
		return nil, nil
	}
	for _, n := range s.tree.Focused(s.status.Branch) {
		if n.IsTrunk {
			continue
		}
		d, ok := s.drift[n.Name]
		if !ok {
			continue
		}
		if d.Merged {
			merged = append(merged, n.Name)
		} else {
			closed = append(closed, n.Name)
		}
	}
	return merged, closed
}

// hasRemoteDrift reports whether any stack branch was landed or closed remotely —
// the gate for the reconcile affordance (key X, the drift banner).
func (s stackModel) hasRemoteDrift() bool {
	m, c := s.remoteDrift()
	return len(m)+len(c) > 0
}

// driftState returns a branch's remote landing state ("merged"/"closed") and
// whether it drifted at all — so gates and reasons can steer a landed branch to
// reconcile (X) instead of propose/restack, which don't apply once it has landed.
func (s stackModel) driftState(branch string) (state string, landed bool) {
	d, ok := s.drift[branch]
	if !ok {
		return "", false
	}
	if d.Merged {
		return "merged", true
	}
	return "closed", true
}

// driftLabel annotates each drifted branch with its PR number (from the drift
// fetch, since a merged/closed branch is gone from the open-PR map), so the
// warning reads "feat-a (PR #12)" and cross-references GitHub.
func (s stackModel) driftLabel(branches []string) []string {
	out := make([]string, 0, len(branches))
	for _, b := range branches {
		if d, ok := s.drift[b]; ok && d.Number > 0 {
			out = append(out, fmt.Sprintf("%s (PR #%d)", b, d.Number))
		} else {
			out = append(out, b)
		}
	}
	return out
}

// reconcileCommand is the synthetic command behind the "reconcile with remote"
// affordance — contextual (shown only when a stack branch landed/closed remotely),
// so it's not in townie.Catalog(), but it routes through the confirm → run flow.
func reconcileCommand() townie.Command {
	return townie.Command{
		Verb: "reconcile", Title: "reconcile with remote", Mutates: true,
		Short: "sync the local stack with what landed on the remote",
		Long: "Part of this stack was landed or closed on GitHub (a teammate shipped " +
			"it, or you merged from the web), so your local git-town tree is out of " +
			"date. This runs a stack sync — pulls the latest trunk, rebases the " +
			"surviving branches onto it, and drops the branches whose PRs merged — then " +
			"cleans up any merged branch you are still sitting on, leaving local and " +
			"remote back in step. Nothing new is pushed or merged.",
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
		// Remote mode: the ship/ready runners are API-only variants that skip the
		// local sync and act on the chosen remote repo, not the cwd tree.
		if s.remote {
			switch s.pending.Verb {
			case "ship":
				return s.runRemoteShip(s.opName)
			case "shipstack":
				return s.runRemoteShipStack(s.opName)
			case "ready":
				return s.runRemoteReady(s.opName)
			}
			return s, nil
		}
		// ship isn't a git-town verb: merge the PR via gh, then sync. It always
		// acts on the current (bottom) branch.
		if s.pending.Verb == "ship" {
			return s.runShip(s.status.Branch)
		}
		// shipstack merges the whole chain bottom-up (s.affected = shipChain).
		if s.pending.Verb == "shipstack" {
			return s.runShipStack(s.affected)
		}
		// ready marks the tree-cursor branch's draft PR ready (via gh).
		if s.pending.Verb == "ready" {
			return s.runReady(s.opName)
		}
		// reconcile syncs the local tree with what landed remotely (gh-detected drift).
		if s.pending.Verb == "reconcile" {
			return s.runReconcile()
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
	case "shipstack":
		// The bottom-up list of branches this ships (what the confirmation lists).
		return s.shipChain()
	default: // new — creates a leaf, rebases nothing
		return nil
	}
}

// View renders the three zones. spinnerFrame is threaded from the root model so
// the running state animates with the same spinner as the rest of the app.
// header is the app's brand masthead (rendered by the root model, which owns the
// login/rate it shows) so stack mode keeps the same top bar as the dashboard.
func (s stackModel) View(spinnerFrame, header string) string {
	bodyH := s.height - headerH /*brand*/ - 1 /*statusline*/ - 1 /*footer*/
	if bodyH < 1 {
		bodyH = 1
	}

	// Remote mode has its own statusline (mode + chosen repo) and body (chooser or
	// remote tree), sharing the brand header and footer with local mode.
	if s.remote {
		return lipgloss.JoinVertical(lipgloss.Left, header,
			s.remoteStatusline(s.width), s.renderRemote(s.width, bodyH), s.viewFooter(spinnerFrame))
	}

	statusline := renderStatusline(s.th, s.repo, s.status, s.currentBase(), s.hasGitTown(), s.width)

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
	// Remote mode sizes to the reconstructed remote nodes (the local tree is
	// irrelevant, and may be nil, when acting on a repo you haven't checked out).
	if s.remote {
		for _, n := range s.remoteNodes() {
			need := 2 + 2*n.Depth + lipgloss.Width(n.Name) + 1
			if n.PRNumber > 0 {
				need += len(fmt.Sprintf(" #%d", n.PRNumber)) + len(" conflicts")
			}
			if need > w {
				w = need
			}
		}
		maxW := bodyWidth(s.width) / 2
		if maxW < stackPaneW {
			maxW = stackPaneW
		}
		if w > maxW {
			w = maxW
		}
		return w
	}
	if s.tree != nil {
		for _, n := range s.tree.Order {
			need := 2 + 2*n.Depth + lipgloss.Width(n.Name) + 1 // marker + indent + name + slack
			if num := s.prNums[n.Name]; num > 0 {
				need += len(fmt.Sprintf(" #%d", num))
			}
			if !n.IsTrunk {
				if _, ok := s.drift[n.Name]; ok {
					need += len(" merged")
				} else if m, ok := s.prMerge[n.Name]; ok && m.Draft {
					need += len(" draft")
				} else if s.prNums != nil && s.prNums[n.Name] == 0 {
					need += len(" no PR")
				}
			}
			if n.Drifted {
				need += 2
			}
			if need > w {
				w = need
			}
		}
		for _, name := range s.loose {
			need := 4 + lipgloss.Width(name) + 1 // marker + indent + name + slack
			if num := s.prNums[name]; num > 0 {
				need += len(fmt.Sprintf(" #%d", num))
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
		num := s.prNums[n.Name]
		if num > 0 {
			suffix += " " + lipgloss.NewStyle().Foreground(s.th.Accent2).Render(fmt.Sprintf("#%d", num))
		}
		// Landing readiness at a glance (once the PR data has loaded): a draft PR is
		// what silently stops a whole-stack ship; a non-trunk branch with no open PR
		// can't be merged/shipped yet.
		if !n.IsTrunk {
			if d, ok := s.drift[n.Name]; ok {
				// Landed/closed on the remote (stale locally) — clearer than "no PR".
				label := "merged"
				if !d.Merged {
					label = "closed"
				}
				suffix += " " + lipgloss.NewStyle().Foreground(s.th.Warning).Render(label)
			} else if m, ok := s.prMerge[n.Name]; ok && m.Draft {
				suffix += " " + lipgloss.NewStyle().Foreground(s.th.Warning).Render("draft")
			} else if s.prNums != nil && num == 0 {
				suffix += " " + lipgloss.NewStyle().Foreground(s.th.Muted).Render("no PR")
			}
		}
		// The local-drift ⚠ is suppressed once a branch has landed remotely: its
		// "merged"/"closed" tag already explains it, and reconcile (not restack) is
		// the fix, so a drift marker would just be conflicting noise.
		if _, landed := s.drift[n.Name]; n.Drifted && !landed {
			suffix += " " + lipgloss.NewStyle().Foreground(s.th.Warning).Render("⚠")
		}
		used := 2 + len(indent) + lipgloss.Width(suffix)
		b.WriteString(marker + indent + nameStyle.Render(truncate(n.Name, w-used-1)) + suffix + "\n")
	}

	// Loose branches: real git branches that aren't part of a stack yet (no
	// git-town parent). Shown under a divider so they're visible and can be checked
	// out + added with t. They continue the same cursor index space as the tree.
	if len(s.loose) > 0 {
		lbl := "not in a stack"
		dashN := w - lipgloss.Width(lbl) - 3 // "─ " before + " " after
		if dashN < 0 {
			dashN = 0
		}
		// The dashes stay muted-divider faint, but the LABEL is Muted (readable text)
		// so "not in a stack" doesn't disappear on a dark background.
		dashStyle := lipgloss.NewStyle().Foreground(s.th.Overlay)
		divider := dashStyle.Render("─ ") + mutedStyle(s.th).Render(lbl) + dashStyle.Render(" "+strings.Repeat("─", dashN))
		b.WriteString(divider + "\n")
		base := len(s.tree.Order)
		for j, name := range s.loose {
			idx := base + j
			nameStyle := lipgloss.NewStyle().Foreground(s.th.Muted)
			marker := "  "
			switch {
			case focused && idx == s.treeCursor:
				marker = lipgloss.NewStyle().Foreground(s.th.Focus).Bold(true).Render(focusGlyph + " ")
			case name == s.status.Branch:
				marker = lipgloss.NewStyle().Foreground(s.th.Primary).Render("▌ ")
				nameStyle = lipgloss.NewStyle().Foreground(s.th.Primary).Bold(true)
			}
			suffix := ""
			if num := s.prNums[name]; num > 0 {
				suffix += " " + lipgloss.NewStyle().Foreground(s.th.Accent2).Render(fmt.Sprintf("#%d", num))
			}
			used := 4 + lipgloss.Width(suffix)
			b.WriteString(marker + "  " + nameStyle.Render(truncate(name, w-used-1)) + suffix + "\n")
		}
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

	// Warning lines above the list explain predictable failures BEFORE you try:
	// a not-in-a-stack branch, a merge that can't run, or a whole-stack ship that
	// will halt partway. Each names the fix so the user is never left guessing.
	warn := func(msg string) string {
		return colorBranches(lipgloss.NewStyle().Foreground(s.th.Warning), s.th, wrapPlain(msg, proseWidth(w), "  "), s.allBranchNames())
	}
	// warnList renders a header + a bulleted checklist of fixes + an optional footer
	// (wrapPlain flattens newlines, so each line is wrapped on its own and joined).
	// Used when one warning carries MORE THAN ONE fix, so they read as a checklist.
	warnList := func(header string, bullets []string, footer string) string {
		tw := proseWidth(w)
		lines := []string{wrapPlain(header, tw, "  ")}
		for _, bl := range bullets {
			lines = append(lines, "  • "+wrapPlain(bl, tw-4, "    "))
		}
		if footer != "" {
			lines = append(lines, wrapPlain(footer, tw, "  "))
		}
		return colorBranches(lipgloss.NewStyle().Foreground(s.th.Warning), s.th, strings.Join(lines, "\n"), s.allBranchNames())
	}
	var banners []string
	// Remote drift is the most urgent thing to surface: branches in this stack were
	// landed or closed on the remote (a teammate shipped, or a GitHub-UI merge) and
	// the local tree is now stale. Name each with its PR number and point at the
	// one-key reconcile that brings local back in step.
	if merged, closed := s.remoteDrift(); len(merged)+len(closed) > 0 {
		trunk := "the trunk"
		if s.tree != nil && s.tree.Root != nil {
			trunk = s.tree.Root.Name
		}
		var clauses []string
		if len(merged) > 0 {
			was := "was"
			if len(merged) > 1 {
				was = "were"
			}
			clauses = append(clauses, fmt.Sprintf("%s %s merged on the remote", humanList(s.driftLabel(merged)), was))
		}
		if len(closed) > 0 {
			was := "was"
			if len(closed) > 1 {
				was = "were"
			}
			clauses = append(clauses, fmt.Sprintf("%s %s closed on the remote", humanList(s.driftLabel(closed)), was))
		}
		banners = append(banners, warn(fmt.Sprintf(
			"⚠ %s — your local stack is out of date. Press X to reconcile (sync the stack, drop merged branches, pull %s).",
			strings.Join(clauses, ", and "), trunk)))
	}
	if s.currentUntracked() {
		banners = append(banners, warn(fmt.Sprintf("⚠ %s isn't in a stack yet — press t to add it under %s.",
			s.status.Branch, val(s.trackParent()))))
	} else if chain := s.shipChain(); len(chain) > 0 && !s.status.Dirty() && !s.hasRemoteDrift() {
		// Suppressed while the current stack has remote drift: the reconcile banner
		// above already says what to do (X), so a "can't merge / propose it" note here
		// would be misleading (the bottom's PR merged, it doesn't need proposing).
		if reason, hint := s.shipBlock(chain[0]); reason != "" {
			// The bottom PR (first to land) can't merge → M and G are dim. Name the
			// blocker AND the fix — the "dim and explain" half of the gating.
			banners = append(banners, warn(fmt.Sprintf("⚠ %s can't be merged: %s. %s.", chain[0], reason, capFirst(hint))))
		} else if landsFirst, stops := s.shipStops(); len(stops) == 1 {
			// One predictable halt: a single sentence with where it stops + the fix.
			st := stops[0]
			msg := fmt.Sprintf("⚠ merge whole stack (G) will stop at %s: %s.", st.branch, st.reason)
			if len(landsFirst) > 0 {
				msg += fmt.Sprintf(" Lands %s first; %s and above are left untouched.", humanList(landsFirst), st.branch)
			} else {
				msg += fmt.Sprintf(" Nothing lands; %s and above are left untouched.", st.branch)
			}
			msg += fmt.Sprintf(" Select %s in the tree, then %s.", st.branch, st.fixAction)
			banners = append(banners, warn(msg))
		} else if len(stops) > 1 {
			// Several predictable blockers → a bulleted checklist, one fix per line.
			header := fmt.Sprintf("⚠ merge whole stack (G) can't land the whole stack yet — %d branches need a fix first:", len(stops))
			bullets := make([]string, 0, len(stops))
			for _, st := range stops {
				bullets = append(bullets, fmt.Sprintf("%s — %s; select it, then %s", st.branch, st.reason, st.fixAction))
			}
			footer := fmt.Sprintf("For now G stops at %s with nothing landed.", stops[0].branch)
			if len(landsFirst) > 0 {
				footer = fmt.Sprintf("For now G lands %s, then stops at %s; the rest stay untouched.", humanList(landsFirst), stops[0].branch)
			}
			banners = append(banners, warnList(header, bullets, footer))
		}
	}
	// Drift: a branch in this stack sits on an out-of-date parent (the parent was
	// amended/rewritten under it). The tree marks it ⚠, but that alone doesn't say
	// what to do — restack (R) rebases it back on top. Surface it independently of
	// the merge banners (drift and mergeability are orthogonal). Suppressed when the
	// stack has remote drift: reconcile (X) runs a full sync that rebases the
	// survivors too, so it supersedes restack — showing both would give rival advice.
	if drifted := s.driftedInCurrentStack(); len(drifted) > 0 && !s.hasRemoteDrift() {
		have, whose := "has", "its parent"
		if len(drifted) > 1 {
			have, whose = "have", "their parents"
		}
		banners = append(banners, warn(fmt.Sprintf("⚠ %s %s drifted from %s — press R to restack (rebase back on top).",
			humanList(drifted), have, whose)))
	}
	banner := ""
	if len(banners) > 0 {
		banner = strings.Join(banners, "\n") + "\n\n"
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
		if c.Verb == "ready" {
			if t := s.proposeTarget(); t != "" {
				short = "mark " + t + " ready for review"
			}
		}
		labelStyle := lipgloss.NewStyle().Foreground(s.th.Text).Bold(true)
		shortStyle := lipgloss.NewStyle().Foreground(s.th.Muted)
		if !on {
			// Disabled rows: use Muted (inactive TEXT), not Overlay (divider color),
			// which was too faint to read in both light and dark. Replace the generic
			// description with the concrete reason it's dim, so the row explains itself.
			if reason := s.actionDisabledReason(c); reason != "" {
				short = reason
			}
			key = lipgloss.NewStyle().Foreground(s.th.Muted).Render(c.Key)
			labelStyle = lipgloss.NewStyle().Foreground(s.th.Muted)
			shortStyle = lipgloss.NewStyle().Foreground(s.th.Muted)
		}
		// Color any branch NAMED in the description/reason (e.g. the merge bottom, the
		// propose target) so it stands out the same as in the tree.
		line := fmt.Sprintf("%s  %s — %s", key, labelStyle.Render(label), colorBranches(shortStyle, s.th, short, s.allBranchNames()))
		if i == s.cursor && s.focus == focusActions {
			// Same full-width focusBg highlight as the focused PR row: styledBar
			// reasserts the background after each fragment's reset so the bar spans
			// the whole line (both themes), with the cursor glyph in Primary.
			cursor := lipgloss.NewStyle().Foreground(s.th.Primary).Render(focusGlyph + " ")
			line = styledBar(s.th.Text, s.th.FocusBg, w, cursor+line)
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
	if s.pending.Verb == "shipstack" {
		return s.renderShipStackConfirm(w)
	}
	c := s.pending
	name := strings.TrimSpace(s.name.Value())
	headline := c.Title
	// init's "name" is the trunk, already shown in the effect + runs lines, so
	// keep its headline clean ("set up git-town — confirm").
	if name != "" && c.Verb != "init" {
		headline += " " + name
	}
	// ready acts on the tree-cursor branch (s.opName), not a typed name — name it.
	if c.Verb == "ready" {
		headline = "mark " + val(s.opName) + " ready"
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
	case "ready":
		effect = fmt.Sprintf("Marks %s's pull request ready for review on GitHub (takes it out of draft). This can notify reviewers and start required checks; nothing merges.", val(s.opName))
	case "reconcile":
		trunk := "the trunk"
		if s.tree != nil && s.tree.Root != nil {
			trunk = s.tree.Root.Name
		}
		merged, closed := s.remoteDrift()
		switch {
		case len(merged) > 0 && len(closed) > 0:
			effect = fmt.Sprintf("Syncs the stack onto the latest %s, drops %s (merged remotely), and rebases the survivors; %s (closed remotely) stays local for you to decide on.", trunk, humanList(merged), humanList(closed))
		case len(merged) > 0:
			effect = fmt.Sprintf("Syncs the stack onto the latest %s, drops %s (merged remotely), and rebases the branches above onto %s.", trunk, humanList(merged), trunk)
		default:
			effect = fmt.Sprintf("Syncs the stack onto the latest %s and rebases it; %s was closed remotely and stays local for you to decide on.", trunk, humanList(closed))
		}
	default:
		effect = "Affects " + cur + "."
	}
	effectLine := colorBranches(lipgloss.NewStyle().Foreground(s.th.Warning), s.th, wrapPlain("• "+effect, textW, "  "), s.allBranchNames())

	// A ship isn't dimmed for a review requirement (repo admins can bypass it, and
	// required-check state is only certain at merge time), so surface it here as a
	// caution — a merge that GitHub then refuses is explained by FriendlyMergeError.
	cautionLine := ""
	if c.Verb == "ship" {
		if m, ok := s.prMerge[s.status.Branch]; ok {
			if cau := m.Caution(); cau != "" {
				cautionLine = lipgloss.NewStyle().Foreground(s.th.Warning).Render(wrapPlain("⚠ "+cau, textW, "  "))
			}
		}
	}

	cmdLine := lipgloss.NewStyle().Foreground(s.th.Muted).Render("runs:  " + commandLine(c, name))
	confirm := lipgloss.NewStyle().Foreground(s.th.Success).Render("[enter] do it") +
		mutedStyle(s.th).Render("    [esc] cancel")

	parts := []string{title, rule, "",
		lipgloss.NewStyle().Foreground(s.th.Muted).Render("What this does:"),
		what, "", effectLine}
	if cautionLine != "" {
		parts = append(parts, "", cautionLine)
	}
	parts = append(parts, "", cmdLine, "", confirm)
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// renderShipStackConfirm is the gate before merging a whole stack — the most
// destructive action in Cairn. It leads with a loud danger warning, then lists
// every branch to be merged in bottom-up order, each annotated with its PR number
// and landing readiness (✓ ready, ⚠ blocked/no PR, ? still computing), so there is
// no ambiguity about what lands and where it might stop.
func (s stackModel) renderShipStackConfirm(w int) string {
	trunk := "main"
	if s.tree != nil && s.tree.Root != nil {
		trunk = s.tree.Root.Name
	}
	branches := s.affected
	textW := proseWidth(w)

	warn := lipgloss.NewStyle().Foreground(s.th.Danger).Bold(true).
		Render(wrapPlain("⚠ SERIOUS — this merges the ENTIRE stack and cannot be undone.", textW, "  "))
	title := lipgloss.NewStyle().Foreground(s.th.Primary).Bold(true).
		Render(fmt.Sprintf("merge whole stack (%d branches) — confirm", len(branches)))
	rule := lipgloss.NewStyle().Foreground(s.th.Danger).Render(strings.Repeat("─", w))
	what := lipgloss.NewStyle().Foreground(s.th.Text).Render(wrapPlain(s.pending.Long, textW, ""))

	// The ordered, annotated merge list.
	var rows []string
	for i, b := range branches {
		numStr := "no PR"
		if n := s.prNums[b]; n > 0 {
			numStr = fmt.Sprintf("#%d", n)
		}
		mark, markColor, note := "✓", s.th.Success, ""
		if m, ok := s.prMerge[b]; ok {
			switch {
			case m.Draft:
				// Draft state is independent of the base, so this is a certain stop.
				mark, markColor, note = "⚠", s.th.Danger, "still a draft — stops here"
			case m.Mergeable == "CONFLICTING":
				// GitHub computes this against the PR's CURRENT base (the branch below
				// it), but the run retargets each branch onto the trunk before merging —
				// so above the bottom it is a risk, not a certainty. (The bottom already
				// targets trunk and the gate dims it, so a conflict here is always higher.)
				if i == 0 {
					mark, markColor, note = "⚠", s.th.Danger, "conflicts with the trunk — stops here"
				} else {
					mark, markColor, note = "⚠", s.th.Warning, "conflicts with its base — may stop here"
				}
			case m.Mergeable == "UNKNOWN":
				mark, markColor, note = "?", s.th.Warning, "mergeability still computing"
			case m.Caution() != "":
				// Not a certain block (repo admins bypass, checks unknown here), so it
				// still tries — but flag the risk up front.
				mark, markColor, note = "?", s.th.Warning, m.Caution()
			}
		} else if s.prMerge != nil {
			mark, markColor, note = "⚠", s.th.Danger, "no open PR — stops here"
		} else {
			mark, markColor, note = "?", s.th.Warning, "readiness not loaded"
		}
		line := fmt.Sprintf("%2d. %s %s  %s",
			i+1,
			lipgloss.NewStyle().Foreground(markColor).Render(mark),
			lipgloss.NewStyle().Foreground(s.th.Accent2).Render(b),
			lipgloss.NewStyle().Foreground(s.th.Muted).Render(numStr))
		if note != "" {
			line += "  " + lipgloss.NewStyle().Foreground(markColor).Render(note)
		}
		rows = append(rows, line)
	}
	list := strings.Join(rows, "\n")

	orderNote := mutedStyle(s.th).Render(wrapPlain(
		"Merged bottom-up, in this order. If one can't merge, the branches below it "+
			"still land and everything above is left untouched.", textW, ""))
	effect := fmt.Sprintf("Squash-merges each PR into %s, deletes each merged branch, then syncs once so what remains re-parents onto %s.", trunk, trunk)
	effectLine := colorBranches(lipgloss.NewStyle().Foreground(s.th.Warning), s.th, wrapPlain("• "+effect, textW, "  "), s.allBranchNames())
	cmdLine := lipgloss.NewStyle().Foreground(s.th.Muted).Render("runs:  " + s.pending.Hint())
	confirm := lipgloss.NewStyle().Foreground(s.th.Danger).Bold(true).Render(fmt.Sprintf("[enter] merge %d branches", len(branches))) +
		mutedStyle(s.th).Render("    [esc] cancel")

	return lipgloss.JoinVertical(lipgloss.Left, warn, "", title, rule, "",
		what, "",
		lipgloss.NewStyle().Foreground(s.th.Muted).Render("Merge order (bottom-up) — readiness:"),
		list, "", orderNote, "", effectLine, "", cmdLine, "", confirm)
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
	branch := lipgloss.NewStyle().Foreground(s.th.Accent2)
	row := func(k, v string) string { return label.Render(pad(k, 8)) + value.Render(v) }
	branchRow := func(k, v string) string { return label.Render(pad(k, 8)) + branch.Render(v) }

	draftState := "no (ready for review)"
	if s.proposeDraft {
		draftState = "yes"
	}
	draftLine := label.Render(pad("draft", 8)) +
		lipgloss.NewStyle().Foreground(s.th.Warning).Render(draftState) +
		mutedStyle(s.th).Render("   (d to toggle)")

	textW := proseWidth(w)
	effect := fmt.Sprintf("Pushes %s to origin, then opens a pull request targeting %s.", s.opName, val(s.proposeBase))
	effectLine := colorBranches(lipgloss.NewStyle().Foreground(s.th.Warning), s.th, wrapPlain("• "+effect, textW, "  "), s.allBranchNames())

	cmdLine := lipgloss.NewStyle().Foreground(s.th.Muted).Render("runs:  " + commandLine(s.pending, s.opName))
	confirm := lipgloss.NewStyle().Foreground(s.th.Success).Render("[enter] open PR") +
		mutedStyle(s.th).Render("    [esc] back to edit")

	return lipgloss.JoinVertical(lipgloss.Left, title, rule, "",
		branchRow("head", s.opName),
		branchRow("base", val(s.proposeBase)),
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
	case s.runErr != nil && s.pending.Verb == "shipstack":
		// A whole-stack ship that ends with an error is a controlled halt ("lands what
		// it can, stops with a reason") — amber "stopped", not a red "failed", since
		// earlier branches may well have merged. The log carries what landed.
		status = colorBranches(lipgloss.NewStyle().Foreground(s.th.Warning), s.th, "stopped: "+s.runErr.Error(), s.allBranchNames())
	case s.runErr != nil:
		status = colorBranches(lipgloss.NewStyle().Foreground(s.th.Danger), s.th, "failed: "+s.runErr.Error(), s.allBranchNames())
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
	return styleRunLog(s.th, out, w, s.logBranchNames())
}

// logBranchNames is the set of branch names to highlight in the run log: the tree
// plus the op's own branches (s.affected / s.opName), so branches that the op just
// deleted — gone from the tree after the reload — are still colored in the output.
func (s stackModel) logBranchNames() []string {
	set := map[string]bool{}
	var out []string
	add := func(n string) {
		if n != "" && !set[n] {
			set[n] = true
			out = append(out, n)
		}
	}
	for _, n := range s.allBranchNames() {
		add(n)
	}
	for _, n := range s.affected {
		add(n)
	}
	add(s.opName)
	return out
}

// styleRunLog is the shared run-log styler used by the stack run screen and the
// conflict resolver's done screen. git-town colorizes its own output (e.g. bold
// command echoes wrapped in \x1b[1m…\x1b[0m), so strip that first — then Cairn
// fully controls styling and the "[branch] …" command detection sees plain text,
// not an escape prefix.
func styleRunLog(th theme.Theme, out string, w int, branches []string) string {
	cmd := lipgloss.NewStyle().Foreground(th.Focus).Bold(true)
	res := lipgloss.NewStyle().Foreground(th.Text)
	var b strings.Builder
	for i, ln := range strings.Split(sanitizeCR(ansi.Strip(out)), "\n") {
		if i > 0 {
			b.WriteByte('\n')
		}
		if isCommandEcho(ln) {
			// Structural marker (color-independent) + accent so command rows stand
			// out even where a terminal flattens truecolor. Branch names inside still
			// get the branch color (colorBranches re-asserts cmd after each).
			b.WriteString(colorBranches(cmd, th, "❯ "+ansi.Wrap(ln, w-2, ""), branches))
		} else {
			b.WriteString(colorBranches(res, th, ansi.Wrap(ln, w, ""), branches))
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
		case s.remote && s.remoteRepo == "":
			help = "↑/↓ j/k repo · enter open · ctrl+r local · esc back · q dashboard"
		case s.remote:
			help = "↑/↓ j/k branch · M merge · G merge stack · Y ready · r refresh · esc repos · ctrl+r local"
		case s.needsInit():
			help = "enter set up git-town · r refresh · ctrl+r remote · esc dashboard"
		case s.currentUntracked():
			help = "t track this branch · ↑/↓ j/k move · enter choose · r refresh · ctrl+r remote · esc dashboard"
		case s.hasRemoteDrift():
			help = "X reconcile · ↑/↓ j/k move · enter choose · tab ←/→ tree · r refresh · ctrl+r remote · esc dashboard"
		case s.focus == focusTree:
			help = "↑/↓ j/k branch · enter checkout · tab ←/→ actions · r refresh · ctrl+r remote · esc dashboard"
		default:
			help = "↑/↓ j/k move · enter choose · tab ←/→ tree · r refresh · ctrl+r remote · esc dashboard"
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
		what := "git-town"
		if s.remote {
			what = "the merge on GitHub"
		}
		help = spinnerFrame + " running " + what + " — please wait"
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

// allBranchNames is every branch Cairn knows locally — the stack nodes plus the
// loose "not in a stack" branches. It's the set colorBranches highlights so a
// branch NAMED in a message (a warning, a reason, a confirmation) is colored the
// same way it is in the tree, wherever it appears.
func (s stackModel) allBranchNames() []string {
	if s.remote {
		var out []string
		for _, n := range s.remoteNodes() {
			out = append(out, n.Name)
		}
		return out
	}
	var out []string
	if s.tree != nil {
		for _, n := range s.tree.Order {
			out = append(out, n.Name)
		}
	}
	out = append(out, s.loose...)
	return out
}

// isNameByte reports whether b can appear inside a git branch name (letters,
// digits, and - _ / .). Used for the word-boundary test when highlighting names.
func isNameByte(b byte) bool {
	return b == '-' || b == '_' || b == '/' || b == '.' ||
		(b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// branchNameAt returns the branch name (from names, which must be sorted
// longest-first) that begins at line[pos] on a WORD BOUNDARY, or "" if none — so
// "main" isn't matched inside "remains" and "base" not inside "feat-base".
func branchNameAt(line string, pos int, names []string) string {
	if pos > 0 && isNameByte(line[pos-1]) {
		return "" // mid-word — not the start of a branch name
	}
	for _, n := range names {
		if strings.HasPrefix(line[pos:], n) {
			if end := pos + len(n); end < len(line) && isNameByte(line[end]) {
				continue // name is only a prefix of a longer word (main → remains)
			}
			return n
		}
	}
	return ""
}

// colorBranches renders text with every occurrence of a known branch name in the
// branch color (Accent2, the same identifier color the tree uses for #N), and all
// other text in base — re-asserting base after each branch so the run color is
// preserved across the inner style's reset. Longest names match first so a name
// that is a prefix of another (feat vs feat-base) doesn't mis-highlight. Cut
// points are always at branch-name starts (ASCII), so multi-byte glyphs in the
// surrounding text are never split.
func colorBranches(base lipgloss.Style, th theme.Theme, text string, branches []string) string {
	names := make([]string, 0, len(branches))
	for _, n := range branches {
		if n != "" {
			names = append(names, n)
		}
	}
	sort.Slice(names, func(i, j int) bool { return len(names[i]) > len(names[j]) })
	branchStyle := lipgloss.NewStyle().Foreground(th.Accent2)
	// Colorize per line: lipgloss.Render pads a MULTI-line string into a rectangular
	// block, so a segment that spanned a newline would inject stray spaces. Rendering
	// one line at a time keeps every Render single-line.
	colorLine := func(line string) string {
		if line == "" || len(names) == 0 {
			return base.Render(line)
		}
		var b strings.Builder
		for i := 0; i < len(line); {
			if m := branchNameAt(line, i, names); m != "" {
				b.WriteString(branchStyle.Render(m))
				i += len(m)
				continue
			}
			j := i + 1
			for j < len(line) && branchNameAt(line, j, names) == "" {
				j++
			}
			b.WriteString(base.Render(line[i:j]))
			i = j
		}
		return b.String()
	}
	lines := strings.Split(text, "\n")
	for i, ln := range lines {
		lines[i] = colorLine(ln)
	}
	return strings.Join(lines, "\n")
}

// capFirst upper-cases the first rune of s (for starting a sentence with a hint
// that is otherwise written lower-case). ASCII-first is enough for our hints.
func capFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
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
