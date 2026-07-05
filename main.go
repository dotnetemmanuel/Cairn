// Command cairn is a terminal cockpit for GitHub: dashboard + review +
// stacked-PR workflow in one TUI. Phase 0 provides the dispatcher, the
// environment doctor, and a minimal authenticated header.
package main

import (
	"fmt"
	"os"
	"os/exec"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/dotnetemmanuel/cairn/internal/config"
	"github.com/dotnetemmanuel/cairn/internal/doctor"
	"github.com/dotnetemmanuel/cairn/internal/gh"
	"github.com/dotnetemmanuel/cairn/internal/tui"
)

const version = "0.0.0-phase0"

func main() {
	args := os.Args[1:]

	// Bare invocation launches the TUI. CAIRN_DEMO=1 flips it to demo mode so
	// `CAIRN_DEMO=1 cairn` works too (handy for scripted screenshot capture).
	if len(args) == 0 {
		if os.Getenv("CAIRN_DEMO") == "1" {
			runDemo()
			return
		}
		runTUI()
		return
	}

	switch args[0] {
	case "demo":
		runDemo()
	case "doctor":
		os.Exit(doctor.Run())
	case "version", "--version", "-v":
		fmt.Printf("cairn %s\n", version)
	case "help", "--help", "-h":
		printHelp()
	default:
		// Hard Rule 4: unknown subcommands fall through to git, never blocked.
		fallthroughToGit(args)
	}
}

func runTUI() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cairn: config error: %v\n", err)
		os.Exit(1)
	}

	// Preflight: required tools + auth must be present before we take over the
	// screen, so failures are readable rather than buried in the alt-screen.
	if report := doctor.Diagnose(); !report.OK() {
		fmt.Fprintln(os.Stderr, "cairn: environment not ready")
		fmt.Fprintln(os.Stderr, report.String())
		fmt.Fprintln(os.Stderr, "\nRun `cairn doctor` for details, fix the above, and retry.")
		os.Exit(1)
	}

	p := tea.NewProgram(tui.New(cfg), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "cairn: %v\n", err)
		os.Exit(1)
	}
}

// runDemo launches the TUI against built-in fake data (see internal/gh/demo.go).
// It mirrors runTUI but enables the demo overlay and SKIPS the doctor preflight,
// so demo mode runs on a clean machine with no gh/git-town and no auth. Config
// still loads (theme, sections) so the board looks real; defaults on any error.
func runDemo() {
	gh.EnableDemo()

	cfg, err := config.Load()
	if err != nil {
		cfg = config.Default()
	}

	p := tea.NewProgram(tui.New(cfg), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "cairn: %v\n", err)
		os.Exit(1)
	}
}

// fallthroughToGit execs git with the original arguments, mirroring its exit
// code so muscle memory like `cairn status` behaves exactly like `git status`.
func fallthroughToGit(args []string) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		fmt.Fprintln(os.Stderr, "cairn: git not found in PATH")
		os.Exit(1)
	}
	cmd := exec.Command(gitPath, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		fmt.Fprintf(os.Stderr, "cairn: %v\n", err)
		os.Exit(1)
	}
}

func printHelp() {
	fmt.Print(`cairn — a terminal cockpit for GitHub

Usage:
  cairn            launch the TUI
  cairn demo       launch the TUI against built-in fake data (no gh/git-town/auth)
  cairn doctor     diagnose tools (git, git-town, gh) and auth
  cairn version    print version
  cairn help       show this help

Any other subcommand falls through to git (e.g. ` + "`cairn status`" + ` == ` + "`git status`" + `).
`)
}
