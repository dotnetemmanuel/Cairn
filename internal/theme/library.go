package theme

import (
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// NamedTheme is a selectable theme: a stable name, a human label, and the two
// palette variants (dark/light) the mode toggle flips between. Themes are loaded
// from JSON — the built-ins are embedded, and users can drop more into the themes
// directory. The JSON shape is exactly this struct: {name, label, dark:{…tokens},
// light:{…tokens}}.
type NamedTheme struct {
	Name  string  `json:"name"`
	Label string  `json:"label"`
	Dark  Palette `json:"dark"`
	Light Palette `json:"light"`
}

//go:embed builtin/*.json
var builtinFS embed.FS

// builtinOrder fixes the display order of the embedded themes so the picker is
// stable (event-horizon first, as the default). User themes sort after these.
var builtinOrder = []string{"event-horizon", "retro-82"}

// Library is the ordered, resolved set of available themes plus any warnings from
// files that failed to load (a malformed drop-in is skipped, never fatal).
type Library struct {
	Themes   []NamedTheme
	Warnings []string
}

// LoadLibrary returns the theme library: the embedded built-ins first (in
// builtinOrder), then any valid *.json themes found in userDir, sorted by name. A
// user theme whose name matches a built-in replaces that built-in in place, so a
// drop-in can retune a shipped theme without adding a duplicate entry. userDir may
// be empty or absent — the built-ins alone are always returned. A malformed user
// file is recorded in Warnings and skipped; it never breaks the load.
func LoadLibrary(userDir string) Library {
	var lib Library
	byName := map[string]int{} // name → index in lib.Themes

	add := func(nt NamedTheme) {
		if i, ok := byName[nt.Name]; ok {
			lib.Themes[i] = nt // override in place (user retunes a built-in)
			return
		}
		byName[nt.Name] = len(lib.Themes)
		lib.Themes = append(lib.Themes, nt)
	}

	// Built-ins, in fixed order.
	for _, name := range builtinOrder {
		data, err := builtinFS.ReadFile("builtin/" + name + ".json")
		if err != nil {
			lib.Warnings = append(lib.Warnings, fmt.Sprintf("builtin %s: %v", name, err))
			continue
		}
		nt, err := parseTheme(data)
		if err != nil {
			lib.Warnings = append(lib.Warnings, fmt.Sprintf("builtin %s: %v", name, err))
			continue
		}
		add(nt)
	}

	// User drop-ins, sorted by filename for a stable order.
	if userDir != "" {
		entries, err := os.ReadDir(userDir)
		if err == nil {
			names := make([]string, 0, len(entries))
			for _, e := range entries {
				if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".json") {
					continue
				}
				names = append(names, e.Name())
			}
			sort.Strings(names)
			for _, fn := range names {
				path := filepath.Join(userDir, fn)
				data, err := os.ReadFile(path)
				if err != nil {
					lib.Warnings = append(lib.Warnings, fmt.Sprintf("%s: %v", fn, err))
					continue
				}
				nt, err := parseTheme(data)
				if err != nil {
					lib.Warnings = append(lib.Warnings, fmt.Sprintf("%s: %v", fn, err))
					continue
				}
				add(nt)
			}
		}
	}

	return lib
}

// parseTheme decodes one theme JSON document and validates the minimum it needs to
// be usable: a name and a non-empty Base for each variant (the token that paints
// the whole frame — a theme missing it would render an unreadable screen). Every
// other token falls back to the built-in default at resolve time, so a partial
// drop-in still works.
func parseTheme(data []byte) (NamedTheme, error) {
	var nt NamedTheme
	if err := json.Unmarshal(data, &nt); err != nil {
		return NamedTheme{}, err
	}
	if strings.TrimSpace(nt.Name) == "" {
		return NamedTheme{}, fmt.Errorf("theme has no name")
	}
	if nt.Label == "" {
		nt.Label = nt.Name
	}
	if nt.Dark.Base == "" && nt.Light.Base == "" {
		return NamedTheme{}, fmt.Errorf("theme %q defines neither a dark nor a light base color", nt.Name)
	}
	return nt, nil
}

// Get returns the theme with the given name, and whether it was found.
func (l Library) Get(name string) (NamedTheme, bool) {
	for _, t := range l.Themes {
		if t.Name == name {
			return t, true
		}
	}
	return NamedTheme{}, false
}

// GetOrDefault returns the named theme, falling back to event-horizon (then the
// first available theme) when the name is unknown — so a stale config.themeName
// can never leave the app themeless.
func (l Library) GetOrDefault(name string) NamedTheme {
	if t, ok := l.Get(name); ok {
		return t
	}
	if t, ok := l.Get("event-horizon"); ok {
		return t
	}
	if len(l.Themes) > 0 {
		return l.Themes[0]
	}
	// Nothing loaded at all (should be impossible: built-ins are embedded). Synthesize
	// from the compiled-in default so callers always get a usable theme.
	return NamedTheme{Name: "event-horizon", Label: "Event Horizon", Dark: DefaultPalette(), Light: LightPalette()}
}

// ResolveNamed builds the live Theme for a named theme and mode ("light"/"dark"),
// layering the user's per-token override (any non-empty token wins) over the
// theme's variant palette, which itself layers over the compiled-in default for
// that mode (so a drop-in that omits tokens still resolves fully).
func ResolveNamed(nt NamedTheme, mode string, override Palette) Theme {
	variant := nt.Dark
	if mode == ModeLight {
		variant = nt.Light
	}
	// Two-layer fallback: override → theme variant → compiled default for the mode.
	withDefaults := build(base(mode), variant)
	return build(paletteFrom(withDefaults), override)
}

// paletteFrom converts a resolved Theme back into a Palette so it can serve as the
// fallback layer for a further override pass. Used only inside ResolveNamed.
func paletteFrom(t Theme) Palette {
	return Palette{
		Base:    string(t.Base),
		Surface: string(t.Surface),
		Overlay: string(t.Overlay),
		Text:    string(t.Text),
		Muted:   string(t.Muted),
		Primary: string(t.Primary),
		Focus:   string(t.Focus),
		Info:    string(t.Info),
		Success: string(t.Success),
		Warning: string(t.Warning),
		Danger:  string(t.Danger),
		Accent2: string(t.Accent2),
		CodeBg:  string(t.CodeBg),
		FocusBg: string(t.FocusBg),
	}
}
