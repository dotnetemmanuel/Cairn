package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/dotnetemmanuel/cairn/internal/gh"
	"github.com/dotnetemmanuel/cairn/internal/stack"
	"github.com/dotnetemmanuel/cairn/internal/theme"
	"github.com/dotnetemmanuel/cairn/internal/townie"
)

// fixtureModel builds a stack model with a known local tree + status, bypassing
// the live git reads in newStackModel so the flow is deterministic.
func fixtureModel() stackModel {
	th := theme.New(theme.DefaultPalette())
	s := newStackModelBare(th, "o/sandbox")
	// main → feat-base → feat-mid → feat-top, on feat-mid with staged changes.
	lin := stack.Lineage{
		Trunk: "main",
		Parents: map[string]string{
			"feat-base": "main", "feat-mid": "feat-base", "feat-top": "feat-mid",
		},
		Perennial: map[string]bool{},
	}
	s.tree = stack.BuildTree(lin, "feat-mid", func(_, _ string) bool { return false })
	s.status = stack.RepoStatus{InRepo: true, Branch: "feat-mid", HasUpstream: true, Staged: 2}
	s.treeCursor = s.tree.IndexOf("feat-mid") // mirror reload()'s "park on current"
	return s
}

func TestStackEnabledGate(t *testing.T) {
	s := fixtureModel()
	if !s.enabled() {
		t.Fatal("repo on a branch with git-town should be enabled")
	}
	s.status.Detached = true
	if s.enabled() {
		t.Error("detached HEAD must disable mutations")
	}
	s.status.Detached = false
	s.tree = nil
	if s.enabled() {
		t.Error("no git-town tree must disable mutations")
	}
}

func TestStackAmendGatedOnStaged(t *testing.T) {
	s := fixtureModel()
	amend := townie.Command{Verb: "amend"}
	if !s.actionEnabled(amend) {
		t.Error("amend should be enabled with staged changes")
	}
	s.status.Staged = 0
	if s.actionEnabled(amend) {
		t.Error("amend should be disabled with a clean tree")
	}
}

func TestMaintainVerbsGatedOnTrackedBranch(t *testing.T) {
	s := fixtureModel() // current = feat-mid, which IS in the tree
	maintain := []townie.Command{{Verb: "restack"}, {Verb: "sync"}}
	create := []townie.Command{{Verb: "new"}, {Verb: "insert"}}

	for _, c := range maintain {
		if !s.actionEnabled(c) {
			t.Errorf("%s should be enabled on a tracked branch", c.Verb)
		}
	}
	// Move onto a branch git-town doesn't track (not a tree node).
	s.status.Branch = "some-random-branch"
	for _, c := range maintain {
		if s.actionEnabled(c) {
			t.Errorf("%s must be disabled when current branch isn't in the tree", c.Verb)
		}
	}
	for _, c := range create {
		if !s.actionEnabled(c) {
			t.Errorf("%s should stay enabled off-stack (you can start a stack anywhere)", c.Verb)
		}
	}
	// amend also requires a tracked branch (and staged changes).
	s.status.Staged = 3
	if s.actionEnabled(townie.Command{Verb: "amend"}) {
		t.Error("amend must be disabled off-stack even with staged changes")
	}
}

func TestShipGatedToBottomBranch(t *testing.T) {
	s := fixtureModel() // current = feat-mid (parent feat-base) — not the bottom
	s.status.Staged = 0 // clean tree (fixture is dirty); merge needs a clean tree
	ship := townie.Command{Verb: "ship"}
	if s.actionEnabled(ship) {
		t.Error("ship must be disabled on a middle branch")
	}
	s.status.Branch = "feat-base" // direct child of trunk = bottom
	if !s.actionEnabled(ship) {
		t.Error("ship should be enabled on the clean bottom branch")
	}
	s.status.Branch = "main" // the trunk itself
	if s.actionEnabled(ship) {
		t.Error("ship must be disabled on the trunk")
	}
	s.status.Branch = "feat-base"
	s.status.Detached = true
	if s.actionEnabled(ship) {
		t.Error("ship must be disabled when detached")
	}
}

func TestShipGatedOnCleanTree(t *testing.T) {
	s := fixtureModel()
	s.status.Branch = "feat-base" // bottom of the stack
	s.status.Staged, s.status.Unstaged, s.status.Conflicts = 0, 0, 0
	if !s.actionEnabled(townie.Command{Verb: "ship"}) {
		t.Fatal("ship should be enabled on a clean bottom branch")
	}
	// Any uncommitted change to tracked files must block merge — its post-merge
	// stack rebase would derail on a dirty tree.
	for _, dirty := range []func(){
		func() { s.status.Unstaged = 3 },
		func() { s.status.Staged = 1 },
		func() { s.status.Conflicts = 1 },
	} {
		s.status.Staged, s.status.Unstaged, s.status.Conflicts = 0, 0, 0
		dirty()
		if s.actionEnabled(townie.Command{Verb: "ship"}) {
			t.Errorf("ship must be disabled on a dirty tree (%+v)", s.status)
		}
	}
	// Untracked files alone don't block merge (they don't interfere with a rebase).
	s.status.Staged, s.status.Unstaged, s.status.Conflicts, s.status.Untracked = 0, 0, 0, 5
	if !s.actionEnabled(townie.Command{Verb: "ship"}) {
		t.Error("untracked files alone should not block merge")
	}
}

func TestShipConfirmNamesMergeAndReParent(t *testing.T) {
	s := fixtureModel()
	s.status.Branch = "feat-base" // bottom
	got := s.affectedBranches(townie.Command{Verb: "ship"}, "")
	if strings.Join(got, ",") != "feat-mid,feat-top" {
		t.Errorf("ship affected = %v, want [feat-mid feat-top]", got)
	}
	s.pending = townie.Command{Verb: "ship", Title: "merge"}
	s.affected = got
	view := s.renderConfirm(80)
	for _, w := range []string{"Merges feat-base's PR", "gh: merge PR (squash)"} {
		if !strings.Contains(view, w) {
			t.Errorf("ship confirm missing %q\n%s", w, view)
		}
	}
}

func TestShipChainBottomUp(t *testing.T) {
	s := fixtureModel() // main → feat-base → feat-mid → feat-top
	cases := []struct {
		branch string
		want   string
	}{
		// "Whole stack" is the whole stack from ANY branch in it, bottom-up — not
		// bounded by where HEAD sits (a blocker still halts the run at merge time).
		{"feat-top", "feat-base,feat-mid,feat-top"},
		{"feat-mid", "feat-base,feat-mid,feat-top"},
		{"feat-base", "feat-base,feat-mid,feat-top"},
		{"main", ""},           // trunk has no chain
		{"unknown-branch", ""}, // off-stack
	}
	for _, tc := range cases {
		s.status.Branch = tc.branch
		if got := strings.Join(s.shipChain(), ","); got != tc.want {
			t.Errorf("shipChain(%s) = %q, want %q", tc.branch, got, tc.want)
		}
	}
}

