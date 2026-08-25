package stack

import "strings"

// HeldByWorktree maps each branch that is checked out in a worktree OTHER than
// dir's own (cwd when empty) to that worktree's path. Git allows one working
// copy per branch, so these branches cannot be checked out here — the tree can
// say so up front instead of letting `git checkout` fail with exit 128. Empty
// when the repo has no linked worktrees or git can't be reached.
func HeldByWorktree(dir string) map[string]string {
	out := gitOutput(dir, "worktree", "list", "--porcelain")
	self := gitOutput(dir, "rev-parse", "--show-toplevel")
	if out == "" || self == "" {
		return nil // without knowing which worktree is ours we would flag our own branch
	}
	return parseWorktreeBranches(out, self)
}

// parseWorktreeBranches turns `git worktree list --porcelain` into branch → path,
// dropping the entry for self (the worktree we are running in) and any detached
// checkout. Pure, so the porcelain parsing is testable without real worktrees.
func parseWorktreeBranches(porcelain, self string) map[string]string {
	held := map[string]string{}
	path := ""
	for _, line := range strings.Split(porcelain, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "worktree "):
			path = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "branch "):
			b := strings.TrimPrefix(strings.TrimPrefix(line, "branch "), "refs/heads/")
			if b != "" && path != "" && path != self {
				held[b] = path
			}
		}
	}
	if len(held) == 0 {
		return nil
	}
	return held
}
