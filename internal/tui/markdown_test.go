package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/dotnetemmanuel/cairn/internal/theme"
	"github.com/muesli/termenv"
)

// TestThemeToggleCodeBlockBackground guards a glamour/chroma gotcha: glamour
// registers its code-block style under a fixed name ("charm") and won't
// re-register, so the first theme rendered used to bake the code background in
// forever. renderMarkdown evicts that cache on a theme change; this asserts a
// light render after a dark one carries only the light CodeBg, and vice versa.
func TestThemeToggleCodeBlockBackground(t *testing.T) {
	// SetColorProfile is global; restore it so other tests keep their Ascii profile.
	prev := lipgloss.ColorProfile()
	defer lipgloss.SetColorProfile(prev)
	defer func() { mdStyleKey = "" }() // force a clean rebuild under the restored profile
	lipgloss.SetColorProfile(termenv.TrueColor)
	dark := theme.Resolve(theme.ModeDark, theme.Palette{})
	light := theme.Resolve(theme.ModeLight, theme.Palette{})
	const darkBG = "48;2;46;48;62"     // dark CodeBg #2e303e
	const lightBG = "48;2;218;205;166" // light CodeBg #dacda6
	md := "```ts\nconst x = resolve('/login')\n```\n"

	mdStyleKey = "" // start from a clean renderer cache

	_ = renderMarkdown(md, 70, dark) // first render bakes "charm" in dark
	gotLight := renderMarkdown(md, 70, light)
	if strings.Contains(gotLight, darkBG) {
		t.Error("light render after a dark one still carries the dark code background (stale chroma style)")
	}
	if !strings.Contains(gotLight, lightBG) {
		t.Error("light render should use the light code background")
	}
	if strings.Contains(renderMarkdown(md, 70, dark), lightBG) {
		t.Error("dark render after a light one still carries the light code background")
	}
}

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
