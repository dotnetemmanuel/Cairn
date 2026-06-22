package stack

import "testing"

func TestParseStatus(t *testing.T) {
	// One staged-only (M.), one staged+worktree (MM), one worktree-only (.M),
	// one untracked (?), one conflict (u), branch + ahead/behind header.
	porcelain := `# branch.oid abc123
# branch.head feat-mid
# branch.upstream origin/feat-mid
# branch.ab +2 -1
1 M. N... 100644 100644 100644 aaa bbb staged.go
1 MM N... 100644 100644 100644 ccc ddd both.go
1 .M N... 100644 100644 100644 eee fff worktree.go
u UU N... 100644 100644 100644 100644 ggg hhh iii conflict.go
? new.go`

	s := parseStatus(porcelain)
	if !s.InRepo {
		t.Fatal("InRepo should be true")
	}
	if s.Branch != "feat-mid" || s.Detached {
		t.Errorf("branch=%q detached=%v, want feat-mid / false", s.Branch, s.Detached)
	}
	if !s.HasUpstream || s.Ahead != 2 || s.Behind != 1 {
		t.Errorf("ahead/behind = +%d -%d (upstream=%v), want +2 -1", s.Ahead, s.Behind, s.HasUpstream)
	}
	// staged: M. and MM → 2. unstaged: MM and .M → 2. untracked: 1. conflict: 1.
	if s.Staged != 2 || s.Unstaged != 2 || s.Untracked != 1 || s.Conflicts != 1 {
		t.Errorf("counts = staged %d unstaged %d untracked %d conflict %d; want 2/2/1/1",
			s.Staged, s.Unstaged, s.Untracked, s.Conflicts)
	}
	if s.Clean() || !s.Dirty() {
		t.Errorf("Clean=%v Dirty=%v, want false/true", s.Clean(), s.Dirty())
	}
}

func TestParseStatusDetached(t *testing.T) {
	s := parseStatus("# branch.oid abc123\n# branch.head (detached)\n")
	if !s.Detached || s.Branch != "" {
		t.Errorf("detached=%v branch=%q, want true / empty", s.Detached, s.Branch)
	}
	if s.HasUpstream {
		t.Error("no branch.ab line → HasUpstream should be false")
	}
}

func TestParseStatusCleanUntrackedOnly(t *testing.T) {
	// Untracked files alone: Clean() false but Dirty() false (can't amend nothing).
	s := parseStatus("# branch.head main\n? scratch.txt\n")
	if s.Clean() {
		t.Error("untracked file present → Clean() should be false")
	}
	if s.Dirty() {
		t.Error("untracked-only → Dirty() should be false (nothing staged/modified)")
	}
}
