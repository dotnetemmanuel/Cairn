package conflict

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Detect inspects the repo at dir and reports the in-progress operation (rebase
// or merge), the two branch labels to show on each side, and the unmerged file
// paths. It is the entry point the TUI calls when a git-town op fails: an OpNone
// result with no files simply means "no conflict in progress" (not an error).
//
// The exec calls here aren't unit-tested — the parsing they feed is (see
// unmergedFiles / parseHeadName). The whole path is exercised by the sandbox
// acceptance test.
func Detect(dir string) (State, error) {
	gitDir, err := git(dir, "rev-parse", "--git-dir")
	if err != nil {
		return State{}, err
	}
	gitDir = strings.TrimSpace(gitDir)
	if !filepath.IsAbs(gitDir) && dir != "" {
		gitDir = filepath.Join(dir, gitDir)
	}

	st := State{}
	switch {
	case exists(filepath.Join(gitDir, "rebase-merge")) || exists(filepath.Join(gitDir, "rebase-apply")):
		st.Op = OpRebase
		// The branch being rebased is "yours"; the commit it's replayed onto is
		// "incoming" (the trunk/parent catching up).
		st.Yours = parseHeadName(readFile(filepath.Join(gitDir, "rebase-merge", "head-name")))
		if st.Yours == "" {
			st.Yours = strings.TrimSpace(gitNoErr(dir, "branch", "--show-current"))
		}
		st.Incoming = friendlyRef(dir, readFile(filepath.Join(gitDir, "rebase-merge", "onto")))
	case exists(filepath.Join(gitDir, "MERGE_HEAD")):
		st.Op = OpMerge
		// On a merge it's the other way round: the current branch is "yours" and
		// the ref being merged in is "incoming".
		st.Yours = strings.TrimSpace(gitNoErr(dir, "branch", "--show-current"))
		st.Incoming = friendlyRef(dir, readFile(filepath.Join(gitDir, "MERGE_HEAD")))
	default:
		st.Op = OpNone
	}

	porcelain, err := git(dir, "status", "--porcelain=v2")
	if err != nil {
		return State{}, err
	}
	st.Files = unmergedFiles(porcelain)
	return st, nil
}

// unmergedFiles extracts the conflicted paths from `git status --porcelain=v2`
// output. Unmerged entries are the lines beginning "u "; the path is the final
// space-separated field.
func unmergedFiles(porcelain string) []string {
	var out []string
	for _, line := range strings.Split(porcelain, "\n") {
		if !strings.HasPrefix(line, "u ") {
			continue
		}
		if i := strings.LastIndex(line, " "); i >= 0 && i+1 < len(line) {
			out = append(out, line[i+1:])
		}
	}
	return out
}

// parseHeadName turns a rebase head-name file (e.g. "refs/heads/feat/x\n") into a
// plain branch name; a bare name passes through unchanged.
func parseHeadName(s string) string {
	return strings.TrimPrefix(strings.TrimSpace(s), "refs/heads/")
}

// friendlyRef resolves a SHA to a readable branch-ish name via `git name-rev`,
// falling back to the short SHA when that fails. Empty input yields "".
func friendlyRef(dir, sha string) string {
	sha = strings.TrimSpace(sha)
	if sha == "" {
		return ""
	}
	name := strings.TrimSpace(gitNoErr(dir, "name-rev", "--name-only", sha))
	name = strings.TrimPrefix(name, "remotes/")
	if name == "" || strings.Contains(name, "undefined") {
		if len(sha) > 8 {
			return sha[:8]
		}
		return sha
	}
	return name
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func readFile(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

// git runs a git command in dir and returns trimmed stdout, or an error.
func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.Output()
	return string(out), err
}

// gitNoErr runs git and returns stdout, swallowing errors (used for best-effort
// label lookups where a blank result is an acceptable fallback).
func gitNoErr(dir string, args ...string) string {
	out, _ := git(dir, args...)
	return out
}
