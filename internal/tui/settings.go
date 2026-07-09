package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/dotnetemmanuel/cairn/internal/theme"
)

// settingsModel is the dedicated Settings screen. Today it hosts a single pane —
// the theme picker — but it is built as a full-screen mode so more settings panes
// can be added later without disturbing the dashboard. Moving the cursor (or
// toggling the variant) re-resolves candidateTheme so the whole screen repaints as
// a live preview; nothing is applied to the app until the user presses enter, and
// esc discards the candidate entirely.
type settingsModel struct {
	lib      theme.Library
	override theme.Palette // the user's per-token override, layered so preview matches reality

	cursor int    // index into lib.Themes
	mode   string // candidate variant: "dark" | "light"

	// The selection that is currently applied/persisted — used to mark the active
	// row and so the picker opens on it.
	savedName string
	savedMode string

	// candidateTheme is the resolved preview theme for (lib.Themes[cursor], mode).
	candidateTheme theme.Theme

	width  int
	height int
}

// newSettingsModel builds the picker positioned on the currently-active theme and
// variant, carrying the user's per-token override so the preview matches what the
// app will actually render.
func newSettingsModel(lib theme.Library, name, mode string, override theme.Palette, width, height int) settingsModel {
	s := settingsModel{
		lib:       lib,
		override:  override,
		mode:      normalizeMode(mode),
		savedName: name,
		savedMode: normalizeMode(mode),
		width:     width,
		height:    height,
	}
	for i, t := range lib.Themes {
		if t.Name == name {
			s.cursor = i
			break
		}
	}
	s.reresolve()
	return s
}

// normalizeMode collapses anything that isn't light to dark.
func normalizeMode(mode string) string {
	if mode == theme.ModeLight {
		return theme.ModeLight
	}
	return theme.ModeDark
}

// reresolve recomputes the preview theme for the current cursor + mode.
func (s *settingsModel) reresolve() {
	if len(s.lib.Themes) == 0 {
		s.candidateTheme = theme.Resolve(s.mode, s.override)
		return
	}
	s.candidateTheme = theme.ResolveNamed(s.lib.Themes[s.cursor], s.mode, s.override)
}

// selectedName returns the name of the theme under the cursor.
func (s settingsModel) selectedName() string {
	if len(s.lib.Themes) == 0 {
		return s.savedName
	}
	return s.lib.Themes[s.cursor].Name
}

// Update handles in-screen navigation only: moving between themes and toggling the
// dark/light variant. Apply (enter) and cancel (esc) are handled by the parent
// model, which owns the mode switch and the live re-theme of the other screens.
func (s settingsModel) Update(msg tea.Msg) (settingsModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		s.width, s.height = msg.Width, msg.Height
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if s.cursor > 0 {
				s.cursor--
				s.reresolve()
			}
		case "down", "j":
			if s.cursor < len(s.lib.Themes)-1 {
				s.cursor++
				s.reresolve()
			}
		case "home", "g":
			s.cursor = 0
			s.reresolve()
		case "end", "G":
			if len(s.lib.Themes) > 0 {
				s.cursor = len(s.lib.Themes) - 1
				s.reresolve()
			}
		case " ", "space", "left", "right", "h", "l":
			s.mode = theme.Toggle(s.mode)
			s.reresolve()
		}
	}
	return s, nil
}