func TestShipStackGating(t *testing.T) {
	s := fixtureModel()
	s.status.Staged = 0 // clean (fixture is dirty)
	stack := townie.Command{Verb: "shipstack"}
	if !s.actionEnabled(stack) {
		t.Error("shipstack should be enabled on a clean tracked non-trunk branch")
	}
	s.status.Branch = "main"
	if s.actionEnabled(stack) {
		t.Error("shipstack must be disabled on the trunk (no chain)")
	}
	s.status.Branch = "feat-mid"
	s.status.Unstaged = 2
	if s.actionEnabled(stack) {
		t.Error("shipstack must be disabled on a dirty tree")
	}
	s.status.Unstaged = 0
	// A blocked BOTTOM PR (the first to land) dims it; the chain bottom is feat-base.
	s.prMerge = map[string]gh.PRMergeability{"feat-base": {Number: 1, Mergeable: "CONFLICTING"}}
	if s.actionEnabled(stack) {
		t.Error("shipstack must be disabled when the bottom PR can't merge")
	}
	// A blocked HIGHER PR does NOT dim it — the run lands what it can and stops.
	// (The bottom must be a real, mergeable PR for the gate to open.)
	s.prMerge = map[string]gh.PRMergeability{
		"feat-base": {Number: 1, Mergeable: "MERGEABLE"},
		"feat-top":  {Number: 3, Draft: true},
	}
	if !s.actionEnabled(stack) {
		t.Error("shipstack should stay enabled when only a higher PR is blocked")
	}
	// Readiness loaded but the bottom has NO open PR → dim with a clear reason.
	s.prMerge = map[string]gh.PRMergeability{"feat-top": {Number: 3, Mergeable: "MERGEABLE"}}
	if s.actionEnabled(stack) {
		t.Error("shipstack must be disabled when the bottom branch has no open PR")
	}
}

func TestShipStackAffectedIsChain(t *testing.T) {
	s := fixtureModel()
	s.status.Branch = "feat-top"
	got := s.affectedBranches(townie.Command{Verb: "shipstack"}, "")
	if strings.Join(got, ",") != "feat-base,feat-mid,feat-top" {
		t.Errorf("shipstack affected = %v, want the bottom-up chain", got)
	}
}

func TestShipMergeabilityDimsAndExplains(t *testing.T) {
	s := fixtureModel()
	s.status.Branch = "feat-base" // bottom
	s.status.Staged = 0
	if !s.actionEnabled(townie.Command{Verb: "ship"}) {
		t.Fatal("ship should be enabled on a clean, unblocked bottom branch")
	}
	s.prMerge = map[string]gh.PRMergeability{"feat-base": {Number: 1, Draft: true, Mergeable: "MERGEABLE"}}
	if s.actionEnabled(townie.Command{Verb: "ship"}) {
		t.Error("ship must be disabled when the bottom PR is a draft")
	}
	// The action pane must explain WHY it is dim AND how to fix it.
	view := s.renderActions(100)
	for _, w := range []string{"feat-base", "can't be merged", "still a draft", "ready for review"} {
		if !strings.Contains(view, w) {
			t.Errorf("blocked-merge banner missing %q\n%s", w, view)
		}
	}
}

func TestShipStackConfirmWarnsAndListsBranches(t *testing.T) {
	s := fixtureModel()
	s.status.Branch = "feat-top"
	s.status.Staged = 0
	s.prNums = map[string]int{"feat-base": 10, "feat-mid": 11, "feat-top": 12}
	s.prMerge = map[string]gh.PRMergeability{"feat-mid": {Number: 11, Mergeable: "CONFLICTING"}}
	s.pending = *townie.Find("G") // the catalog entry (carries Long + Hint)
	s.affected = s.shipChain()
	view := s.renderShipStackConfirm(90)
	for _, w := range []string{
		"SERIOUS", "merge whole stack (3 branches)",
		"feat-base", "feat-mid", "feat-top",
		"#10", "conflicts with its base", "bottom-up",
		"merge 3 branches",
	} {
		if !strings.Contains(view, w) {
			t.Errorf("ship-stack confirm missing %q\n%s", w, view)
		}
	}
	// A CONFLICTING branch ABOVE the bottom is a risk, not a certainty (the run
	// rebases it onto trunk first), so it must read "may stop here", not "stops here".
	if !strings.Contains(view, "may stop here") {
		t.Errorf("non-bottom conflict should read \"may stop here\"\n%s", view)
	}
}

func TestShipStackDraftStopsHereButConflictMayStop(t *testing.T) {
	s := fixtureModel()
	s.status.Branch = "feat-top"
	s.status.Staged = 0
	s.prNums = map[string]int{"feat-base": 10, "feat-mid": 11, "feat-top": 12}
	// feat-mid is a DRAFT: a certain stop regardless of base.
	s.prMerge = map[string]gh.PRMergeability{"feat-mid": {Number: 11, Draft: true, Mergeable: "MERGEABLE"}}
	s.pending = *townie.Find("G")
	s.affected = s.shipChain()
	view := s.renderShipStackConfirm(90)
	if !strings.Contains(view, "still a draft — stops here") {
		t.Errorf("a draft must read a definite \"stops here\"\n%s", view)
	}
}

func TestShipStackTerminalStatusIsStoppedNotFailed(t *testing.T) {
	s := fixtureModel()
	s.phase = stackDone
	s.runErr = fmt.Errorf("mixed-3 can't merge: it is still a draft")
	// A whole-stack ship that halts is a controlled "stopped", not a red "failed".
	s.pending = townie.Command{Verb: "shipstack", Title: "merge whole stack"}
	if out := s.renderOutput(80); !strings.Contains(out, "stopped:") || strings.Contains(out, "failed:") {
		t.Errorf("shipstack halt must read \"stopped:\" not \"failed:\"\n%s", out)
	}
	// A single ship that fails is a genuine "failed".
	s.pending = townie.Command{Verb: "ship", Title: "merge"}
	if out := s.renderOutput(80); !strings.Contains(out, "failed:") {
		t.Errorf("single ship failure should still read \"failed:\"\n%s", out)
	}
}

