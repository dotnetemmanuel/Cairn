package stack

import (
	"os/exec"
	"strconv"
	"strings"
)

// RepoStatus is the working-tree snapshot the stack mode needs to answer "here
// you are": whether cwd is a git repo, the checked-out branch (or detached
// HEAD), counts of staged / unstaged / untracked / conflicted paths, and the
// ahead/behind relationship to the upstream. It is local-context only — the
// remote PR browsing path never touches it.
type RepoStatus struct {
	InRepo      bool
	Branch      string // "" when detached or not in a repo
	Detached    bool
	Staged      int
	Unstaged    int // tracked files changed in the working tree (not untracked)
	Untracked   int
	Conflicts   int
	HasUpstream bool
	Ahead       int
	Behind      int
}

// Clean reports whether the working tree has no pending changes of any kind.
func (s RepoStatus) Clean() bool {
	return s.Staged == 0 && s.Unstaged == 0 && s.Untracked == 0 && s.Conflicts == 0
}

// Dirty reports whether there is anything staged, modified, or conflicted —
// untracked files alone don't count as dirty for the "can I amend?" sense.
func (s RepoStatus) Dirty() bool {
	return s.Staged > 0 || s.Unstaged > 0 || s.Conflicts > 0
}

// Status returns the working-tree snapshot for the repo at dir (cwd if empty).
// A non-repo directory yields a zero RepoStatus with InRepo=false rather than an
// error, so the caller can render "not in a git repo" uniformly.
func Status(dir string) RepoStatus {
	cmd := exec.Command("git", "status", "--porcelain=v2", "--branch")
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.Output()
	if err != nil {
		return RepoStatus{} // not a repo (or git unavailable) → InRepo=false
	}
	return parseStatus(string(out))
}

// parseStatus turns `git status --porcelain=v2 --branch` output into a
// RepoStatus. Kept pure (no exec) so the staged/unstaged/ahead-behind accounting
// is directly table-testable.
//
// Format reference (porcelain v2):
//   - "# branch.head <name>" — branch, or "(detached)"
//   - "# branch.ab +A -B"    — ahead/behind (line present only with an upstream)
//   - "1 <XY> …" / "2 <XY> …" — changed entry; X=staged slot, Y=worktree slot,
//     '.' meaning unmodified in that slot
//   - "u …" — unmerged (conflict)
//   - "? …" — untracked
func parseStatus(porcelain string) RepoStatus {
	s := RepoStatus{InRepo: true}
	for _, line := range strings.Split(porcelain, "\n") {
		if line == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "# branch.head "):
			head := strings.TrimSpace(strings.TrimPrefix(line, "# branch.head "))
			if head == "(detached)" {
				s.Detached = true
			} else {
				s.Branch = head
			}
		case strings.HasPrefix(line, "# branch.ab "):
			s.HasUpstream = true
			for _, tok := range strings.Fields(strings.TrimPrefix(line, "# branch.ab ")) {
				if len(tok) < 2 {
					continue
				}
				n, err := strconv.Atoi(tok[1:])
				if err != nil {
					continue
				}
				switch tok[0] {
				case '+':
					s.Ahead = n
				case '-':
					s.Behind = n
				}
			}
		case strings.HasPrefix(line, "1 "), strings.HasPrefix(line, "2 "):
			// fields[1] is the two-char XY status code.
			if f := strings.Fields(line); len(f) >= 2 && len(f[1]) == 2 {
				if f[1][0] != '.' {
					s.Staged++
				}
				if f[1][1] != '.' {
					s.Unstaged++
				}
			}
		case strings.HasPrefix(line, "u "):
			s.Conflicts++
		case strings.HasPrefix(line, "? "):
			s.Untracked++
		}
	}
	return s
}
