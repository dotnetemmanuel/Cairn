package doctor

import (
	"os/exec"
	"runtime"
)

// hintFor returns how to install a missing tool on THIS machine.
func hintFor(tool string) string {
	return installHint(tool, runtime.GOOS, linuxPackageManager())
}

// linuxPackageManager names the distro's package manager, or "" when it is not
// one we know how to advise on.
func linuxPackageManager() string {
	if runtime.GOOS != "linux" {
		return ""
	}
	for _, pm := range []string{"pacman", "apt", "dnf", "zypper"} {
		if _, err := exec.LookPath(pm); err == nil {
			return pm
		}
	}
	return ""
}

// installHint is the pure form: given a tool, an OS and (on Linux) the package
// manager present, it returns a command the user can actually run, or the
// project's own installer where no package carries it. Kept honest on purpose:
// git-town is in neither apt, dnf nor winget, so those cases point at its
// releases rather than inventing a package that does not exist.
func installHint(tool, goos, linuxPM string) string {
	switch goos {
	case "windows":
		switch tool {
		case "git":
			return "install git: `winget install --id Git.Git`"
		case "gh":
			return "install the GitHub CLI: `winget install --id GitHub.cli`"
		case "git-town":
			return "install git-town: run the installer git-town_windows_intel_64.msi from https://github.com/git-town/git-town/releases/latest"
		}
	case "darwin":
		switch tool {
		case "git":
			return "install git: `brew install git`"
		case "gh":
			return "install the GitHub CLI: `brew install gh`"
		case "git-town":
			return "install git-town: `brew install git-town`"
		}
	case "linux":
		switch linuxPM {
		case "pacman":
			switch tool {
			case "git":
				return "install git: `sudo pacman -S git`"
			case "gh":
				return "install the GitHub CLI: `sudo pacman -S github-cli`"
			case "git-town":
				return "install git-town: `sudo pacman -S git-town`"
			}
		case "apt":
			switch tool {
			case "git":
				return "install git: `sudo apt install git`"
			case "gh":
				return "install the GitHub CLI: see https://cli.github.com (it ships its own apt repository)"
			case "git-town":
				return "install git-town: take the .deb from https://github.com/git-town/git-town/releases/latest"
			}
		case "dnf", "zypper":
			switch tool {
			case "git":
				return "install git: `sudo " + linuxPM + " install git`"
			case "gh":
				return "install the GitHub CLI: `sudo " + linuxPM + " install gh`"
			case "git-town":
				return "install git-town: take the .rpm from https://github.com/git-town/git-town/releases/latest"
			}
		}
	}
	switch tool {
	case "git":
		return "install git: https://git-scm.com"
	case "gh":
		return "install the GitHub CLI: https://cli.github.com"
	case "git-town":
		return "install git-town: https://www.git-town.com"
	}
	return "install " + tool
}
