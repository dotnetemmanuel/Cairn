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

func TestStackRunTransitionsAndReloads(t *testing.T) {
	s := fixtureModel()
	s.phase = stackConfirming
	s.pending = townie.Command{Verb: "amend", Title: "amend"}
	s2, cmd := s.updateConfirming(tea.KeyMsg{Type: tea.KeyEnter})
	if s2.phase != stackRunning || cmd == nil {
		t.Fatalf("enter on confirm should run: phase=%d cmd=%v", s2.phase, cmd != nil)
	}
	// Feed a completion message; should land in Done with the output captured.
	s3, _ := s2.Update(stackRanMsg{out: "restacked feat-top\n", err: nil})
	if s3.phase != stackDone || !strings.Contains(s3.output, "restacked feat-top") {
		t.Errorf("after run: phase=%d output=%q", s3.phase, s3.output)
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
