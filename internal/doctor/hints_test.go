package doctor

import (
	"strings"
	"testing"
)

// A hint that names another platform's package manager is worse than no hint:
// it tells a Windows user to run pacman.
func TestHintsNeverNameAnotherPlatformsPackageManager(t *testing.T) {
	foreign := map[string][]string{
		"windows": {"pacman", "apt", "dnf", "brew"},
		"darwin":  {"pacman", "apt", "dnf", "winget", "scoop"},
		"linux":   {"winget", "scoop", "brew"},
	}
	for goos, banned := range foreign {
		for _, tool := range []string{"git", "git-town", "gh"} {
			hint := installHint(tool, goos, "pacman")
			for _, word := range banned {
				if strings.Contains(strings.ToLower(hint), word) {
					t.Errorf("%s hint for %s mentions %q: %s", goos, tool, word, hint)
				}
			}
		}
	}
}

func TestWindowsHintsGiveRunnableCommands(t *testing.T) {
	if got := installHint("gh", "windows", ""); !strings.Contains(got, "winget install --id GitHub.cli") {
		t.Errorf("gh on Windows should give the winget command, got %q", got)
	}
	if got := installHint("git", "windows", ""); !strings.Contains(got, "winget install --id Git.Git") {
		t.Errorf("git on Windows should give the winget command, got %q", got)
	}
	// git-town is not in winget, so pointing at winget would be a dead end.
	got := installHint("git-town", "windows", "")
	if strings.Contains(got, "winget") || !strings.Contains(got, "git-town/releases") {
		t.Errorf("git-town on Windows should point at its own installer, got %q", got)
	}
}

func TestLinuxHintsFollowThePackageManagerFound(t *testing.T) {
	if got := installHint("gh", "linux", "pacman"); !strings.Contains(got, "pacman -S github-cli") {
		t.Errorf("on Arch, gh is github-cli, got %q", got)
	}
	if got := installHint("gh", "linux", "apt"); !strings.Contains(got, "apt") {
		t.Errorf("on Debian, the hint should use apt, got %q", got)
	}
	// git-town is in neither apt nor dnf, so those must point at its own packages.
	if got := installHint("git-town", "linux", "apt"); strings.Contains(got, "apt install") {
		t.Errorf("git-town is not in apt; hint must not claim it is: %q", got)
	}
	// An unrecognised distro still gets somewhere to go.
	if got := installHint("git-town", "linux", ""); !strings.Contains(got, "git-town.com") {
		t.Errorf("unknown package manager should still point somewhere, got %q", got)
	}
}

func TestMacHintsUseHomebrew(t *testing.T) {
	for _, tool := range []string{"git", "git-town", "gh"} {
		if got := installHint(tool, "darwin", ""); !strings.Contains(got, "brew install") {
			t.Errorf("%s on macOS should give a brew command, got %q", tool, got)
		}
	}
}
