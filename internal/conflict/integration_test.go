package conflict

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestEndToEndRebaseConflict drives the exec-backed paths (Detect, WriteResolved)
// against a real git rebase conflict in a temp repo, so the parts that aren't
// unit-tested are exercised end to end. Skips if git is unavailable.
func TestEndToEndRebaseConflict(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
			"GIT_EDITOR=true",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}
	write := func(content string) {
		if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	run("init", "-b", "main")
	write("base\n")
	run("add", ".")
	run("commit", "-m", "base")

	run("checkout", "-b", "feat")
	write("feat side\n")
	run("commit", "-am", "feat")

	run("checkout", "main")
	write("main side\n")
	run("commit", "-am", "main")

	// Rebasing feat onto main replays the feat commit over main's change to the
	// same line → conflict.
	run("checkout", "feat")
	cmd := exec.Command("git", "rebase", "main")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_EDITOR=true")
	_ = cmd.Run() // expected to fail with a conflict

	// Detect should see the rebase and the unmerged file.
	st, err := Detect(dir)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if st.Op != OpRebase {
		t.Fatalf("Op = %v, want OpRebase", st.Op)
	}
	if len(st.Files) != 1 || st.Files[0] != "f.txt" {
		t.Fatalf("Files = %v, want [f.txt]", st.Files)
	}

	// Parse the conflicted file and resolve by taking the incoming side.
	raw, err := os.ReadFile(filepath.Join(dir, "f.txt"))
	if err != nil {
		t.Fatal(err)
	}
	spans, err := Parse(string(raw))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if Conflicts(spans) != 1 {
		t.Fatalf("want 1 conflict, got %d", Conflicts(spans))
	}
	merged := Apply(spans, []Resolution{{Choice: ChoiceIncoming}})
	if strings.Contains(merged, "<<<<<<<") || strings.Contains(merged, ">>>>>>>") {
		t.Fatalf("merged content still has markers:\n%s", merged)
	}

	// WriteResolved writes + stages it; the file should leave the unmerged set.
	if err := WriteResolved(dir, "f.txt", merged); err != nil {
		t.Fatalf("WriteResolved: %v", err)
	}
	st2, _ := Detect(dir)
	if len(st2.Files) != 0 {
		t.Fatalf("after resolve, still unmerged: %v", st2.Files)
	}
}
