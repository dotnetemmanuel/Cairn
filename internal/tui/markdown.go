package tui

import (
	"regexp"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	chromastyles "github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/lipgloss"
	"github.com/dotnetemmanuel/cairn/internal/theme"
	"github.com/muesli/termenv"
)

// Sentinels wrapped around fenced code blocks (via glamour's CodeBlock
// BlockPrefix/BlockSuffix) so the post-render pass can find a block's exact line
// range — which a prose line that merely contains inline code would not match.
// NUL bytes never occur in rendered Markdown, and they're stripped before display.
const (
	codeBlockStart = "\x00cb\x00"
	codeBlockEnd   = "\x00/cb\x00"
)

// ansiSeq matches SGR (color/style) escape sequences, used to measure the visible
// width of a styled line and to locate escapes while scanning.
var ansiSeq = regexp.MustCompile("\x1b\\[[0-9;]*m")

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
	// Square off code blocks into a solid rectangle (see padCodeBlocks) before the
	// outer newline trim.
	out = padCodeBlocks(out, th)
	// Colorize @mentions so people stand out from blue inline code and muted prose.
	out = colorizeMentions(out, th)
	// glamour brackets the document with blank lines (Document BlockPrefix/Suffix);
	// trim those so the body sits flush with our own headers and dividers. Trim
	// only newlines — leading spaces inside code blocks are meaningful.
	return strings.Trim(out, "\n")
}

// viewerLogin is the authenticated user's login, set once the viewer loads. Used
// to make your own @mention pop brighter than others. Package-level so the
// stateless renderMarkdown can reach it without threading it through every call.
var viewerLogin string

// mentionRE matches an @mention at a word boundary: an @ not preceded by a word
// char (so it skips emails like a@b), then a GitHub-style login.
var mentionRE = regexp.MustCompile(`(^|[^\w@/])(@[A-Za-z0-9][A-Za-z0-9-]{0,38})`)

// colorizeMentions tints @mentions in already-rendered markdown: your own login
// in the focus accent (bold), everyone else in the brand accent — distinct from
// blue inline code. After the mention it reasserts the body text color so the
// rest of the line keeps glamour's flow. (A light touch: it can occasionally hit
// an @token inside code, which is harmless.)
func colorizeMentions(s string, th theme.Theme) string {
	me := lipgloss.NewStyle().Foreground(th.Focus).Bold(true)
	other := lipgloss.NewStyle().Foreground(th.Primary)
	resume := "\x1b[39m" // reset foreground to the terminal default, not a full SGR reset
	return mentionRE.ReplaceAllStringFunc(s, func(m string) string {
		loc := mentionRE.FindStringSubmatch(m)
		lead, at := loc[1], loc[2]
		style := other
		if viewerLogin != "" && strings.EqualFold(at, "@"+viewerLogin) {
			style = me
		}
		return lead + style.Render(at) + resume
	})
}

