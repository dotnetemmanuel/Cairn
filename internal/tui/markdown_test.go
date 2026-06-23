package tui

import (
	"strings"
	"testing"

	"github.com/dotnetemmanuel/cairn/internal/theme"
)

// In the test color profile glamour emits no escapes, so we can assert directly
// on the rendered text: markdown markup must be transformed, not shown literally.
func TestRenderMarkdownTransformsMarkup(t *testing.T) {
	th := theme.New(theme.DefaultPalette())

	cases := []struct {
		name string
		in   string
		want string // substring that must appear
		gone string // substring that must NOT appear (raw markup)
	}{
		{"bold", "this is **bold** text", "bold", "**bold**"},
		{"bullet", "- first\n- second", "• first", "- first"},
		{"heading", "# Title", "Title", "# Title"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := renderMarkdown(c.in, 60, th)
			if !strings.Contains(out, c.want) {
				t.Errorf("rendered %q missing %q\ngot: %q", c.in, c.want, out)
			}
			if strings.Contains(out, c.gone) {
				t.Errorf("rendered %q still shows raw markup %q\ngot: %q", c.in, c.gone, out)
			}
		})
	}
}

func TestRenderMarkdownEmpty(t *testing.T) {
	th := theme.New(theme.DefaultPalette())
	if got := renderMarkdown("   \n  ", 40, th); got != "" {
		t.Errorf("blank body should render empty, got %q", got)
	}
}
