package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/dotnetemmanuel/cairn/internal/gh"
	"github.com/dotnetemmanuel/cairn/internal/theme"
)

func TestExpandTabs(t *testing.T) {
	cases := map[string]string{
		"\tx":     "    x",     // one leading tab -> next 4-col stop
		"\t\tx":   "        x", // two tabs -> 8 cols
		"ab\tc":   "ab  c",     // mid-line tab aligns to col 4
		"abcd\te": "abcd    e", // tab at a stop -> full tab width
		"no tabs": "no tabs",   // unchanged
	}
	for in, want := range cases {
		if got := expandTabs(in); got != want {
			t.Errorf("expandTabs(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestDiffNoRowExceedsWidth guards the soft-wrap invariant: no rendered visual
// row may be wider than the diff pane, even for tab-indented lines (which
// ansi.StringWidth otherwise under-measures, causing terminal hard-wrap to col 0).
func TestDiffNoRowExceedsWidth(t *testing.T) {
	th := theme.New(theme.DefaultPalette())
	patch := "@@ -1,2 +1,2 @@\n" +
		"-\t\tshortOld()\n" +
		"+\t\tthis_is_a_fairly_long_added_line_with_leading_tabs_that_must_wrap()\n" +
		" \tcontext line with a tab and more words to push it over the edge here too"
	width := 40
	content, _ := renderDiff(th, gh.FileDiff{Filename: "x.go", Patch: patch}, width, -1, -1, nil)
	for _, row := range strings.Split(content, "\n") {
		if w := ansi.StringWidth(row); w > width {
			t.Errorf("row exceeds pane width %d: width=%d %q", width, w, ansi.Strip(row))
		}
	}
}
