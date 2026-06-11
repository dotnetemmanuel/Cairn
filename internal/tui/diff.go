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
		l = lexerForExt(filename) // chroma misses a few extensions
	}
	if l == nil {
		l = lexers.Fallback
	}
	return highlighter{lexer: chroma.Coalesce(l)}
}

// lexerForExt maps extensions chroma doesn't recognize to the closest lexer.
// Razor-family files are HTML with embedded @C#, so HTML highlights them well.
func lexerForExt(filename string) chroma.Lexer {
	lower := strings.ToLower(filename)
	dot := strings.LastIndexByte(lower, '.')
	if dot < 0 {
		return nil
	}
	switch lower[dot+1:] {
	case "cshtml", "razor", "vbhtml", "aspx", "ascx", "master":
		return lexers.Get("HTML")
	}
	return nil
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
// renderDiff returns the rendered block and rowAt, a map from patch-line index
// to the first visual row that line occupies. Because long lines soft-wrap onto
// several visual rows, callers must translate patch-line positions (hunks, the
// cursor) through rowAt before scrolling the viewport.
func renderDiff(th theme.Theme, f gh.FileDiff, width, activeHunk, cursor int, commentCounts map[int]int) (string, []int) {
	if width < 6 {
		width = 6
	}
	if strings.TrimSpace(f.Patch) == "" {
		note := "(no textual diff — binary file, or too large to display)"
		return lipgloss.NewStyle().Foreground(th.Muted).Render(note), nil
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

	// Line-number gutter: old | new, each right-aligned to gw digits. A number
	// is blank on the side where the line doesn't exist (new side of a deletion,
	// old side of an addition).
	gw := diffGutterWidth(f.Patch)
	gutW := 2*gw + 1 // old + │ + new
	gutStyle := lipgloss.NewStyle().Foreground(th.Muted)
	sepStyle := lipgloss.NewStyle().Foreground(th.Overlay)
	num := func(n int) string {
		if n <= 0 {
			return strings.Repeat(" ", gw)
		}
		return fmt.Sprintf("%*d", gw, n)
	}
	// gnum renders "old│new" with a subtle divider between the two columns.
	gnum := func(o, n int) string {
		return gutStyle.Render(num(o)) + sepStyle.Render("│") + gutStyle.Render(num(n))
	}
	blankGutter := gutStyle.Render(strings.Repeat(" ", gutW))
	// Faint add/del line tints, derived from the theme's success/danger.
	addBG := blendRGB(th.Base, th.Success, 0.16)
	delBG := blendRGB(th.Base, th.Danger, 0.16)
	oldLine, newLine := 0, 0

	hunkIdx := -1
	var out []string
	var rowAt []int
	for i, raw := range strings.Split(f.Patch, "\n") {
		var rendered, gutter, bg string
		switch {
		case strings.HasPrefix(raw, "@@"):
			oldLine, newLine = parseHunkStarts(raw)
			gutter = blankGutter
			hunkIdx++
			if hunkIdx == activeHunk {
				rendered = activeStyle.Render("▶ " + raw)
			} else {
				rendered = hunkStyle.Render("  " + raw)
			}
		case strings.HasPrefix(raw, "+"):
			gutter = gnum(0, newLine)
			bg = addBG
			newLine++
			rendered = addMarker.Render("+") + hl.line(raw[1:])
		case strings.HasPrefix(raw, "-"):
			gutter = gnum(oldLine, 0)
			bg = delBG
			oldLine++
			rendered = delMarker.Render("-") + hl.line(raw[1:])
		case strings.HasPrefix(raw, "\\"):
			gutter = blankGutter
			rendered = metaStyle.Render(raw)
		default:
			gutter = gnum(oldLine, newLine)
			oldLine++
			newLine++
			code := raw
			if strings.HasPrefix(raw, " ") {
				code = raw[1:]
			}
			rendered = ctxMarker.Render(" ") + hl.line(code)
		}

		// One column on the left for the line cursor keeps every line aligned,
		// then the line-number gutter (old │ new).
		cur := " "
		if i == cursor {
			cur = cursorStyle.Render("▌")
		}
		// A 💬N badge advertises inline comments; reserve its width on the first
		// visual row so the badge always fits.
		badge, badgeW := "", 0
		if n := commentCounts[i]; n > 0 {
			badge = badgeStyle.Render(fmt.Sprintf(" 💬%d", n))
			badgeW = lipgloss.Width(badge)
		}
		// Soft-wrap the code to the available width so no change is hidden off
		// the right edge. Word-aware (ansi.Wrap) so wraps fall on boundaries
		// where possible, hard-breaking only tokens longer than the line. The
		// gutter shows on the first row; continuation rows are blank-gutter.
		avail := width - 1 - gutW - 1 - badgeW
		if avail < 8 {
			avail = 8
		}
		rows := strings.Split(ansi.Wrap(rendered, avail, ""), "\n")
		rowAt = append(rowAt, len(out))
		for j, r := range rows {
			g := blankGutter
			b := ""
			if j == 0 {
				g = gutter
				b = badge
			}
			// Tint the code area (after the gutter) on added/removed lines.
			code := r
			if bg != "" {
				code = tintRow(r, bg, avail)
			}
			out = append(out, cur+g+" "+code+b)
		}
	}
	return strings.Join(out, "\n"), rowAt
}

// hexRGB parses "#rrggbb" into 0-255 components.
func hexRGB(h string) (int, int, int) {
	h = strings.TrimPrefix(string(h), "#")
	if len(h) != 6 {
		return 0, 0, 0
	}
	r, _ := strconv.ParseInt(h[0:2], 16, 0)
	g, _ := strconv.ParseInt(h[2:4], 16, 0)
	b, _ := strconv.ParseInt(h[4:6], 16, 0)
	return int(r), int(g), int(b)
}

// blendRGB mixes base toward accent by t (0..1) and returns an "R;G;B" string
// for an SGR background — used for the faint add/del line tints derived from the
// active theme.
func blendRGB(base, accent lipgloss.Color, t float64) string {
	br, bg, bb := hexRGB(string(base))
	ar, ag, ab := hexRGB(string(accent))
	mix := func(b, a int) int { return b + int((float64(a)-float64(b))*t) }
	return fmt.Sprintf("%d;%d;%d", mix(br, ar), mix(bg, ag), mix(bb, ab))
}

// tintRow gives a wrapped code row a full-width background: it re-asserts the bg
// after each ANSI reset (chroma emits per-token resets that would otherwise
// clear it) and pads to width so the tint spans the whole line.
func tintRow(row, rgb string, width int) string {
	open := "\x1b[48;2;" + rgb + "m"
	body := open + strings.ReplaceAll(row, "\x1b[0m", "\x1b[0m"+open)
	if pad := width - ansi.StringWidth(row); pad > 0 {
		body += strings.Repeat(" ", pad)
	}
	return body + "\x1b[0m"
}

// diffGutterWidth returns the per-side digit width needed for line numbers in a
// patch (min 2), by finding the largest old/new line number it reaches.
func diffGutterWidth(patch string) int {
	maxN, oldLine, newLine := 0, 0, 0
	bump := func(n int) {
		if n > maxN {
			maxN = n
		}
	}
	for _, raw := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(raw, "@@"):
			oldLine, newLine = parseHunkStarts(raw)
		case strings.HasPrefix(raw, "+"):
			bump(newLine)
			newLine++
		case strings.HasPrefix(raw, "-"):
			bump(oldLine)
			oldLine++
		case strings.HasPrefix(raw, "\\"):
		default:
			bump(oldLine)
			bump(newLine)
			oldLine++
			newLine++
		}
	}
	if w := len(strconv.Itoa(maxN)); w > 2 {
		return w
	}
	return 2
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
