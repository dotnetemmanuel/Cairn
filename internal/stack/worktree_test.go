package stack

import "testing"

func TestParseWorktreeBranchesSkipsSelfAndDetached(t *testing.T) {
	porcelain := `worktree /home/e/repo
HEAD aaa
branch refs/heads/fix/71

worktree /home/e/repo-detached
HEAD bbb
detached

worktree /home/e/repo-72
HEAD ccc
branch refs/heads/fix/72
`
	got := parseWorktreeBranches(porcelain, "/home/e/repo")
	if len(got) != 1 {
		t.Fatalf("want only the other worktree's branch, got %v", got)
	}
	if got["fix/72"] != "/home/e/repo-72" {
		t.Errorf("fix/72 path = %q", got["fix/72"])
	}
	if _, ok := got["fix/71"]; ok {
		t.Error("our own worktree's branch must not count as held")
	}
}

func TestParseWorktreeBranchesSingleWorktreeIsEmpty(t *testing.T) {
	porcelain := "worktree /home/e/repo\nHEAD aaa\nbranch refs/heads/main\n"
	if got := parseWorktreeBranches(porcelain, "/home/e/repo"); got != nil {
		t.Errorf("a lone worktree holds nothing, got %v", got)
	}
}

// A bare main repo has no working copy of its own, so git reports it with "bare"
// and self is one of the linked worktrees.
func TestParseWorktreeBranchesBareRepo(t *testing.T) {
	porcelain := "worktree /home/e/bare\nbare\n\nworktree /home/e/wt\nHEAD aaa\nbranch refs/heads/feat\n" +
		"\nworktree /home/e/here\nHEAD bbb\nbranch refs/heads/mine\n"
	got := parseWorktreeBranches(porcelain, "/home/e/here")
	if len(got) != 1 || got["feat"] != "/home/e/wt" {
		t.Errorf("want only feat held by the other linked worktree, got %v", got)
	}
}
