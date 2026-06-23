package tui

import (
	"strings"
	"testing"

	"github.com/dotnetemmanuel/cairn/internal/gh"
	"github.com/dotnetemmanuel/cairn/internal/theme"
)

func TestReviewGlyphDistinguishesRequestTarget(t *testing.T) {
	th := theme.New(theme.DefaultPalette())
	strip := func(s string) string { // drop ANSI for content assertions
		var b strings.Builder
		in := false
		for _, r := range s {
			switch {
			case r == '\x1b':
				in = true
			case in && (r == 'm'):
				in = false
			case !in:
				b.WriteRune(r)
			}
		}
		return b.String()
	}

	fromMe := reviewGlyph(th, gh.Item{ReviewReqFromMe: true, Review: gh.ReviewRequired})
	fromOthers := reviewGlyph(th, gh.Item{ReviewReqFromOthers: true, Review: gh.ReviewRequired})
	if strip(fromMe) != "◆" {
		t.Errorf("requested-from-me glyph = %q, want ◆", strip(fromMe))
	}
	if strip(fromOthers) != "◇" {
		t.Errorf("requested-from-others glyph = %q, want ◇", strip(fromOthers))
	}
	if fromMe == fromOthers {
		t.Error("from-me and from-others glyphs must differ")
	}
	// A direct decision still wins when no request targets the viewer.
	if strip(reviewGlyph(th, gh.Item{Review: gh.ReviewApproved})) != "✓" {
		t.Error("approved should render ✓")
	}
}

func TestStyleTitleTagsDrafts(t *testing.T) {
	th := theme.New(theme.DefaultPalette())
	strip := func(s string) string {
		var b strings.Builder
		in := false
		for _, r := range s {
			switch {
			case r == '\x1b':
				in = true
			case in && r == 'm':
				in = false
			case !in:
				b.WriteRune(r)
			}
		}
		return b.String()
	}

	if got := strip(styleTitle(th, "Add widget", "")); got != "Add widget" {
		t.Errorf("non-draft title = %q, want unchanged", got)
	}
	// The padded title carries the ASCII "draft " prefix; styleTitle must keep
	// it visible while coloring it apart from the title text.
	if got := strip(styleTitle(th, "DRAFT Add widget", "DRAFT ")); got != "DRAFT Add widget" {
		t.Errorf("draft title content = %q, want the tag preserved", got)
	}
}