func TestShipStackConfirmAnnotatesCautionAndNoPR(t *testing.T) {
	s := fixtureModel()
	s.status.Branch = "feat-top"
	s.status.Staged = 0
	// feat-base ready; feat-mid needs review (caution, not a hard block); feat-top
	// has no open PR yet. prMerge is non-nil, so an absent branch = "no open PR".
	s.prNums = map[string]int{"feat-base": 10, "feat-mid": 11}
	s.prMerge = map[string]gh.PRMergeability{
		"feat-base": {Number: 10, Mergeable: "MERGEABLE"},
		"feat-mid":  {Number: 11, Mergeable: "MERGEABLE", ReviewDecision: "CHANGES_REQUESTED"},
	}
	s.pending = *townie.Find("G")
	s.affected = s.shipChain()
	view := s.renderShipStackConfirm(100)
	for _, w := range []string{"changes were requested", "no open PR"} {
		if !strings.Contains(view, w) {
			t.Errorf("confirm annotation missing %q\n%s", w, view)
		}
	}
}

func TestShipStackConfirmEnterRuns(t *testing.T) {
	s := fixtureModel()
	s.status.Branch = "feat-top"
	s.status.Staged = 0
	s.phase = stackConfirming
	s.pending = *townie.Find("G")
	s.affected = s.shipChain()
	s2, cmd := s.updateConfirming(tea.KeyMsg{Type: tea.KeyEnter})
	if s2.phase != stackRunning || cmd == nil {
		t.Fatalf("enter on the ship-stack confirm should run: phase=%d cmd=%v", s2.phase, cmd != nil)
	}
}

func TestStackAffectedBranches(t *testing.T) {
	s := fixtureModel()
	// amend on feat-mid rebases feat-top (its only descendant).
	if got := s.affectedBranches(townie.Command{Verb: "amend"}, ""); strings.Join(got, ",") != "feat-top" {
		t.Errorf("amend affected = %v, want [feat-top]", got)
	}
	// sync rebases the whole stack (trunk excluded).
	got := s.affectedBranches(townie.Command{Verb: "sync"}, "")
	if strings.Join(got, ",") != "feat-base,feat-mid,feat-top" {
		t.Errorf("sync affected = %v, want all three", got)
	}
	// new (a leaf) rebases nothing.
	if got := s.affectedBranches(townie.Command{Verb: "new"}, "feat-new"); len(got) != 0 {
		t.Errorf("new affected = %v, want none", got)
	}
}

func TestStackConfirmShowsAffectedAndCommand(t *testing.T) {
	s := fixtureModel()
	// Select amend (index of the amend command in the catalog).
	for i, c := range s.commands {
		if c.Verb == "amend" {
			s.cursor = i
		}
	}
	s2, _ := s.updateBrowsing(tea.KeyMsg{Type: tea.KeyEnter})
	if s2.phase != stackConfirming {
		t.Fatalf("amend has no name prompt → should go straight to confirming, got phase %d", s2.phase)
	}
	view := s2.renderConfirm(60)
	for _, w := range []string{"feat-top", "git commit --amend", "do it"} {
		if !strings.Contains(view, w) {
			t.Errorf("confirm view missing %q\n%s", w, view)
		}
	}
}

func TestStackNewPromptsForName(t *testing.T) {
	s := fixtureModel()
	for i, c := range s.commands {
		if c.Verb == "new" {
			s.cursor = i
		}
	}
	s2, _ := s.updateBrowsing(tea.KeyMsg{Type: tea.KeyEnter})
	if s2.phase != stackNaming {
		t.Fatalf("new needs a name → should enter naming phase, got %d", s2.phase)
	}
	// Empty name is rejected.
	s3, _ := s2.updateNaming(tea.KeyMsg{Type: tea.KeyEnter})
	if s3.phase != stackNaming {
		t.Error("empty name should keep us in the naming phase")
	}
}