// View renders the settings box centered over the screen, painted in the candidate
// theme so it doubles as a live preview.
func (s settingsModel) View() string {
	th := s.candidateTheme
	w := helpBoxWidth(s.width)
	inner := w - 2*2 // Padding(1,2) eats two columns each side

	head := func(t string) string {
		return lipgloss.NewStyle().Foreground(th.Focus).Bold(true).Render(t)
	}
	dim := lipgloss.NewStyle().Foreground(th.Muted).Render

	var b strings.Builder
	b.WriteString(head("Settings · Theme") + "\n")
	b.WriteString(dim("Drop a *.json theme into ~/.config/cairn/themes to add your own.") + "\n\n")

	// Theme list.
	for i, t := range s.lib.Themes {
		marker := "  "
		nameStyle := lipgloss.NewStyle().Foreground(th.Text)
		if i == s.cursor {
			marker = lipgloss.NewStyle().Foreground(th.Primary).Bold(true).Render("› ")
			nameStyle = nameStyle.Bold(true).Foreground(th.Primary)
		}
		row := marker + nameStyle.Render(t.Label)
		if t.Name == s.savedName {
			row += "  " + lipgloss.NewStyle().Foreground(th.Success).Render("● active")
		}
		b.WriteString(row + "\n")
	}
	b.WriteString("\n")

	// Variant toggle.
	darkDot, lightDot := "○", "○"
	sel := lipgloss.NewStyle().Foreground(th.Primary).Bold(true)
	off := lipgloss.NewStyle().Foreground(th.Muted)
	dark, light := off.Render("dark"), off.Render("light")
	if s.mode == theme.ModeLight {
		lightDot = "●"
		light = sel.Render("light")
	} else {
		darkDot = "●"
		dark = sel.Render("dark")
	}
	b.WriteString(head("Variant") + "  " +
		sel.Render(darkDot) + " " + dark + "   " +
		sel.Render(lightDot) + " " + light + "   " +
		dim("(space toggles)") + "\n\n")

	// Preview: swatches + a live-rendered markdown line + a syntax-highlighted code
	// line + a status strip. This is what proves the theme reaches code tokens and
	// Markdown, not just the chrome.
	b.WriteString(head("Preview") + "\n")
	b.WriteString(swatchRow(th, inner) + "\n")
	b.WriteString(previewMarkdown(th, inner) + "\n")
	b.WriteString(previewCode(th) + "\n")
	b.WriteString(previewStatus(th) + "\n")

	// Footer key hints.
	hint := dim("↑/↓ pick · space dark/light · enter apply · esc cancel")

	body := lipgloss.JoinVertical(lipgloss.Left, b.String(), hint)
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(th.Focus).
		Background(th.Base).
		Padding(1, 2).
		Width(w).
		Render(body)
	return lipgloss.Place(s.width, s.height, lipgloss.Center, lipgloss.Center, box)
}

// swatchRow renders each semantic token as a small colored block with its name,
// wrapped to the inner width.
func swatchRow(th theme.Theme, width int) string {
	type tok struct {
		name string
		c    lipgloss.Color
	}
	toks := []tok{
		{"base", th.Base}, {"surface", th.Surface}, {"overlay", th.Overlay},
		{"text", th.Text}, {"muted", th.Muted}, {"primary", th.Primary},
		{"focus", th.Focus}, {"info", th.Info}, {"success", th.Success},
		{"warning", th.Warning}, {"danger", th.Danger}, {"accent2", th.Accent2},
		{"codeBg", th.CodeBg},
	}
	label := lipgloss.NewStyle().Foreground(th.Muted)
	var rows []string
	var line strings.Builder
	lineW := 0
	for _, t := range toks {
		chip := lipgloss.NewStyle().Background(t.c).Render("  ")
		cell := chip + " " + label.Render(t.name)
		cellW := 2 + 1 + len(t.name) + 2 // chip + space + name + gap
		if lineW+cellW > width && lineW > 0 {
			rows = append(rows, line.String())
			line.Reset()
			lineW = 0
		}
		line.WriteString(cell + "  ")
		lineW += cellW
	}
	if line.Len() > 0 {
		rows = append(rows, line.String())
	}
	return strings.Join(rows, "\n")
}

// previewMarkdown renders a tiny Markdown snippet through the real renderer so the
// preview reflects heading/link/inline-code theming exactly as the app draws it.
func previewMarkdown(th theme.Theme, width int) string {
	md := "**Markdown:** a heading tint, some `inline code`, and _emphasis_."
	out := renderMarkdown(md, width, th)
	// Collapse to the first non-empty rendered line — glamour pads with blank lines.
	for _, ln := range strings.Split(out, "\n") {
		if strings.TrimSpace(ansiSeq.ReplaceAllString(ln, "")) != "" {
			return ln
		}
	}
	return out
}

// previewCode renders one syntax-highlighted line of Go under the candidate theme,
// demonstrating that code tokens follow the palette.
func previewCode(th theme.Theme) string {
	ensureChromaStyle(th)
	hl := newHighlighter("preview.go")
	lead := lipgloss.NewStyle().Foreground(th.Muted).Render("Code:  ")
	return lead + hl.line(`func Ship(pr int) error { return nil } // #ok`)
}

// previewStatus renders a status strip using the status-role tokens: CI dots,
// a PR number, and a commit hash.
func previewStatus(th theme.Theme) string {
	dot := func(c lipgloss.Color) string { return lipgloss.NewStyle().Foreground(c).Render("●") }
	num := lipgloss.NewStyle().Foreground(th.Info).Render("#412")
	hash := lipgloss.NewStyle().Foreground(th.Accent2).Render("a5c3460")
	label := lipgloss.NewStyle().Foreground(th.Muted).Render
	return label("Status:  ") +
		dot(th.Success) + label(" pass  ") +
		dot(th.Warning) + label(" pending  ") +
		dot(th.Danger) + label(" fail    ") +
		num + "  " + hash
}