func mdRenderer(width int, th theme.Theme) *glamour.TermRenderer {
	mdMu.Lock()
	defer mdMu.Unlock()

	// Full fingerprint: cairnGlamourStyle (code blocks included) consumes Success
	// and Danger too, so a partial key would leave those orphaned — stale code-block
	// strings/errors if only they changed.
	key := theme.Fingerprint(th)
	if key != mdStyleKey {
		mdStyleKey = key
		mdStyle = cairnGlamourStyle(th)
		mdRenderers = map[int]*glamour.TermRenderer{}
		// glamour registers its code-block chroma style under the fixed name "charm"
		// and refuses to re-register if it already exists (ansi/codeblock.go), so the
		// FIRST theme rendered bakes the code-block colors into chroma's global
		// registry forever — toggling the theme then leaves stale code backgrounds.
		// Evict that entry so glamour re-registers with the new palette.
		delete(chromastyles.Registry, "charm")
	}

	if r, ok := mdRenderers[width]; ok {
		return r
	}
	opts := []glamour.TermRendererOption{
		glamour.WithStyles(mdStyle),
		glamour.WithWordWrap(width),
		glamour.WithEmoji(),
		// Honor the same color profile as the rest of the TUI. Glamour otherwise
		// defaults to TrueColor regardless of the terminal, which would emit escape
		// codes even when lipgloss is rendering plain (e.g. non-TTY / tests).
		glamour.WithColorProfile(lipgloss.ColorProfile()),
	}
	// Glamour highlights code blocks with the 256-color chroma formatter by default,
	// which quantizes our code-block background away from the exact Overlay used for
	// inline code. On a truecolor terminal, force the truecolor formatter so the two
	// backgrounds match precisely; otherwise keep glamour's 256-color default.
	if lipgloss.ColorProfile() == termenv.TrueColor {
		opts = append(opts, glamour.WithChromaFormatter("terminal16m"))
	}
	r, err := glamour.NewTermRenderer(opts...)
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
	// Inline code: the Accent2 teal reads well on the dark code panel, but on the
	// light sand panel it's too faint — fall back to the strong body text color
	// there (the tinted background already marks it as code).
	codeFg := th.Accent2
	if theme.IsLight(th) {
		codeFg = th.Text
	}
	s.Code.Color = sptr(codeFg)
	s.Code.BackgroundColor = sptr(th.CodeBg)

	s.Emph.Color = sptr(th.Warning)
	s.Strong.Color = sptr(th.Text)
	s.BlockQuote.Color = sptr(th.Muted)
	s.HorizontalRule.Color = sptr(th.Overlay)
	s.Item.Color = sptr(th.Text)
	s.Enumeration.Color = sptr(th.Text)

	// Fenced code blocks: drop the indenting margin so they fit narrow panes; keep
	// glamour's chroma syntax theme for the contents, but give the block an Overlay
	// backdrop so it reads as a distinct unit (matching inline code above).
	s.CodeBlock.Margin = uptr(0)
	// chroma's terminal formatter deliberately discards the *block-level* background
	// (clearBackground() in tty_indexed.go zeroes the "Background" token), so setting
	// it there paints nothing. The backdrop has to be tinted onto every *token* style
	// instead. This tints the code itself (and its indentation); padCodeBlocks then
	// squares the block off into a solid rectangle by padding to the longest line.
	//
	// s.CodeBlock.Chroma is a pointer shared with the package default, so copy it by
	// value before mutating, or we'd corrupt the shared style for everyone.
	if s.CodeBlock.Chroma != nil {
		c := *s.CodeBlock.Chroma
		bg := string(th.CodeBg)
		// Recolor chroma's syntax tokens onto Cairn's semantic palette so code blocks
		// match the rest of the TUI instead of glamour's built-in dark theme. Each token
		// gets the Overlay backdrop plus a themed foreground; other attributes (bold,
		// italic) carry over from the copied default. Tinting Text also covers
		// whitespace/indentation and any token the formatter falls back to.
		set := func(p *ansi.StylePrimitive, fg lipgloss.Color) {
			v := string(fg)
			p.Color = &v
			p.BackgroundColor = &bg
		}
		set(&c.Text, th.Text)
		set(&c.Error, th.Danger)
		set(&c.Comment, th.Muted)
		set(&c.CommentPreproc, th.Muted)
		set(&c.Keyword, th.Primary)
		set(&c.KeywordReserved, th.Primary)
		set(&c.KeywordNamespace, th.Primary)
		set(&c.KeywordType, th.Focus)
		set(&c.Operator, th.Focus)
		set(&c.Punctuation, th.Muted)
		set(&c.Name, th.Text)
		set(&c.NameBuiltin, th.Focus)
		set(&c.NameTag, th.Info)
		set(&c.NameAttribute, th.Info)
		set(&c.NameClass, th.Focus)
		set(&c.NameConstant, th.Accent2)
		set(&c.NameDecorator, th.Info)
		set(&c.NameException, th.Danger)
		set(&c.NameFunction, th.Focus)
		set(&c.NameOther, th.Text)
		set(&c.Literal, th.Accent2)
		set(&c.LiteralNumber, th.Accent2)
		set(&c.LiteralDate, th.Accent2)
		set(&c.LiteralString, th.Success)
		set(&c.LiteralStringEscape, th.Warning)
		set(&c.GenericDeleted, th.Danger)
		set(&c.GenericEmph, th.Text)
		set(&c.GenericInserted, th.Success)
		set(&c.GenericStrong, th.Text)
		set(&c.GenericSubheading, th.Text)
		set(&c.Background, th.Text)
		s.CodeBlock.Chroma = &c
	}
	// And the fallback (no-language / non-color) path renders via this primitive.
	s.CodeBlock.StyleBlock.StylePrimitive.BackgroundColor = sptr(th.CodeBg)
	// Bracket each block with sentinels so padCodeBlocks can square it into a solid
	// rectangle. glamour renders these with the document style (no background), and
	// they're stripped before display.
	s.CodeBlock.StyleBlock.StylePrimitive.BlockPrefix = codeBlockStart
	s.CodeBlock.StyleBlock.StylePrimitive.BlockSuffix = codeBlockEnd

	return s
}