func TestStackCommandKeyAccelerators(t *testing.T) {
	key := func(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

	// 'n' (new) needs a name → naming phase. Accelerators fire from the ACTION pane
	// (the tree pane is inert — see TestTreeFocusBlocksActionAccelerators).
	s := fixtureModel()
	s.focus = focusActions
	if s2, _ := s.updateBrowsing(key("n")); s2.phase != stackNaming {
		t.Errorf("'n' should start new (naming), got phase %d", s2.phase)
	}
	// 'S' (sync) on a tracked branch → straight to confirming.
	if s2, _ := s.updateBrowsing(key("S")); s2.phase != stackConfirming {
		t.Errorf("'S' should start sync (confirming), got phase %d", s2.phase)
	}
	// 'A' (amend) is gated on staged changes — inert when clean.
	clean := fixtureModel()
	clean.status.Staged = 0
	if s2, _ := clean.updateBrowsing(key("A")); s2.phase != stackBrowsing {
		t.Errorf("'A' must be inert with a clean index, got phase %d", s2.phase)
	}
	// Command keys stay inert before git-town is set up.
	noInit := fixtureModel()
	noInit.tree = nil
	noInit.status = stack.RepoStatus{InRepo: true} // needsInit
	if s2, _ := noInit.updateBrowsing(key("n")); s2.phase != stackBrowsing {
		t.Errorf("command keys must be inert when git-town isn't initialized, got phase %d", s2.phase)
	}
}

func TestWrapBlockSanitizesCRAndOverflow(t *testing.T) {
	// git rebase prints "Successfully…" with a leading \r to clear progress; it
	// must not survive into the rendered output (would jump to column 0).
	out := wrapBlock("Rebasing (1/3)\rSuccessfully rebased and updated refs/heads/feat-base.", 80)
	if strings.Contains(out, "\r") {
		t.Errorf("carriage returns must be stripped, got %q", out)
	}
	if strings.Contains(out, "Rebasing (1/3)") {
		t.Errorf("progress before the last \\r should be dropped, got %q", out)
	}
	if !strings.HasPrefix(out, "Successfully rebased") {
		t.Errorf("want the post-\\r text kept, got %q", out)
	}
	// A line longer than the width is hard-wrapped so nothing exceeds it.
	long := "git -c rebase.updateRefs=false rebase --onto feat-base 7e9f6dc4ba5e1044f135d9ae8cd797153b221131"
	for _, ln := range strings.Split(wrapBlock(long, 40), "\n") {
		if len(ln) > 40 {
			t.Errorf("wrapped line exceeds width: %q (%d)", ln, len(ln))
		}
	}
}

func TestStackRunTransitionsAndReloads(t *testing.T) {
	s := fixtureModel()
	s.phase = stackConfirming
	s.pending = townie.Command{Verb: "amend", Title: "amend"}
	s2, cmd := s.updateConfirming(tea.KeyMsg{Type: tea.KeyEnter})
	if s2.phase != stackRunning || cmd == nil {
		t.Fatalf("enter on confirm should run: phase=%d cmd=%v", s2.phase, cmd != nil)
	}
	// Stream a line, then completion; output accumulates and we land in Done.
	s3, _ := s2.Update(stackStreamMsg{ev: townie.StreamEvent{Line: "restacked feat-top"}})
	if s3.phase != stackRunning || !strings.Contains(s3.output, "restacked feat-top") {
		t.Errorf("after line: phase=%d output=%q", s3.phase, s3.output)
	}
	s4, _ := s3.Update(stackStreamMsg{ev: townie.StreamEvent{Done: true}})
	if s4.phase != stackDone {
		t.Errorf("after done: phase=%d", s4.phase)
	}
}

// fakeRunner records the commands a stack op delegates, for asserting the init
// flow without touching real git.
type fakeRunner struct{ calls [][]string }

func (f *fakeRunner) Run(_, name string, args ...string) (string, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	return "configured", nil
}

func TestInitCTAAndFlow(t *testing.T) {
	th := theme.New(theme.DefaultPalette())
	s := newStackModelBare(th, "o/sandbox")
	s.status = stack.RepoStatus{InRepo: true} // in a repo…
	s.tree = nil                              // …but no git-town config
	s.trunk = "main"                          // detected trunk for the prompt
	s.focus = focusTree                       // real entry (newStackModel) focuses the tree
	fr := &fakeRunner{}
	s.ops = townie.Ops{Runner: fr}

	if !s.needsInit() {
		t.Fatal("InRepo + no git-town should need init")
	}
	if v := s.renderInitCTA(60); !strings.Contains(v, "Set up git-town") || !strings.Contains(v, "main") {
		t.Errorf("init CTA should show the title and detected trunk:\n%s", v)
	}

	// enter starts the guided setup, pre-filled with the detected trunk.
	s2, _ := s.updateBrowsing(tea.KeyMsg{Type: tea.KeyEnter})
	if s2.phase != stackNaming || s2.pending.Verb != "init" {
		t.Fatalf("enter should start init naming; phase=%d verb=%q", s2.phase, s2.pending.Verb)
	}
	if s2.name.Value() != "main" {
		t.Errorf("init prompt should pre-fill trunk 'main', got %q", s2.name.Value())
	}

	// Naming → confirmation, which shows the two config commands.
	s3, _ := s2.updateNaming(tea.KeyMsg{Type: tea.KeyEnter})
	if s3.phase != stackConfirming {
		t.Fatalf("naming enter → confirming, got phase %d", s3.phase)
	}
	cv := s3.renderConfirm(72)
	for _, w := range []string{"git config git-town.main-branch main", "rebase", "do it"} {
		if !strings.Contains(cv, w) {
			t.Errorf("init confirm missing %q:\n%s", w, cv)
		}
	}

	// Confirm → run; the closure delegates the two git config writes in order.
	s4, cmd := s3.updateConfirming(tea.KeyMsg{Type: tea.KeyEnter})
	if s4.phase != stackRunning || cmd == nil {
		t.Fatalf("confirm enter → running, got phase %d cmd=%v", s4.phase, cmd != nil)
	}
	s4.Update(cmd()) // executes the op; fr records the calls
	if len(fr.calls) != 2 {
		t.Fatalf("init should run 2 git config commands, ran %d: %v", len(fr.calls), fr.calls)
	}
	if got := strings.Join(fr.calls[0], " "); got != "git config git-town.main-branch main" {
		t.Errorf("step 1 = %q", got)
	}
	if got := strings.Join(fr.calls[1], " "); got != "git config git-town.sync-feature-strategy rebase" {
		t.Errorf("step 2 = %q", got)
	}
}

func TestStackTabFocusesTreeAndCheckout(t *testing.T) {
	s := fixtureModel() // current = feat-mid
	// Tab moves focus to the tree.
	s2, _ := s.updateBrowsing(tea.KeyMsg{Type: tea.KeyTab})
	if s2.focus != focusTree {
		t.Fatalf("tab should focus the tree, got %d", s2.focus)
	}
	// Cursor starts on the current branch; move it down to a different branch.
	if s2.tree.Order[s2.treeCursor].Name != "feat-mid" {
		t.Fatalf("tree cursor should start on current branch, got %s", s2.tree.Order[s2.treeCursor].Name)
	}
	s3, _ := s2.updateTree(tea.KeyMsg{Type: tea.KeyDown}) // → feat-top
	target := s3.tree.Order[s3.treeCursor].Name
	s4, cmd := s3.updateTree(tea.KeyMsg{Type: tea.KeyEnter})
	if s4.phase != stackRunning || cmd == nil {
		t.Fatalf("enter on a different branch should run checkout: phase=%d", s4.phase)
	}
	if s4.pending.Verb != "checkout" || s4.opName != target {
		t.Errorf("checkout op = %q %q, want checkout %q", s4.pending.Verb, s4.opName, target)
	}
}

func TestStackCheckoutCurrentBranchIsNoop(t *testing.T) {
	s := fixtureModel()
	s.focus = focusTree
	// Cursor is on the current branch (feat-mid) → enter does nothing.
	_, cmd := s.updateTree(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Error("checking out the branch you're already on should be a no-op")
	}
}

func TestStackExitFromBrowsing(t *testing.T) {
	s := fixtureModel()
	_, cmd := s.updateBrowsing(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Fatal("esc should emit an exit command")
	}
	if _, ok := cmd().(stackExitMsg); !ok {
		t.Error("esc should emit stackExitMsg")
	}
}

func TestProposeGating(t *testing.T) {
	s := fixtureModel() // cursor on feat-mid (non-trunk, tracked)
	propose := *townie.Find("p")
	if propose.Verb != "propose" {
		t.Fatalf("Find(p) = %q, want propose", propose.Verb)
	}
	if !s.actionEnabled(propose) {
		t.Error("propose should be enabled on a tracked non-trunk branch with no PR")
	}
	// A branch that already has an open PR can't be proposed again.
	s.prNums = map[string]int{"feat-mid": 42}
	if s.actionEnabled(propose) {
		t.Error("propose must be disabled when the branch already has a PR")
	}
	// The trunk has no parent to target and can't be proposed.
	s.prNums = nil
	s.treeCursor = s.tree.IndexOf("main")
	if s.actionEnabled(propose) {
		t.Error("propose must be disabled on the trunk")
	}
}

func TestProposeTargetAndBaseFollowTreeCursor(t *testing.T) {
	s := fixtureModel()
	for _, tc := range []struct{ branch, base string }{
		{"feat-base", "main"},     // bottom → trunk
		{"feat-mid", "feat-base"}, // middle → branch below
		{"feat-top", "feat-mid"},
	} {
		s.treeCursor = s.tree.IndexOf(tc.branch)
		if got := s.proposeTarget(); got != tc.branch {
			t.Errorf("proposeTarget = %q, want %q", got, tc.branch)
		}
		if got := s.proposeBaseFor(tc.branch); got != tc.base {
			t.Errorf("base for %s = %q, want %q (its parent in the stack)", tc.branch, got, tc.base)
		}
	}
}

func TestStartProposeOpensComposerWithDetectedBase(t *testing.T) {
	s := fixtureModel()
	s.treeCursor = s.tree.IndexOf("feat-mid")
	s, _ = s.triggerAction(*townie.Find("p"))
	if s.phase != stackComposing {
		t.Fatalf("phase = %v, want stackComposing", s.phase)
	}
	if s.opName != "feat-mid" {
		t.Errorf("opName = %q, want feat-mid", s.opName)
	}
	if s.proposeBase != "feat-base" {
		t.Errorf("proposeBase = %q, want feat-base (the parent)", s.proposeBase)
	}
	if s.composeFocus != composeTitle {
		t.Error("composer should start focused on the title field")
	}
}

func TestComposeRequiresTitleBeforeConfirm(t *testing.T) {
	s := fixtureModel()
	s, _ = s.triggerAction(*townie.Find("p"))
	s.titleInput.SetValue("") // blank title
	s, _ = s.updateComposing(tea.KeyMsg{Type: tea.KeyCtrlS})
	if s.phase != stackComposing {
		t.Error("ctrl+s with an empty title must stay in the composer")
	}
	s.titleInput.SetValue("Add retry logic")
	s, _ = s.updateComposing(tea.KeyMsg{Type: tea.KeyCtrlS})
	if s.phase != stackConfirming {
		t.Errorf("ctrl+s with a title should advance to confirm, got %v", s.phase)
	}
}

func TestComposeTabSwitchesFieldAndEscCancels(t *testing.T) {
	s := fixtureModel()
	s, _ = s.triggerAction(*townie.Find("p"))
	s, _ = s.updateComposing(tea.KeyMsg{Type: tea.KeyTab})
	if s.composeFocus != composeBody {
		t.Error("tab should move focus to the body")
	}
	s, _ = s.updateComposing(tea.KeyMsg{Type: tea.KeyEsc})
	if s.phase != stackBrowsing {
		t.Error("esc should cancel the composer back to browsing")
	}
}

func TestProposeConfirmTogglesDraft(t *testing.T) {
	s := fixtureModel()
	s, _ = s.triggerAction(*townie.Find("p"))
	s.titleInput.SetValue("My PR")
	s, _ = s.updateComposing(tea.KeyMsg{Type: tea.KeyCtrlS})
	if s.proposeDraft {
		t.Fatal("draft should default to off")
	}
	s, _ = s.updateConfirming(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	if !s.proposeDraft {
		t.Error("d should toggle draft on")
	}
	// esc from the propose confirm returns to the editor, not all the way out.
	s, _ = s.updateConfirming(tea.KeyMsg{Type: tea.KeyEsc})
	if s.phase != stackComposing {
		t.Error("esc from the propose confirm should return to the composer")
	}
}

func TestTreeShowsPRNumberFlag(t *testing.T) {
	s := fixtureModel()
	s.prNums = map[string]int{"feat-base": 12}
	out := s.renderLocalTree(40)
	if !strings.Contains(out, "#12") {
		t.Errorf("tree should flag feat-base with its PR number; got:\n%s", out)
	}
}

func TestActionDisabledReasonsExplain(t *testing.T) {
	s := fixtureModel() // current = feat-mid (middle), dirty (Staged 2)
	s.status.Staged = 0 // clean, so the reason isn't "clean tree"

	// M on a middle branch: names the only branch that can merge (the bottom).
	if r := s.actionDisabledReason(townie.Command{Verb: "ship"}); !strings.Contains(r, "bottom branch (feat-base)") {
		t.Errorf("ship reason on a middle branch should name the bottom; got %q", r)
	}
	// A dirty tree changes the bottom-branch reason to the clean-tree one.
	s.status.Branch = "feat-base"
	s.status.Unstaged = 3
	if r := s.actionDisabledReason(townie.Command{Verb: "ship"}); !strings.Contains(r, "clean working tree") {
		t.Errorf("ship reason on a dirty bottom should mention a clean tree; got %q", r)
	}
	s.status.Unstaged = 0

	// A blocked bottom PR reports the blocker + fix.
	s.prMerge = map[string]gh.PRMergeability{"feat-base": {Number: 1, Draft: true}}
	if r := s.actionDisabledReason(townie.Command{Verb: "ship"}); !strings.Contains(r, "draft") {
		t.Errorf("ship reason on a draft bottom should say draft; got %q", r)
	}

	// Maintenance verbs off-stack.
	s.status.Branch = "off-tree"
	s.prMerge = nil
	for _, v := range []string{"sync", "restack", "amend"} {
		if r := s.actionDisabledReason(townie.Command{Verb: v}); !strings.Contains(r, "isn't in a stack") {
			t.Errorf("%s reason off-stack should say isn't in a stack; got %q", v, r)
		}
	}
}

func TestShipStopsListsAllCertainStops(t *testing.T) {
	s := fixtureModel()
	s.status.Branch = "feat-top" // whole chain: feat-base, feat-mid, feat-top
	s.prNums = map[string]int{"feat-base": 10, "feat-mid": 11}
	// base mergeable, mid is a draft (certain stop), top has no open PR (certain stop).
	s.prMerge = map[string]gh.PRMergeability{
		"feat-base": {Number: 10, Mergeable: "MERGEABLE"},
		"feat-mid":  {Number: 11, Draft: true},
	} // feat-top absent => no open PR
	landsFirst, stops := s.shipStops()
	if strings.Join(landsFirst, ",") != "feat-base" {
		t.Errorf("landsFirst = %v, want [feat-base] (only what lands before the first stop)", landsFirst)
	}
	if len(stops) != 2 {
		t.Fatalf("want 2 certain stops, got %d: %+v", len(stops), stops)
	}
	if stops[0].branch != "feat-mid" || !strings.Contains(stops[0].reason, "draft") || !strings.Contains(stops[0].fixAction, "Y") {
		t.Errorf("first stop = %+v, want feat-mid draft / Y", stops[0])
	}
	if stops[1].branch != "feat-top" || !strings.Contains(stops[1].reason, "no open PR") || !strings.Contains(stops[1].fixAction, "p") {
		t.Errorf("second stop = %+v, want feat-top no-PR / p", stops[1])
	}
	// All clear => no stops.
	s.prMerge = map[string]gh.PRMergeability{
		"feat-base": {Number: 10, Mergeable: "MERGEABLE"},
		"feat-mid":  {Number: 11, Mergeable: "MERGEABLE"},
		"feat-top":  {Number: 12, Mergeable: "MERGEABLE"},
	}
	s.prNums["feat-top"] = 12
	if _, stops := s.shipStops(); len(stops) != 0 {
		t.Errorf("a clean chain should have no stops, got %+v", stops)
	}
}

func TestStackActionsBannerWarnsWhereGWillStop(t *testing.T) {
	s := fixtureModel()
	s.status.Branch = "feat-top"
	s.status.Staged = 0 // clean, so G is enabled
	s.prNums = map[string]int{"feat-base": 10, "feat-mid": 11, "feat-top": 12}
	s.prMerge = map[string]gh.PRMergeability{
		"feat-base": {Number: 10, Mergeable: "MERGEABLE"},
		"feat-mid":  {Number: 11, Draft: true},
		"feat-top":  {Number: 12, Mergeable: "MERGEABLE"},
	}
	if !s.actionEnabled(townie.Command{Verb: "shipstack"}) {
		t.Fatal("G should be enabled: the bottom is mergeable")
	}
	out := s.renderActions(100)
	for _, want := range []string{"will stop at feat-mid", "Lands feat-base first"} {
		if !strings.Contains(out, want) {
			t.Errorf("G stop banner missing %q\n%s", want, out)
		}
	}
}

func TestLooseBranchesShowInTreeAndNav(t *testing.T) {
	s := fixtureModel()
	s.loose = []string{"loose-branch"}
	// navBranches appends loose after the stack nodes.
	nav := s.navBranches()
	if len(nav) != len(s.tree.Order)+1 || nav[len(nav)-1] != "loose-branch" {
		t.Errorf("navBranches should end with loose-branch; got %v", nav)
	}
	out := s.renderLocalTree(40)
	if !strings.Contains(out, "not in a stack") || !strings.Contains(out, "loose-branch") {
		t.Errorf("tree should show the not-in-a-stack section with loose-branch; got:\n%s", out)
	}
	// The cursor can land on the loose branch, and enter checks it out.
	s.focus = focusTree
	s.treeCursor = len(s.tree.Order) // first loose entry
	s2, cmd := s.updateTree(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil || s2.opName != "loose-branch" {
		t.Errorf("enter on a loose branch should check it out; opName=%q", s2.opName)
	}
}

func TestTreeShowsDraftAndNoPRBadges(t *testing.T) {
	s := fixtureModel()
	s.prNums = map[string]int{"feat-base": 10, "feat-mid": 11} // feat-top has no open PR
	s.prMerge = map[string]gh.PRMergeability{
		"feat-base": {Number: 10, Mergeable: "MERGEABLE"},
		"feat-mid":  {Number: 11, Draft: true},
	}
	out := s.renderLocalTree(50)
	if !strings.Contains(out, "draft") {
		t.Errorf("tree should badge the draft PR; got:\n%s", out)
	}
	if !strings.Contains(out, "no PR") {
		t.Errorf("tree should badge the branch with no open PR; got:\n%s", out)
	}
}

func TestReadyGatedToDraftUnderCursor(t *testing.T) {
	s := fixtureModel() // main→feat-base→feat-mid→feat-top
	ready := townie.Command{Verb: "ready"}
	s.prNums = map[string]int{"feat-base": 10, "feat-mid": 11}
	s.prMerge = map[string]gh.PRMergeability{
		"feat-base": {Number: 10, Mergeable: "MERGEABLE"},
		"feat-mid":  {Number: 11, Draft: true},
	}
	// Cursor on the draft PR → enabled.
	s.treeCursor = s.tree.IndexOf("feat-mid")
	if !s.actionEnabled(ready) {
		t.Error("ready should be enabled when the cursor is on a draft PR")
	}
	// Cursor on a non-draft PR → disabled, with a reason.
	s.treeCursor = s.tree.IndexOf("feat-base")
	if s.actionEnabled(ready) {
		t.Error("ready must be disabled on a non-draft PR")
	}
	if r := s.actionDisabledReason(ready); !strings.Contains(r, "isn't a draft") {
		t.Errorf("reason should say not a draft; got %q", r)
	}
	// Cursor on a branch with no PR → reason says so.
	s.treeCursor = s.tree.IndexOf("feat-top")
	if r := s.actionDisabledReason(ready); !strings.Contains(r, "no open PR") {
		t.Errorf("reason should say no open PR; got %q", r)
	}
	// triggerAction on the draft opens a confirm targeting that branch; enter runs it.
	s.treeCursor = s.tree.IndexOf("feat-mid")
	s2, _ := s.triggerAction(townie.Command{Verb: "ready", Title: "mark ready"})
	if s2.phase != stackConfirming || s2.opName != "feat-mid" {
		t.Fatalf("ready should confirm on feat-mid; phase=%v opName=%q", s2.phase, s2.opName)
	}
	if !strings.Contains(s2.renderConfirm(80), "mark feat-mid ready") {
		t.Errorf("confirm should name the branch\n%s", s2.renderConfirm(80))
	}
	s3, cmd := s2.updateConfirming(tea.KeyMsg{Type: tea.KeyEnter})
	if s3.phase != stackRunning || cmd == nil {
		t.Errorf("enter should start the ready op; phase=%v", s3.phase)
	}
}

func TestGStopBannerPointsAtReady(t *testing.T) {
	s := fixtureModel()
	s.status.Branch = "feat-top"
	s.status.Staged = 0
	s.prNums = map[string]int{"feat-base": 10, "feat-mid": 11, "feat-top": 12}
	s.prMerge = map[string]gh.PRMergeability{
		"feat-base": {Number: 10, Mergeable: "MERGEABLE"},
		"feat-mid":  {Number: 11, Draft: true},
		"feat-top":  {Number: 12, Mergeable: "MERGEABLE"},
	}
	if out := s.renderActions(110); !strings.Contains(out, "press Y") {
		t.Errorf("G stop banner should point at Y for a draft\n%s", out)
	}
}

func TestGStopBannerBulletsMultipleFixes(t *testing.T) {
	s := fixtureModel()
	s.status.Branch = "feat-top"
	s.status.Staged = 0
	// feat-base mergeable, feat-mid draft, feat-top no open PR => TWO fixes.
	s.prNums = map[string]int{"feat-base": 10, "feat-mid": 11}
	s.prMerge = map[string]gh.PRMergeability{
		"feat-base": {Number: 10, Mergeable: "MERGEABLE"},
		"feat-mid":  {Number: 11, Draft: true},
	}
	out := s.renderActions(110)
	for _, want := range []string{
		"•",               // rendered as a bulleted checklist
		"press Y",         // fix for the draft (feat-mid)
		"press p",         // fix for the no-PR branch (feat-top)
		"feat-top",        // the no-PR branch is now named in the banner, not just the row
		"lands feat-base", // what still ships before the first stop
	} {
		if !strings.Contains(out, want) {
			t.Errorf("multi-fix G banner missing %q\n%s", want, out)
		}
	}
}

func TestDriftBannerNamesBranchAndFix(t *testing.T) {
	s := fixtureModel()
	// Rebuild the tree so feat-mid has drifted off its parent.
	lin := stack.Lineage{Trunk: "main", Parents: map[string]string{
		"feat-base": "main", "feat-mid": "feat-base", "feat-top": "feat-mid",
	}, Perennial: map[string]bool{}}
	s.tree = stack.BuildTree(lin, "feat-mid", func(b, _ string) bool { return b == "feat-mid" })
	s.status = stack.RepoStatus{InRepo: true, Branch: "feat-mid"}

	if got := s.driftedInCurrentStack(); len(got) != 1 || got[0] != "feat-mid" {
		t.Fatalf("driftedInCurrentStack = %v, want [feat-mid]", got)
	}
	out := s.renderActions(90)
	for _, w := range []string{"feat-mid has drifted", "press R to restack"} {
		if !strings.Contains(out, w) {
			t.Errorf("drift banner missing %q\n%s", w, out)
		}
	}

	// Drift in an UNRELATED stack under the same trunk must not warn here.
	lin2 := stack.Lineage{Trunk: "main", Parents: map[string]string{
		"green-1": "main", "cf-1": "main", "cf-2": "cf-1",
	}, Perennial: map[string]bool{}}
	s.tree = stack.BuildTree(lin2, "green-1", func(b, _ string) bool { return b == "cf-2" })
	s.status.Branch = "green-1"
	if got := s.driftedInCurrentStack(); len(got) != 0 {
		t.Errorf("drift in an unrelated stack should not surface; got %v", got)
	}
}

func TestColorBranchesHighlightsKnownBranchesOnly(t *testing.T) {
	th := theme.New(theme.DefaultPalette())
	base := lipgloss.NewStyle().Foreground(th.Warning)
	out := colorBranches(base, th, "merge feat-base into main now", []string{"feat-base", "main"})
	// No characters added or dropped — only styling changes.
	if got := ansi.Strip(out); got != "merge feat-base into main now" {
		t.Errorf("plain text changed: %q", got)
	}
	// Branch names carry the branch color (Accent2); surrounding text stays base.
	wantBranch := lipgloss.NewStyle().Foreground(th.Accent2).Render("feat-base")
	if !strings.Contains(out, wantBranch) {
		t.Errorf("feat-base not rendered in the branch color\n%q", out)
	}
	if !strings.Contains(out, lipgloss.NewStyle().Foreground(th.Accent2).Render("main")) {
		t.Errorf("main not rendered in the branch color\n%q", out)
	}
	if !strings.Contains(out, base.Render("merge ")) {
		t.Errorf("non-branch text should keep the base style\n%q", out)
	}
	// A word that isn't a known branch is never colored as one.
	plain := colorBranches(base, th, "nothing staged here", []string{"feat-base"})
	if plain != base.Render("nothing staged here") {
		t.Errorf("text with no branch should be a single base render\n%q", plain)
	}

	// Plain text is preserved even with a substring collision ("main" in "remains").
	if got := ansi.Strip(colorBranches(base, th, "what remains re-parents onto main", []string{"main"})); got != "what remains re-parents onto main" {
		t.Errorf("plain text changed: %q", got)
	}
}

func TestLogBranchNamesIncludesOpBranches(t *testing.T) {
	s := fixtureModel()
	// Simulate a ship whose op branches (affected/opName) may already be gone from
	// the reloaded tree — they must still be colorable in the run log.
	s.affected = []string{"feat-base", "shipped-and-gone"}
	s.opName = "another-gone"
	got := map[string]bool{}
	for _, n := range s.logBranchNames() {
		got[n] = true
	}
	for _, want := range []string{"feat-mid", "feat-base", "shipped-and-gone", "another-gone"} {
		if !got[want] {
			t.Errorf("logBranchNames missing %q (%v)", want, s.logBranchNames())
		}
	}
}

func TestBranchNameAtRespectsWordBoundaries(t *testing.T) {
	names := []string{"feat-base", "main"} // longest-first, as colorBranches sorts
	cases := []struct {
		line string
		pos  int
		want string
	}{
		{"onto main now", 5, "main"},              // standalone → match
		{"what remains onto x", 7, ""},            // "main" inside "remains" → no match
		{"rebase feat-base onto", 7, "feat-base"}, // standalone hyphenated → match
		{"feat-base-x drift", 0, ""},              // longer word "feat-base-x" → no match
		{"main", 0, "main"},                       // whole string → match
		{"domain", 2, ""},                         // "main" inside "domain" → no match
	}
	for _, tc := range cases {
		if got := branchNameAt(tc.line, tc.pos, names); got != tc.want {
			t.Errorf("branchNameAt(%q,%d) = %q, want %q", tc.line, tc.pos, got, tc.want)
		}
	}
}

func TestTreeWidthFitsLongBranchAndClamps(t *testing.T) {
	s := fixtureModel()
	s.width = 200 // plenty of room
	if w := s.treeWidth(); w != stackPaneW {
		t.Errorf("short branch names should give the floor %d, got %d", stackPaneW, w)
	}
	// A long branch name grows the pane past the floor.
	lin := stack.Lineage{
		Trunk:     "main",
		Parents:   map[string]string{"feat/session-auth-consolidation-rollout": "main"},
		Perennial: map[string]bool{},
	}
	s.tree = stack.BuildTree(lin, "main", func(_, _ string) bool { return false })
	if w := s.treeWidth(); w <= stackPaneW {
		t.Errorf("a long branch should widen the pane beyond %d, got %d", stackPaneW, w)
	}
	// On a tiny terminal it can't exceed the floor (the View clamps the right pane).
	s.width = 30
	if w := s.treeWidth(); w != stackPaneW {
		t.Errorf("tiny terminal should clamp to the floor %d, got %d", stackPaneW, w)
	}
}

func TestCurrentUntrackedAndTrack(t *testing.T) {
	s := fixtureModel() // current = feat-mid, which IS tracked
	if s.currentUntracked() {
		t.Error("a tracked branch should not be flagged untracked")
	}
	// Hop onto a branch git-town doesn't know.
	s.status.Branch = "ui-polish"
	if !s.currentUntracked() {
		t.Error("an off-tree branch in a git-town repo should be flagged untracked")
	}
	if s.trackParent() != "main" {
		t.Errorf("trackParent = %q, want the trunk main", s.trackParent())
	}
	// The action list surfaces the peach call-to-action.
	if out := s.renderActions(70); !strings.Contains(out, "ui-polish isn't in a stack") {
		t.Errorf("expected the untracked banner; got:\n%s", out)
	}
	// t starts the track op (running phase + a command).
	s2, cmd := s.updateBrowsing(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	if s2.phase != stackRunning {
		t.Errorf("t should start the track op (running phase), got %v", s2.phase)
	}
	if cmd == nil {
		t.Error("t should return a command to run the track op")
	}
	// On a tracked branch, t is inert.
	s.status.Branch = "feat-mid"
	if _, cmd := s.updateBrowsing(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")}); cmd != nil {
		t.Error("t must be a no-op on a tracked branch")
	}
}

// TestTreeFocusBlocksActionAccelerators: while the branch tree is focused (the
// default on entry), the letter accelerators must NOT fire — so navigating can't
// accidentally trigger a destructive action. You tab to the action list first.
func TestTreeFocusBlocksActionAccelerators(t *testing.T) {
	keys := []string{"n", "p", "I", "S", "R", "A", "M", "G", "Y"}
	for _, k := range keys {
		s := fixtureModel()
		s.status.Staged = 0
		s.focus = focusTree
		s2, _ := s.updateBrowsing(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
		if s2.phase != stackBrowsing {
			t.Errorf("key %q while the tree is focused fired something (phase=%d) — must be inert", k, s2.phase)
		}
	}
	// Sanity: with the ACTION LIST focused, an accelerator still acts (n -> naming).
	s := fixtureModel()
	s.focus = focusActions
	if s2, _ := s.updateBrowsing(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")}); s2.phase != stackNaming {
		t.Errorf("n with the action list focused should start naming, got phase %d", s2.phase)
	}
}

// --- Component B: remote drift detection + reconcile ---

func TestRemoteDriftSplitsMergedAndClosed(t *testing.T) {
	s := fixtureModel()
	s.drift = map[string]gh.PRLanding{
		"feat-base": {Number: 12, Merged: true},
		"feat-mid":  {Number: 14, Merged: false}, // closed without merging
	}
	merged, closed := s.remoteDrift()
	if len(merged) != 1 || merged[0] != "feat-base" {
		t.Errorf("merged = %v, want [feat-base]", merged)
	}
	if len(closed) != 1 || closed[0] != "feat-mid" {
		t.Errorf("closed = %v, want [feat-mid]", closed)
	}
	if !s.hasRemoteDrift() {
		t.Error("hasRemoteDrift should be true when a branch drifted")
	}
	// The trunk never counts as drift, and a branch not in the tree is ignored.
	s.drift = map[string]gh.PRLanding{"main": {Number: 1, Merged: true}, "ghost": {Number: 2, Merged: true}}
	if s.hasRemoteDrift() {
		t.Error("trunk and unknown branches must not register as drift")
	}
}

func TestDriftMsgSetsAndClears(t *testing.T) {
	s := fixtureModel()
	s2, _ := s.Update(stackDriftMsg{drift: map[string]gh.PRLanding{"feat-base": {Number: 12, Merged: true}}})
	if !s2.hasRemoteDrift() {
		t.Error("a drift msg should populate the drift map")
	}
	// An empty refetch (post-reconcile) clears the stale warning.
	s3, _ := s2.Update(stackDriftMsg{})
	if s3.hasRemoteDrift() {
		t.Error("an empty drift msg should clear the drift map")
	}
}

func TestDriftBannerNamesBranchAndPR(t *testing.T) {
	s := fixtureModel()
	s.focus = focusActions
	s.drift = map[string]gh.PRLanding{"feat-base": {Number: 12, Merged: true}}
	out := ansi.Strip(s.renderActions(80))
	if !strings.Contains(out, "feat-base (PR #12)") {
		t.Errorf("drift banner should name the branch and its PR, got:\n%s", out)
	}
	if !strings.Contains(out, "merged on the remote") || !strings.Contains(out, "Press X to reconcile") {
		t.Errorf("drift banner should explain the drift and point at X, got:\n%s", out)
	}
}

func TestReconcileKeyGatedOnDrift(t *testing.T) {
	key := func(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }
	s := fixtureModel()
	// No drift → X is inert.
	if s2, _ := s.updateBrowsing(key("X")); s2.phase != stackBrowsing {
		t.Errorf("X must be inert without drift, got phase %d", s2.phase)
	}
	// Drift present → X opens the reconcile confirmation.
	s.drift = map[string]gh.PRLanding{"feat-base": {Number: 12, Merged: true}}
	s2, _ := s.updateBrowsing(key("X"))
	if s2.phase != stackConfirming || s2.pending.Verb != "reconcile" {
		t.Errorf("X should open the reconcile confirm, got phase=%d verb=%q", s2.phase, s2.pending.Verb)
	}
}

func TestReconcileConfirmExplainsAndRuns(t *testing.T) {
	s := fixtureModel()
	s.drift = map[string]gh.PRLanding{"feat-base": {Number: 12, Merged: true}}
	s2, _ := s.startReconcile()
	if s2.phase != stackConfirming || s2.pending.Verb != "reconcile" {
		t.Fatalf("startReconcile: phase=%d verb=%q", s2.phase, s2.pending.Verb)
	}
	out := ansi.Strip(s2.renderConfirm(80))
	if !strings.Contains(out, "feat-base") || !strings.Contains(out, "merged remotely") {
		t.Errorf("reconcile confirm should name the merged branch it drops, got:\n%s", out)
	}
	// Enter runs it → running phase with a live command.
	s3, cmd := s2.updateConfirming(tea.KeyMsg{Type: tea.KeyEnter})
	if s3.phase != stackRunning || cmd == nil {
		t.Errorf("enter should run reconcile: phase=%d cmd=%v", s3.phase, cmd != nil)
	}
}

func TestGhostBranchFunnelsToReconcile(t *testing.T) {
	s := fixtureModel() // current = feat-mid
	s.status.Staged = 2 // so amend isn't disabled for the ordinary reason
	// Current branch itself merged remotely = a ghost: create/maintain verbs funnel
	// to reconcile.
	s.drift = map[string]gh.PRLanding{"feat-mid": {Number: 9, Merged: true}}
	for _, verb := range []string{"new", "insert", "amend", "restack", "sync"} {
		c := townie.Command{Verb: verb}
		if s.actionEnabled(c) {
			t.Errorf("%s must be disabled on a ghost (merged) current branch", verb)
		}
		if r := s.actionDisabledReason(c); !strings.Contains(r, "reconcile (X)") {
			t.Errorf("%s reason on a ghost branch = %q, want it to point at reconcile", verb, r)
		}
	}
	// On a LIVE current branch (no drift), the same verbs stay enabled.
	s.drift = nil
	for _, verb := range []string{"new", "insert", "restack", "sync"} {
		if !s.actionEnabled(townie.Command{Verb: verb}) {
			t.Errorf("%s should be enabled on a live branch", verb)
		}
	}
}
