package tui

import (
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/dotnetemmanuel/cairn/internal/gh"
	"github.com/dotnetemmanuel/cairn/internal/theme"
)

// chromaStyle/chromaFormatter are shared: a dark style and a truecolor TTY
// formatter. Built once.
var (
	chromaStyle     = styles.Get("github-dark")
	chromaFormatter = formatters.Get("terminal16m")
)

func init() {
	if chromaStyle == nil {
		chromaStyle = styles.Fallback
	}
	if chromaFormatter == nil {
		chromaFormatter = formatters.Fallback
	}
}

// highlighter syntax-highlights individual code lines for one file. Per-line
// tokenization loses cross-line context (block comments etc.), which is an
// acceptable trade for diffs that show only changed regions.
type highlighter struct {
	lexer chroma.Lexer
}

func newHighlighter(filename string) highlighter {
	l := lexers.Match(filename)
	if l == nil {
		l = lexers.Fallback
	}
	return highlighter{lexer: chroma.Coalesce(l)}
}

func (h highlighter) line(code string) string {
	if code == "" {
		return ""
	}
	it, err := h.lexer.Tokenise(nil, code)
	if err != nil {
		return code
	}
	var b strings.Builder
	if err := chromaFormatter.Format(&b, chromaStyle, it); err != nil {
		return code
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderDiff turns a file's patch into a styled, width-bounded block for the
// diff viewport: hunk headers in focus blue, added/removed lines marked in the
// gutter (green/red), code syntax-highlighted by chroma.
func renderDiff(th theme.Theme, f gh.FileDiff, width int) string {
	if width < 4 {
		width = 4
	}
	if strings.TrimSpace(f.Patch) == "" {
		note := "(no textual diff — binary file, or too large to display)"
		return lipgloss.NewStyle().Foreground(th.Muted).Render(note)
	}

	hl := newHighlighter(f.Filename)
	addMarker := lipgloss.NewStyle().Foreground(th.Success)
	delMarker := lipgloss.NewStyle().Foreground(th.Danger)
	ctxMarker := lipgloss.NewStyle().Foreground(th.Muted)
	hunkStyle := lipgloss.NewStyle().Foreground(th.Focus).Bold(true)
	metaStyle := lipgloss.NewStyle().Foreground(th.Muted)

	var lines []string
	for _, raw := range strings.Split(f.Patch, "\n") {
		var rendered string
		switch {
		case strings.HasPrefix(raw, "@@"):
			rendered = hunkStyle.Render(raw)
		case strings.HasPrefix(raw, "+"):
			rendered = addMarker.Render("+") + hl.line(raw[1:])
		case strings.HasPrefix(raw, "-"):
			rendered = delMarker.Render("-") + hl.line(raw[1:])
		case strings.HasPrefix(raw, "\\"):
			rendered = metaStyle.Render(raw)
		default:
			code := raw
			if strings.HasPrefix(raw, " ") {
				code = raw[1:]
			}
			rendered = ctxMarker.Render(" ") + hl.line(code)
		}
		lines = append(lines, ansi.Truncate(rendered, width, "…"))
	}
	return strings.Join(lines, "\n")
}

// hunkLineIndexes returns the line offsets (within the rendered block) where
// hunks start, for hunk-to-hunk navigation.
func hunkLineIndexes(patch string) []int {
	var idx []int
	for i, raw := range strings.Split(patch, "\n") {
		if strings.HasPrefix(raw, "@@") {
			idx = append(idx, i)
		}
	}
	return idx
}
