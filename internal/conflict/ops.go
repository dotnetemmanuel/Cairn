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
