package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
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
	ship := townie.Command{Verb: "ship"}
	if s.actionEnabled(ship) {
		t.Error("ship must be disabled on a middle branch")
	}
	s.status.Branch = "feat-base" // direct child of trunk = bottom
	if !s.actionEnabled(ship) {
		t.Error("ship should be enabled on the bottom branch")
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

	// 'n' (new) needs a name → naming phase, even from the tree pane.
	s := fixtureModel()
	s.focus = focusTree
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
