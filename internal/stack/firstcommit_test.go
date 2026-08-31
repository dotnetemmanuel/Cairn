package stack

import (
	"os/exec"
	"testing"
	"time"
)

// git runs a git command in dir, failing the test on error.
func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func commitAt(t *testing.T, dir, msg string, at time.Time) {
	t.Helper()
	stamp := at.Format(time.RFC3339)
	cmd := exec.Command("git", "commit", "-q", "--allow-empty", "-m", msg)
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		"GIT_AUTHOR_DATE="+stamp, "GIT_COMMITTER_DATE="+stamp)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("commit %q: %v\n%s", msg, err, out)
	}
}

// The branch carries several commits: the answer must be the OLDEST one unique to
// it, not the tip. Dating a branch by its tip would call a long-lived branch new
// and drop a real drift warning.
func TestFirstCommitTimeIsOldestUniqueCommit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	commitAt(t, dir, "trunk", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	git(t, dir, "checkout", "-q", "-b", "feature")
	oldest := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	commitAt(t, dir, "first", oldest)
	commitAt(t, dir, "second", time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC))

	got := FirstCommitTime(dir, "main", "feature")
	if !got.Equal(oldest) {
		t.Fatalf("FirstCommitTime = %v, want %v (the oldest commit unique to the branch)", got.UTC(), oldest)
	}
}

func TestFirstCommitTimeZeroWhenBranchAddsNothing(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	commitAt(t, dir, "trunk", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	git(t, dir, "checkout", "-q", "-b", "feature")

	if got := FirstCommitTime(dir, "main", "feature"); !got.IsZero() {
		t.Fatalf("FirstCommitTime = %v, want zero for a branch with no commits of its own", got)
	}
}

func TestFirstCommitTimeZeroOnUnknownBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	commitAt(t, dir, "trunk", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	if got := FirstCommitTime(dir, "main", "nope"); !got.IsZero() {
		t.Fatalf("FirstCommitTime = %v, want zero for a branch git does not know", got)
	}
}
