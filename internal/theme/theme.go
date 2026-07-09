// Package theme defines Cairn's color palette and the Lip Gloss style set
// derived from it. The default is "Event Horizon" (Omarchy Horizon Dark);
// every token is overridable from config.yml under `theme:`.
package theme

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Palette is the set of semantic color tokens. It is defined here (rather than
// in the config package) so config can embed it without an import cycle, and so
// the TUI depends only on semantic roles — never raw hex.
type Palette struct {
	Name    string `yaml:"name" json:"name"`
	Base    string `yaml:"base" json:"base"`       // app background
	Surface string `yaml:"surface" json:"surface"` // panel background
	Overlay string `yaml:"overlay" json:"overlay"` // dividers, inactive pane borders
	Text    string `yaml:"text" json:"text"`       // primary text
	Muted   string `yaml:"muted" json:"muted"`     // secondary text, empty CI dots, inactive nodes
	Primary string `yaml:"primary" json:"primary"` // selection bar, active stack node ("you are here")
	Focus   string `yaml:"focus" json:"focus"`     // focused pane border, section headers, spinners
	Info    string `yaml:"info" json:"info"`       // PR/issue numbers, links, branch names
	Success string `yaml:"success" json:"success"` // approved, merged, CI pass
	Warning string `yaml:"warning" json:"warning"` // lineage drift (amber), CI pending
	Danger  string `yaml:"danger" json:"danger"`   // CI fail, conflicts, changes-requested
	Accent2 string `yaml:"accent2" json:"accent2"` // commit hashes, subtle emphasis
	CodeBg  string `yaml:"codeBg" json:"codeBg"`   // background for code blocks & inline code (decoupled from Overlay dividers)
	FocusBg string `yaml:"focusBg" json:"focusBg"` // background of the focused/selected cursor bar (falls back to Surface when unset)
}

// DefaultPalette returns the Event Horizon (dark) palette — the source-of-truth
// tokens from the build plan, Appendix A. It is the base for ModeDark.
func DefaultPalette() Palette {
	return Palette{
		Name:    "event-horizon",
		Base:    "#1c1e26",
		Surface: "#232530",
		Overlay: "#2e303e",
		Text:    "#fadad1",
		Muted:   "#6c6f93",
		Primary: "#ee64ac",
		Focus:   "#26bbd9",
		Info:    "#3fc4de",
		Success: "#29d398",
		Warning: "#fab795",
		Danger:  "#e95678",
		Accent2: "#59e3e3",
		CodeBg:  "#2e303e", // matches Overlay on dark — an elevated panel reads fine there
	}
}

// LightPalette returns "Event Horizon Day" — the light counterpart for working
// outdoors. It keeps Event Horizon's native hue identity (magenta-pink, coral,
// teal-cyan, spring green, peach) rather than swapping in generic colors; each
// accent is only deepened/saturated enough to make a bold statement and stay
// legible on the warm parchment base (deliberately not white).
func LightPalette() Palette {
	return Palette{
		Name:    "event-horizon-day",
		Base:    "#e9dcbd", // warm parchment background (not white)
		Surface: "#dfd0ac", // panels: a deeper warm tone
		Overlay: "#c8b78e", // dividers, inactive borders (kept darker so rules stay visible)
		Text:    "#2b2833", // primary text: deep warm charcoal (strong contrast)
		Muted:   "#79705f", // secondary text, inactive nodes (warm taupe)
		Primary: "#d4267f", // selection bar, active node — native magenta-pink, deepened
		Focus:   "#0c8fa6", // focused borders, headers — native cyan as a bold teal
		Info:    "#1577a3", // PR numbers, links, branches (deep cyan-blue)
		Success: "#09a96a", // approved, merged, CI pass — native spring/mint emerald, vivid
		Warning: "#c2611a", // drift, CI pending — peach turned bold burnt orange
		Danger:  "#db1f4d", // failures, conflicts — native coral, deepened to crimson
		Accent2: "#0e8a86", // commit hashes, emphasis (teal)
		CodeBg:  "#dacda6", // code/inline-code panel — soft warm sand, lighter than the divider tan
	}
}

// Mode names the two built-in palette variants.
const (
	ModeDark  = "dark"
	ModeLight = "light"
)

// Toggle returns the opposite mode.
func Toggle(mode string) string {
	if mode == ModeLight {
		return ModeDark
	}
	return ModeLight
}

// base returns the built-in palette for a mode (dark for anything unrecognized).
func base(mode string) Palette {
	if mode == ModeLight {
		return LightPalette()
	}
	return DefaultPalette()
}

// Fingerprint returns a stable string covering every token, so cache keys built
// from it invalidate whenever any color changes (a mode toggle, or a single-token
// override). Use this rather than hand-picking a subset of tokens — that risks
// leaving a token orphaned (a cache that never refreshes when only it changes).
func Fingerprint(t Theme) string {
	return strings.Join([]string{
		string(t.Base), string(t.Surface), string(t.Overlay), string(t.Text),
		string(t.Muted), string(t.Primary), string(t.Focus), string(t.Info),
		string(t.Success), string(t.Warning), string(t.Danger), string(t.Accent2),
		string(t.CodeBg), string(t.FocusBg),
	}, "|")
}

// IsLight reports whether the theme's background reads as light. It lets callers
// pick a contrasting affordance (e.g. which toggle glyph to light up) from the
// resolved Theme alone, without separately tracking the mode string.
func IsLight(t Theme) bool {
	return luminance(string(t.Base)) > 0.5
}

// luminance returns the perceptual brightness (0..1) of a "#rrggbb" color, or 0
// if it can't be parsed.
func luminance(hex string) float64 {
	hex = strings.TrimPrefix(hex, "#")
	if len(hex) != 6 {
		return 0
	}
	r, err1 := strconv.ParseInt(hex[0:2], 16, 0)
	g, err2 := strconv.ParseInt(hex[2:4], 16, 0)
	b, err3 := strconv.ParseInt(hex[4:6], 16, 0)
	if err1 != nil || err2 != nil || err3 != nil {
		return 0
	}
	return (0.299*float64(r) + 0.587*float64(g) + 0.114*float64(b)) / 255
}

// Theme holds resolved Lip Gloss colors plus a few ready-made styles. Build it
// once from a Palette and thread it through the TUI.
type Theme struct {
	Base    lipgloss.Color
	Surface lipgloss.Color
	Overlay lipgloss.Color
	Text    lipgloss.Color
	Muted   lipgloss.Color
	Primary lipgloss.Color
	Focus   lipgloss.Color
	Info    lipgloss.Color
	Success lipgloss.Color
	Warning lipgloss.Color
	Danger  lipgloss.Color
	Accent2 lipgloss.Color
	CodeBg  lipgloss.Color
	FocusBg lipgloss.Color
}

// New resolves a Palette into a Theme, falling back to the Event Horizon (dark)
// default for any token the user left blank. Equivalent to Resolve(ModeDark, p).
func New(p Palette) Theme {
	return build(DefaultPalette(), p)
}

// Resolve builds the Theme for a mode ("light"/"dark"), layering the user's
// override palette (any non-empty token wins) over that mode's built-in base.
// An empty override yields the pure built-in palette for the mode.
func Resolve(mode string, override Palette) Theme {
	return build(base(mode), override)
}

// build layers override over a base palette: a non-empty override token wins,
// otherwise the base value is used.
func build(b, override Palette) Theme {
	d := b
	p := override
	pick := func(v, fallback string) lipgloss.Color {
		if v == "" {
			return lipgloss.Color(fallback)
		}
		return lipgloss.Color(v)
	}
	// FocusBg defaults to the resolved Surface when no palette sets it, so themes
	// that don't opt in (e.g. event-horizon) keep the original surface-backed cursor
	// bar; retro-82 sets focusBg to a lighter tone so its focus bar reads on the dark base.
	surface := pick(p.Surface, d.Surface)
	focusBg := surface
	if p.FocusBg != "" {
		focusBg = lipgloss.Color(p.FocusBg)
	} else if d.FocusBg != "" {
		focusBg = lipgloss.Color(d.FocusBg)
	}
	return Theme{
		Base:    pick(p.Base, d.Base),
		Surface: surface,
		Overlay: pick(p.Overlay, d.Overlay),
		Text:    pick(p.Text, d.Text),
		Muted:   pick(p.Muted, d.Muted),
		Primary: pick(p.Primary, d.Primary),
		Focus:   pick(p.Focus, d.Focus),
		Info:    pick(p.Info, d.Info),
		Success: pick(p.Success, d.Success),
		Warning: pick(p.Warning, d.Warning),
		Danger:  pick(p.Danger, d.Danger),
		Accent2: pick(p.Accent2, d.Accent2),
		CodeBg:  pick(p.CodeBg, d.CodeBg),
		FocusBg: focusBg,
	}
}
