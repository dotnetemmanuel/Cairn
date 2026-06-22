package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/dotnetemmanuel/cairn/internal/stack"
	"github.com/dotnetemmanuel/cairn/internal/theme"
)

// Statusline glyphs. The working/staged/stash marks deliberately match the
// user's oh-my-posh Event Horizon prompt so the eye reads them the same way in
// Cairn as in the shell. The Nerd Font icons are built from code points via
// string(rune(...)) so they stay pure-ASCII in source — non-ASCII stripping
// can't silently blank them, which is exactly what happened once and left the
// statusline glyph-less.
var (
	iconRepo   = string(rune(0xf401)) // nf-oct-repo
	iconBranch = string(rune(0xe0a0)) // nf-pl-branch
	iconWork   = string(rune(0xf044)) // working-tree changes (omp Working.Changed)
	iconStaged = string(rune(0xf046)) // staged (omp Staging.Changed)
	iconStash  = string(rune(0xeb4b)) // stash (omp)
)

const (
	chevron      = "›"
	glyphAhead   = "↑"
	glyphBehind  = "↓"
	glyphClean   = "✓"
	glyphConflic = "⚠" // matches the stack tree's drift marker
)

// renderStatusline draws the "here you are" breadcrumb: repo › branch › status,
// flat-chevron (no powerline fills), recoloring the status segment by git state.
// It is the single source of truth for the local-context gate: when cwd is not a
// repo / has no git-town / is detached, it says so plainly. Pure given its
// inputs so it is golden-testable.
func renderStatusline(th theme.Theme, repo string, st stack.RepoStatus, hasGitTown bool, width int) string {
	pad := func(s string) string {
		return lipgloss.NewStyle().Width(width).Background(th.Surface).Padding(0, 1).Render(s)
	}

	if !st.InRepo {
		return pad(lipgloss.NewStyle().Foreground(th.Muted).
			Render(glyphConflic + " not in a git repo — stack actions unavailable"))
	}

	sep := lipgloss.NewStyle().Foreground(th.Overlay).Render(" " + chevron + " ")

	// repo segment (green, like the omp path segment).
	repoName := repo
	if repoName == "" {
		repoName = "(unknown repo)"
	}
	segs := []string{
		lipgloss.NewStyle().Foreground(th.Success).Render(iconRepo + " " + repoName),
	}

	// branch segment.
	if st.Detached {
		segs = append(segs, lipgloss.NewStyle().Foreground(th.Danger).Render(iconBranch+" detached HEAD"))
	} else {
		segs = append(segs, lipgloss.NewStyle().Foreground(th.Info).Bold(true).Render(iconBranch+" "+st.Branch))
	}

	// status segment — recolors by state.
	segs = append(segs, statusSegment(th, st))

	line := strings.Join(segs, sep)

	// git-town gate note: mutations need git-town initialized.
	if !hasGitTown && !st.Detached {
		line += sep + lipgloss.NewStyle().Foreground(th.Warning).Render("git-town not initialized")
	}
	return pad(line)
}

// statusSegment renders the working-tree state: dirty counts in peach, ahead/
// behind in cyan, a calm green check when clean and current.
func statusSegment(th theme.Theme, st stack.RepoStatus) string {
	dirty := lipgloss.NewStyle().Foreground(th.Warning) // peach, like omp's dirty bg
	calm := lipgloss.NewStyle().Foreground(th.Muted)
	sync := lipgloss.NewStyle().Foreground(th.Accent2) // cyan, like omp ahead/behind

	var parts []string
	if st.Conflicts > 0 {
		parts = append(parts, lipgloss.NewStyle().Foreground(th.Danger).
			Render(fmt.Sprintf("%s %d", glyphConflic, st.Conflicts)))
	}
	if st.Staged > 0 {
		parts = append(parts, lipgloss.NewStyle().Foreground(th.Success).
			Render(fmt.Sprintf("%s %d", iconStaged, st.Staged)))
	}
	if st.Unstaged > 0 {
		parts = append(parts, dirty.Render(fmt.Sprintf("%s %d", iconWork, st.Unstaged)))
	}
	if st.Untracked > 0 {
		parts = append(parts, calm.Render(fmt.Sprintf("?%d", st.Untracked)))
	}

	// sync state.
	switch {
	case !st.HasUpstream:
		parts = append(parts, calm.Render("no upstream"))
	case st.Ahead > 0 || st.Behind > 0:
		ab := ""
		if st.Ahead > 0 {
			ab += fmt.Sprintf("%s%d", glyphAhead, st.Ahead)
		}
		if st.Behind > 0 {
			if ab != "" {
				ab += " "
			}
			ab += fmt.Sprintf("%s%d", glyphBehind, st.Behind)
		}
		parts = append(parts, sync.Render(ab))
	}

	if len(parts) == 0 {
		// Clean and up to date.
		return lipgloss.NewStyle().Foreground(th.Success).Render(glyphClean+" clean") +
			calm.Render(" · up to date")
	}
	return strings.Join(parts, calm.Render(" "))
}
