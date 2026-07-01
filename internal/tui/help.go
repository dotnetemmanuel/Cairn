package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/dotnetemmanuel/cairn/internal/townie"
)

// helpMaxW caps the help overlay's width at a comfortable reading measure even on
// very wide monitors.
const helpMaxW = 100

// helpBoxWidth returns the overlay's box width: it grows with the terminal up to
// helpMaxW, so the component is wide where there's room (fewer wrapped lines) and
// shrinks gracefully on a narrow screen.
func helpBoxWidth(termW int) int {
	w := termW - 8
	if w > helpMaxW {
		w = helpMaxW
	}
	if w < 34 {
		w = 34
	}
	return w
}

// openHelp builds the help content for the current mode and loads it into a
// viewport sized to fit the terminal — so a long guide (e.g. the full stack
// command reference) scrolls instead of bleeding off the top and bottom.
func (m *Model) openHelp() {
	inner := helpBoxWidth(m.width) - 4 // minus the box's 2-col padding each side
	body := m.helpBody(inner)
	vp := newVP() // arrows + j/k scroll
	vp.Width = inner
	maxH := m.height - 5 // border (2) + padding (2) + the hint line (1)
	if maxH < 3 {
		maxH = 3
	}
	if h := lipgloss.Height(body); h < maxH {
		vp.Height = h
	} else {
		vp.Height = maxH
	}
	vp.SetContent(body)
	m.helpVP = vp
}

// renderHelp draws the help overlay box around the scrollable content, centered
// over the current screen. The body itself is built by helpBody and lives in
// m.helpVP (sized by openHelp), so it can never exceed the terminal height.
func (m Model) renderHelp() string {
	w := helpBoxWidth(m.width)
	hint := "? or esc to close"
	if m.helpVP.TotalLineCount() > m.helpVP.Height {
		hint = "↑/↓ scroll · " + hint
	}
	inner := lipgloss.JoinVertical(lipgloss.Left, m.helpVP.View(),
		lipgloss.NewStyle().Foreground(m.th.Muted).Render(hint))
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(m.th.Focus).
		Background(m.th.Base).
		Padding(1, 2).
		Width(w).
		Render(inner)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}

// helpBody builds the overlay's inner text (no border), wrapped to inner columns.
// It is a pedagogical guide to the stack commands (for people new to git-town)
// plus the general navigation keys, mode-aware so it only lists keys that act in
// the screen the help was opened from.
func (m Model) helpBody(inner int) string {
	th := m.th
	head := func(s string) string {
		return lipgloss.NewStyle().Foreground(th.Focus).Bold(true).Render(s)
	}
	key := func(s string) string {
		return lipgloss.NewStyle().Foreground(th.Primary).Bold(true).Render(s)
	}
	dim := func(s string) string { return lipgloss.NewStyle().Foreground(th.Muted).Render(s) }

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
			{"n/N", "hop to the next/prev header (OPEN/CLOSED, org, repo)"},
			{"←/→ tab", "switch section"},
			{"enter", "open the PR — or fold/unfold the header under the cursor"},
			{"→ / enter", "Notifications: focus the conversation preview (↑/↓ scroll, ← / esc back)"},
			{"x", "mark notification read (Notifications inbox)"},
			{"o", "group the list by repo (toggle) — else newest-updated first"},
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
	return strings.TrimRight(b.String(), "\n")
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
