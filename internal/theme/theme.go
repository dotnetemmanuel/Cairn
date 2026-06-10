// Package theme defines Cairn's color palette and the Lip Gloss style set
// derived from it. The default is "Event Horizon" (Omarchy Horizon Dark);
// every token is overridable from config.yml under `theme:`.
package theme

import "github.com/charmbracelet/lipgloss"

// Palette is the set of semantic color tokens. It is defined here (rather than
// in the config package) so config can embed it without an import cycle, and so
// the TUI depends only on semantic roles — never raw hex.
type Palette struct {
	Name    string `yaml:"name"`
	Base    string `yaml:"base"`    // app background
	Surface string `yaml:"surface"` // panel background
	Overlay string `yaml:"overlay"` // dividers, inactive pane borders
	Text    string `yaml:"text"`    // primary text
	Muted   string `yaml:"muted"`   // secondary text, empty CI dots, inactive nodes
	Primary string `yaml:"primary"` // selection bar, active stack node ("you are here")
	Focus   string `yaml:"focus"`   // focused pane border, section headers, spinners
	Info    string `yaml:"info"`    // PR/issue numbers, links, branch names
	Success string `yaml:"success"` // approved, merged, CI pass
	Warning string `yaml:"warning"` // lineage drift (amber), CI pending
	Danger  string `yaml:"danger"`  // CI fail, conflicts, changes-requested
	Accent2 string `yaml:"accent2"` // commit hashes, subtle emphasis
}

// DefaultPalette returns the Event Horizon palette (the source-of-truth tokens
// from the build plan, Appendix A).
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
	}
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
}

// New resolves a Palette into a Theme, falling back to the Event Horizon
// default for any token the user left blank.
func New(p Palette) Theme {
	d := DefaultPalette()
	pick := func(v, fallback string) lipgloss.Color {
		if v == "" {
			return lipgloss.Color(fallback)
		}
		return lipgloss.Color(v)
	}
	return Theme{
		Base:    pick(p.Base, d.Base),
		Surface: pick(p.Surface, d.Surface),
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
	}
}
