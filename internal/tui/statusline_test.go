package tui

import (
	"strings"
	"testing"

	"github.com/dotnetemmanuel/cairn/internal/stack"
	"github.com/dotnetemmanuel/cairn/internal/theme"
)

func TestStatuslineNotInRepo(t *testing.T) {
	th := theme.New(theme.DefaultPalette())
	out := renderStatusline(th, "", stack.RepoStatus{InRepo: false}, false, 80)
	if !strings.Contains(out, "not in a git repo") {
		t.Errorf("want not-in-repo message, got %q", out)
	}
}

func TestStatuslineBranchAndCounts(t *testing.T) {
	th := theme.New(theme.DefaultPalette())
	st := stack.RepoStatus{
		InRepo: true, Branch: "feat-mid", HasUpstream: true,
		Staged: 1, Unstaged: 2, Ahead: 2,
	}
	out := renderStatusline(th, "dotnetemmanuel/cairn-sandbox", st, true, 120)
	for _, w := range []string{"cairn-sandbox", "feat-mid", chevron} {
		if !strings.Contains(out, w) {
			t.Errorf("statusline missing %q in %q", w, out)
		}
	}
	if strings.Contains(out, "clean") {
		t.Error("dirty tree should not render the clean marker")
	}
}

func TestStatuslineCleanAndGitTownGate(t *testing.T) {
	th := theme.New(theme.DefaultPalette())
	st := stack.RepoStatus{InRepo: true, Branch: "main", HasUpstream: true}
	clean := renderStatusline(th, "o/r", st, true, 120)
	if !strings.Contains(clean, "clean") || !strings.Contains(clean, "up to date") {
		t.Errorf("clean+current tree should say so, got %q", clean)
	}
	// No git-town → the gate note appears.
	ungated := renderStatusline(th, "o/r", st, false, 120)
	if !strings.Contains(ungated, "git-town not initialized") {
		t.Errorf("want git-town gate note, got %q", ungated)
	}
}

func TestStatuslineDetached(t *testing.T) {
	th := theme.New(theme.DefaultPalette())
	out := renderStatusline(th, "o/r", stack.RepoStatus{InRepo: true, Detached: true}, true, 120)
	if !strings.Contains(out, "detached HEAD") {
		t.Errorf("want detached HEAD, got %q", out)
	}
}
