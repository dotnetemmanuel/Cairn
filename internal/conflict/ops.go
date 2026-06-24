package conflict

import (
	"os"
	"os/exec"
	"path/filepath"
)

// WriteResolved writes the reassembled content for a resolved file back to disk
// and stages it (`git add`), marking the conflict settled. path is relative to
// the repo root at dir. The git-town `continue` that follows is delegated to the
// townie layer, not here, so this package stays free of git-town specifics.
func WriteResolved(dir, path, content string) error {
	full := path
	if dir != "" {
		full = filepath.Join(dir, path)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		return err
	}
	return stage(dir, path)
}

func stage(dir, path string) error {
	cmd := exec.Command("git", "add", "--", path)
	if dir != "" {
		cmd.Dir = dir
	}
	return cmd.Run()
}

// ContinuePlain resumes the underlying git operation directly. It's the fallback
// for when the conflict wasn't started by git-town (a bare `git rebase`/`git
// merge`, or a rebase begun outside Cairn), so `git town continue` has no
// runstate to resume. The editor is forced non-interactive so it never blocks.
func ContinuePlain(dir string, op Op) (string, error) {
	var args []string
	switch op {
	case OpRebase:
		args = []string{"rebase", "--continue"}
	case OpMerge:
		args = []string{"commit", "--no-edit"}
	default:
		return "", nil
	}
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(),
		"GIT_EDITOR=true", "GIT_SEQUENCE_EDITOR=true", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	return string(out), err
}
