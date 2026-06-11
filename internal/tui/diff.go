package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/dotnetemmanuel/cairn/internal/gh"
	"github.com/dotnetemmanuel/cairn/internal/theme"
)

// chromaStyle is the syntax-highlighting palette, built lazily from the active
// theme (see ensureChromaStyle) so highlighted code matches Cairn's colors —
// the Event Horizon palette by default. chromaFormatter is a truecolor TTY
// formatter, built once.
var (
	chromaStyle     *chroma.Style
	chromaFormatter = formatters.Get("terminal16m")
)

func init() {
	if chromaFormatter == nil {
		chromaFormatter = formatters.Fallback
	}
}

// ensureChromaStyle builds the syntax style from the theme palette on first use.
// Token→role mapping mirrors the Event Horizon (Aether) editor theme: keywords
// in primary, functions/attributes in focus, strings in success-green, types in
// warning-peach, numbers/constants in danger, comments muted. The theme is
// fixed per process, so building once is safe.
func ensureChromaStyle(th theme.Theme) {
	if chromaStyle != nil {
		return
	}
	hx := func(c lipgloss.Color) string { return string(c) }
	chromaStyle = chroma.MustNewStyle("cairn", chroma.StyleEntries{
		chroma.Background:          hx(th.Text),
		chroma.Comment:             hx(th.Muted) + " italic",
		chroma.CommentPreproc:      hx(th.Muted),
		chroma.Keyword:             hx(th.Primary),
		chroma.KeywordConstant:     hx(th.Danger),
		chroma.KeywordType:         hx(th.Warning),
		chroma.Operator:            hx(th.Primary),
		chroma.Name:                hx(th.Text),
		chroma.NameFunction:        hx(th.Focus),
		chroma.NameAttribute:       hx(th.Focus),
		chroma.NameClass:           hx(th.Warning),
		chroma.NameTag:             hx(th.Danger),
		chroma.NameBuiltin:         hx(th.Danger),
		chroma.NameConstant:        hx(th.Danger),
		chroma.LiteralString:       hx(th.Success),
		chroma.LiteralStringEscape: hx(th.Danger),
		chroma.LiteralNumber:       hx(th.Danger),
		chroma.GenericHeading:      hx(th.Warning) + " bold",
		chroma.GenericSubheading:   hx(th.Warning),
		chroma.GenericEmph:         "italic",
		chroma.GenericStrong:       "bold",
		chroma.Error:               hx(th.Danger),
	})
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
// gutter (green/red), code syntax-highlighted by chroma. The header of the
// activeHunk'th hunk (0-based) is marked with a ▶ so n/N navigation is visible
// even when the whole diff fits on screen (pass -1 for none). The line at
// index cursor gets a ▌ line cursor (pass -1 for none). Lines whose rendered
// index appears in commentCounts get a 💬N badge.
func renderDiff(th theme.Theme, f gh.FileDiff, width, activeHunk, cursor int, commentCounts map[int]int) string {
	if width < 6 {
		width = 6
	}
	if strings.TrimSpace(f.Patch) == "" {
		note := "(no textual diff — binary file, or too large to display)"
		return lipgloss.NewStyle().Foreground(th.Muted).Render(note)
	}

	ensureChromaStyle(th)
	hl := newHighlighter(f.Filename)
	addMarker := lipgloss.NewStyle().Foreground(th.Success)
	delMarker := lipgloss.NewStyle().Foreground(th.Danger)
	ctxMarker := lipgloss.NewStyle().Foreground(th.Muted)
	hunkStyle := lipgloss.NewStyle().Foreground(th.Focus).Bold(true)
	activeStyle := lipgloss.NewStyle().Foreground(th.Base).Background(th.Focus).Bold(true)
	metaStyle := lipgloss.NewStyle().Foreground(th.Muted)
	cursorStyle := lipgloss.NewStyle().Foreground(th.Focus).Bold(true)
	badgeStyle := lipgloss.NewStyle().Foreground(th.Info)

	hunkIdx := -1
	var lines []string
	for i, raw := range strings.Split(f.Patch, "\n") {
		var rendered string
		switch {
		case strings.HasPrefix(raw, "@@"):
			hunkIdx++
			if hunkIdx == activeHunk {
				rendered = activeStyle.Render("▶ " + raw)
			} else {
				rendered = hunkStyle.Render("  " + raw)
			}
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

		// One column on the left for the line cursor keeps every line aligned.
		cur := " "
		if i == cursor {
			cur = cursorStyle.Render("▌")
		}
		// A trailing 💬N badge advertises inline comments; reserve its width so
		// the line still fits and the viewport never wraps.
		badge := ""
		inner := width - 1
		if n := commentCounts[i]; n > 0 {
			badge = badgeStyle.Render(fmt.Sprintf(" 💬%d", n))
			inner -= lipgloss.Width(badge)
		}
		if inner < 4 {
			inner = 4
		}
		lines = append(lines, cur+ansi.Truncate(rendered, inner, "…")+badge)
	}
	return strings.Join(lines, "\n")
}

// diffLineMeta describes, for one rendered diff line, which side of the diff it
// belongs to and the file line number there — the anchor a new inline comment
// needs. side is "" for lines that can't be commented on (hunk headers, the
// "\ No newline" marker).
type diffLineMeta struct {
	side string // "RIGHT" (new), "LEFT" (old), or "" (not commentable)
	line int    // file line number on that side
	code string // the line's code without the +/-/space gutter
}

// patchLineMeta walks a unified patch and returns one entry per line (1:1 with
// renderDiff's output), tracking old/new line numbers across hunks.
func patchLineMeta(patch string) []diffLineMeta {
	var out []diffLineMeta
	oldLine, newLine := 0, 0
	for _, raw := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(raw, "@@"):
			oldLine, newLine = parseHunkStarts(raw)
			out = append(out, diffLineMeta{})
		case strings.HasPrefix(raw, "+"):
			out = append(out, diffLineMeta{side: "RIGHT", line: newLine, code: raw[1:]})
			newLine++
		case strings.HasPrefix(raw, "-"):
			out = append(out, diffLineMeta{side: "LEFT", line: oldLine, code: raw[1:]})
			oldLine++
		case strings.HasPrefix(raw, "\\"):
			out = append(out, diffLineMeta{})
		default:
			code := raw
			if strings.HasPrefix(raw, " ") {
				code = raw[1:]
			}
			out = append(out, diffLineMeta{side: "RIGHT", line: newLine, code: code})
			oldLine++
			newLine++
		}
	}
	return out
}

// parseHunkStarts pulls the old/new starting line numbers out of an
// "@@ -a,b +c,d @@" header.
func parseHunkStarts(header string) (oldStart, newStart int) {
	for _, f := range strings.Fields(header) {
		switch {
		case strings.HasPrefix(f, "-"):
			oldStart = atoiBefore(f[1:])
		case strings.HasPrefix(f, "+"):
			newStart = atoiBefore(f[1:])
		}
	}
	return
}

// atoiBefore parses the integer prefix of "12,7" or "12".
func atoiBefore(s string) int {
	if i := strings.IndexByte(s, ','); i >= 0 {
		s = s[:i]
	}
	n, _ := strconv.Atoi(s)
	return n
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