// padCodeBlocks turns each fenced code block (delimited by the codeBlockStart /
// codeBlockEnd sentinels) into a solid background rectangle: it trims glamour's
// per-line padding, finds the widest line in the block, and re-pads every line to
// that width with the theme's Overlay background. Lines outside code blocks — which
// may contain inline code on the same surface tint — are left untouched.
func padCodeBlocks(out string, th theme.Theme) string {
	if !strings.Contains(out, codeBlockStart) {
		return out
	}
	lines := strings.Split(out, "\n")
	result := make([]string, 0, len(lines))
	for i := 0; i < len(lines); i++ {
		if !strings.Contains(lines[i], codeBlockStart) {
			result = append(result, lines[i])
			continue
		}
		// Gather the block: the start sentinel is glued to the first code line; the
		// end sentinel sits alone on its own trailing line, which we drop.
		var block []string
		block = append(block, strings.Replace(lines[i], codeBlockStart, "", 1))
		i++
		for i < len(lines) && !strings.Contains(lines[i], codeBlockEnd) {
			block = append(block, lines[i])
			i++
		}
		// i now rests on the end-sentinel line (or past the slice); the loop's i++
		// then steps past it, discarding that artifact line.
		result = append(result, squareCodeBlock(block, th)...)
	}
	return strings.Join(result, "\n")
}

// squareCodeBlock pads every line of a single code block to the width of its widest
// line, backgrounded with CodeBg so the block reads as one rectangle.
func squareCodeBlock(block []string, th theme.Theme) []string {
	contents := make([]string, len(block))
	widths := make([]int, len(block))
	target := 0
	for j, ln := range block {
		// Expand tabs (Go code blocks use them) so width accounting matches display.
		ln = strings.ReplaceAll(ln, "\t", "    ")
		content := ln[:lastVisibleByte(ln)]
		contents[j] = content
		widths[j] = lipgloss.Width(ansiSeq.ReplaceAllString(content, ""))
		if widths[j] > target {
			target = widths[j]
		}
	}
	bg := lipgloss.NewStyle().Background(th.CodeBg)
	for j := range block {
		// Reset after the trimmed content, then lay down a backgrounded run of spaces
		// to reach the common width. The content's last cell already carries Overlay,
		// so the seam is invisible.
		block[j] = contents[j] + "\x1b[0m" + bg.Render(strings.Repeat(" ", target-widths[j]))
	}
	return block
}

// lastVisibleByte returns the byte offset just past the last non-whitespace rune in
// a styled line, skipping ANSI escape sequences. Everything after it (glamour's
// trailing pad spaces, empty escape pairs) is dropped before re-padding.
func lastVisibleByte(line string) int {
	escapes := ansiSeq.FindAllStringIndex(line, -1)
	last, pos, e := 0, 0, 0
	for pos < len(line) {
		if e < len(escapes) && escapes[e][0] == pos {
			pos = escapes[e][1]
			e++
			continue
		}
		r, size := utf8.DecodeRuneInString(line[pos:])
		if !unicode.IsSpace(r) {
			last = pos + size
		}
		pos += size
	}
	return last
}
