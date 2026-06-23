package tui

import (
	"strings"
	"sync"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/lipgloss"
	"github.com/dotnetemmanuel/cairn/internal/theme"
)

// Markdown bodies (PR descriptions, comments, reviews) arrive as GitHub-flavored
// Markdown. We render them with glamour so headings, lists, links, emphasis and
// code blocks read as formatted text instead of raw syntax.
//
// glamour TermRenderers are comparatively expensive to build, so we cache one
// per render width. View() is called on every frame; without the cache we'd
// reconstruct the renderer (and re-parse the style) constantly. The style is
// derived from the active theme, so the cache is invalidated if the theme's
// colors change (rare — effectively once per session).
var (
	mdMu        sync.Mutex
	mdStyleKey  string
	mdStyle     ansi.StyleConfig
	mdRenderers = map[int]*glamour.TermRenderer{}
)

// renderMarkdown renders GitHub-flavored Markdown to styled, width-wrapped text.
// It falls back to plain word-wrapping (wrap) if glamour is unavailable for the
// requested width, so a renderer failure degrades gracefully rather than blanking
// the body.
func renderMarkdown(s string, width int, th theme.Theme) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if width < 8 {
		width = 8
	}
	r := mdRenderer(width, th)
	if r == nil {
		return wrap(s, width)
	}
	out, err := r.Render(s)
	if err != nil {
		return wrap(s, width)
	}
	// glamour brackets the document with blank lines (Document BlockPrefix/Suffix);
	// trim those so the body sits flush with our own headers and dividers. Trim
	// only newlines — leading spaces inside code blocks are meaningful.
	return strings.Trim(out, "\n")
}

func mdRenderer(width int, th theme.Theme) *glamour.TermRenderer {
	mdMu.Lock()
	defer mdMu.Unlock()

	key := string(th.Text) + "|" + string(th.Focus) + "|" + string(th.Primary) +
		"|" + string(th.Info) + "|" + string(th.Accent2) + "|" + string(th.Surface) +
		"|" + string(th.Warning) + "|" + string(th.Muted) + "|" + string(th.Overlay)
	if key != mdStyleKey {
		mdStyleKey = key
		mdStyle = cairnGlamourStyle(th)
		mdRenderers = map[int]*glamour.TermRenderer{}
	}

	if r, ok := mdRenderers[width]; ok {
		return r
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithStyles(mdStyle),
		glamour.WithWordWrap(width),
		glamour.WithEmoji(),
		// Honor the same color profile as the rest of the TUI. Glamour otherwise
		// defaults to TrueColor regardless of the terminal, which would emit escape
		// codes even when lipgloss is rendering plain (e.g. non-TTY / tests).
		glamour.WithColorProfile(lipgloss.ColorProfile()),
	)
	if err != nil {
		return nil
	}
	mdRenderers[width] = r
	return r
}

// cairnGlamourStyle adapts glamour's dark style to Cairn's theme: it zeroes the
// document/code-block margins (so our own panes own the horizontal space and the
// wrap width is honored) and maps the most visible elements onto theme tokens.
// It copies the default by value and only reassigns pointer fields, so the shared
// package default is never mutated.
func cairnGlamourStyle(th theme.Theme) ansi.StyleConfig {
	s := styles.DarkStyleConfig

	sptr := func(c lipgloss.Color) *string { v := string(c); return &v }
	uptr := func(u uint) *uint { return &u }
	bptr := func(b bool) *bool { return &b }

	// Own the full pane width: no outer margin, no leading/trailing blank lines.
	s.Document.Margin = uptr(0)
	s.Document.BlockPrefix = ""
	s.Document.BlockSuffix = ""
	s.Document.Color = sptr(th.Text)
	s.Paragraph.Color = sptr(th.Text)
	s.Text.Color = sptr(th.Text)

	// Headings in the focus color; H1 loses its filled background block (too loud
	// inside a comment) in favor of a "# " prefix like the lower levels.
	s.Heading.Color = sptr(th.Focus)
	s.Heading.Bold = bptr(true)
	s.H1.BackgroundColor = nil
	s.H1.Prefix = "# "
	s.H1.Suffix = ""
	s.H1.Color = sptr(th.Primary)
	s.H1.Bold = bptr(true)

	// Links and inline code in their dashboard-equivalent colors.
	s.Link.Color = sptr(th.Info)
	s.LinkText.Color = sptr(th.Info)
	s.Code.Color = sptr(th.Accent2)
	s.Code.BackgroundColor = sptr(th.Surface)

	s.Emph.Color = sptr(th.Warning)
	s.Strong.Color = sptr(th.Text)
	s.BlockQuote.Color = sptr(th.Muted)
	s.HorizontalRule.Color = sptr(th.Overlay)
	s.Item.Color = sptr(th.Text)
	s.Enumeration.Color = sptr(th.Text)

	// Fenced code blocks: drop the indenting margin so they fit narrow panes; keep
	// glamour's chroma syntax theme for the contents.
	s.CodeBlock.Margin = uptr(0)

	return s
}
