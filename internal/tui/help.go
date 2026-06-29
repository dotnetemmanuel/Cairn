package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/dotnetemmanuel/cairn/internal/townie"
)

// renderHelp draws the help overlay: a pedagogical guide to the stack commands
// (for people new to git-town) plus the general navigation keys. It is rendered
// centered over the current screen.
func (m Model) renderHelp() string {
	th := m.th
	head := func(s string) string {
		return lipgloss.NewStyle().Foreground(th.Focus).Bold(true).Render(s)
	}
	key := func(s string) string {
		return lipgloss.NewStyle().Foreground(th.Primary).Bold(true).Render(s)
	}
	dim := func(s string) string { return lipgloss.NewStyle().Foreground(th.Muted).Render(s) }

	// Box width, bounded by the terminal; inner = box minus 2-col padding.
	w := 60
	if m.width-8 < w {
		w = m.width - 8
	}
	if w < 34 {
		w = 34
	}
	inner := w - 4

	indentWrap := func(s, indent string) string {
		return indent + wrapPlain(s, inner-len(indent), indent)
	}

	// The overlay is global, but its keys are context-specific: the stack
	// mutation commands (n/I/S/R/A) only act inside stack mode, and several
	// navigation keys (enter→open PR, s→sidebar, S→open stack mode, q→quit) only
	// act on the dashboard. So we render the section that matches the mode the
	// help was opened from — otherwise the same box would list, e.g., `S` as both
	// "sync" and "open stack mode".
	inStack := m.mode == modeStack

	var b strings.Builder

	if inStack {
		// Stack mode: the five mutation commands are live here. List them in full.
		b.WriteString(head("Stack commands") + "\n")
		b.WriteString(dim(wrapPlain(
			"A stack is a chain of branches where each PR targets the one below "+
				"it instead of main. git-town keeps the chain rebased; Cairn drives it. "+
				"New to git-town? Start with new, then sync.", inner, "")) + "\n\n")

		for _, c := range townie.Catalog() {
			name := c.Title
			if c.NeedsName {
				name += " <name>"
			}
			b.WriteString(key(pad(c.Key, 3)) +
				lipgloss.NewStyle().Foreground(th.Text).Bold(true).Render(name) + "\n")
			b.WriteString(lipgloss.NewStyle().Foreground(th.Info).Render(indentWrap(c.Short, "    ")) + "\n")
			b.WriteString(dim(indentWrap(c.Long, "    ")) + "\n")
			b.WriteString("    " + dim("↳ "+c.Hint()) + "\n\n")
		}

		b.WriteString(head("Navigation") + "\n")
		for _, nav := range [][2]string{
			{"↑/↓ j/k", "move within actions / stack"},
			{"←/→ tab", "switch actions ⇄ tree"},
			{"enter", "run the action / check out the branch"},
			{"r", "refresh tree + status"},
			{"esc", "back to the dashboard"},
			{"?", "toggle this help"},
		} {
			b.WriteString(key(pad(nav[0], 9)) + " " + dim(nav[1]) + "\n")
		}
	} else {
		// Dashboard: list only keys that act here. Point at stack mode for the
		// mutation commands (they're documented by stack mode's own `?`).
		b.WriteString(head("Navigation") + "\n")
		for _, nav := range [][2]string{
			{"↑/↓", "move within the list / stack"},
			{"n/N", "jump to the next/prev OPEN/CLOSED group"},
			{"←/→ tab", "switch section"},
			{"enter", "open the PR — or fold/unfold an OPEN/CLOSED group"},
			{"→ / enter", "Notifications: focus the conversation preview (↑/↓ scroll, ← / esc back)"},
			{"x", "mark notification read (Notifications inbox)"},
			{"s", "toggle the stack sidebar"},
			{"S", "open stack mode (author/maintain the cwd repo's stack)"},
			{"r", "sync all tabs (re-run every query)"},
			{"?", "toggle this help"},
			{"q", "quit"},
		} {
			b.WriteString(key(pad(nav[0], 9)) + " " + dim(nav[1]) + "\n")
		}
		b.WriteString("\n" + head("Stacks") + "\n")
		b.WriteString(dim(wrapPlain(
			"Press S to open stack mode and author/maintain stacked PRs in the "+
				"current repo. Its own ? lists the stack commands (new, insert, sync, "+
				"restack, amend).", inner, "")) + "\n")
	}
	b.WriteString("\n" + dim("? or esc to close"))

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(th.Focus).
		Background(th.Base).
		Padding(1, 2).
		Width(w).
		Render(strings.TrimRight(b.String(), "\n"))

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}

// wrapPlain word-wraps s to width, prefixing every line after the first with
// indent (so multi-line explanations stay aligned under their heading).
func wrapPlain(s string, width int, indent string) string {
	if width < 8 {
		width = 8
	}
	words := strings.Fields(s)
	var lines []string
	cur := ""
	for _, w := range words {
		if cur == "" {
			cur = w
		} else if len(cur)+1+len(w) <= width {
			cur += " " + w
		} else {
			lines = append(lines, cur)
			cur = w
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return strings.Join(lines, "\n"+indent)
}
