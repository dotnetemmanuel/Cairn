// Package doctor diagnoses Cairn's environment: required external tools and
// GitHub authentication. It backs both the `cairn doctor` subcommand and the
// preflight check the TUI runs before launching.
package doctor

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/cli/go-gh/v2/pkg/auth"
	"github.com/dotnetemmanuel/cairn/internal/gh"
)

// Check is a single diagnostic result.
type Check struct {
	Name     string // short label, e.g. "git-town"
	OK       bool
	Required bool   // a failing required check blocks the TUI
	Detail   string // human-readable status or remediation hint
}

// Report is the full set of checks.
type Report struct {
	Checks []Check
}

// OK reports whether every required check passed.
func (r Report) OK() bool {
	for _, c := range r.Checks {
		if c.Required && !c.OK {
			return false
		}
	}
	return true
}

// requiredTools are the external binaries Cairn shells out to. git-town owns
// every stack mutation (Hard Rule 1); gh provides the token go-gh reads
// (Hard Rule 2); git handles diff/status and the fall-through.
var requiredTools = []struct {
	name string
	hint string
}{
	{"git", "install git"},
	{"git-town", "install git-town: https://www.git-town.com (e.g. `pacman -S git-town`)"},
	{"gh", "install the GitHub CLI: https://cli.github.com"},
}

// Diagnose runs all checks and returns a Report.
func Diagnose() Report {
	var r Report

	for _, t := range requiredTools {
		path, err := exec.LookPath(t.name)
		c := Check{Name: t.name, Required: true}
		if err != nil {
			c.OK = false
			c.Detail = "not found in PATH — " + t.hint
		} else {
			c.OK = true
			c.Detail = path
			if v := toolVersion(t.name); v != "" {
				c.Detail = fmt.Sprintf("%s (%s)", path, v)
			}
		}
		r.Checks = append(r.Checks, c)
	}

	// Auth: token presence is required; resolving the viewer is best-effort.
	token, _ := auth.TokenForHost("github.com")
	authCheck := Check{Name: "gh auth", Required: true}
	if token == "" {
		authCheck.OK = false
		authCheck.Detail = "no token for github.com — run `gh auth login`"
		r.Checks = append(r.Checks, authCheck)
		return r
	}
	authCheck.OK = true
	authCheck.Detail = "token resolved for github.com"
	r.Checks = append(r.Checks, authCheck)

	viewer, err := gh.FetchViewer()
	apiCheck := Check{Name: "github api", Required: false}
	if err != nil {
		apiCheck.OK = false
		apiCheck.Detail = "could not reach GitHub API: " + err.Error()
	} else {
		apiCheck.OK = true
		apiCheck.Detail = fmt.Sprintf("logged in as %s · %d/%d API calls remaining",
			viewer.Login, viewer.RateRemaining, viewer.RateLimit)
	}
	r.Checks = append(r.Checks, apiCheck)

	return r
}

// toolVersion returns a best-effort one-line version string, or "" if it can't
// be determined quickly.
func toolVersion(name string) string {
	var args []string
	switch name {
	case "git", "git-town":
		args = []string{"--version"}
	case "gh":
		args = []string{"--version"}
	default:
		return ""
	}
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		return ""
	}
	line := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0]
	return strings.TrimSpace(line)
}

// String renders the report for the terminal.
func (r Report) String() string {
	var b strings.Builder
	for _, c := range r.Checks {
		mark := "✓"
		if !c.OK {
			mark = "✗"
		}
		req := ""
		if !c.Required {
			req = " (optional)"
		}
		fmt.Fprintf(&b, "  %s  %-12s%s  %s\n", mark, c.Name, req, c.Detail)
	}
	return strings.TrimRight(b.String(), "\n")
}

// Run executes `cairn doctor`: prints the report and returns a process exit
// code (0 if all required checks pass, 1 otherwise).
func Run() int {
	r := Diagnose()
	fmt.Println("cairn doctor")
	fmt.Println(r.String())
	if r.OK() {
		fmt.Println("\nAll required checks passed.")
		return 0
	}
	fmt.Println("\nSome required checks failed — see hints above.")
	return 1
}
