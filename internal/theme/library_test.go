package theme

import (
	"os"
	"path/filepath"
	"testing"
)

// The embedded built-ins must always be present and in the fixed order, so the
// picker and the default resolution are stable.
func TestLoadLibrary_BuiltinsPresentAndOrdered(t *testing.T) {
	lib := LoadLibrary("")
	if len(lib.Warnings) != 0 {
		t.Fatalf("built-in load produced warnings: %v", lib.Warnings)
	}
	if len(lib.Themes) < 2 {
		t.Fatalf("want at least 2 built-in themes, got %d", len(lib.Themes))
	}
	if lib.Themes[0].Name != "event-horizon" {
		t.Errorf("first theme = %q, want event-horizon", lib.Themes[0].Name)
	}
	if lib.Themes[1].Name != "retro-82" {
		t.Errorf("second theme = %q, want retro-82", lib.Themes[1].Name)
	}
}

// event-horizon.json must mirror the compiled-in DefaultPalette/LightPalette
// exactly, so the two sources of truth can never drift apart.
func TestBuiltinEventHorizonMatchesCompiledPalettes(t *testing.T) {
	lib := LoadLibrary("")
	nt, ok := lib.Get("event-horizon")
	if !ok {
		t.Fatal("event-horizon not found")
	}
	if nt.Dark != DefaultPalette() {
		t.Errorf("event-horizon dark variant:\n got  %+v\n want %+v", nt.Dark, DefaultPalette())
	}
	if nt.Light != LightPalette() {
		t.Errorf("event-horizon light variant:\n got  %+v\n want %+v", nt.Light, LightPalette())
	}
}

// retro-82 must define a full set of dark and light tokens (a distinct palette,
// not a stub).
func TestBuiltinRetro82Complete(t *testing.T) {
	lib := LoadLibrary("")
	nt, ok := lib.Get("retro-82")
	if !ok {
		t.Fatal("retro-82 not found")
	}
	for _, v := range []struct {
		name string
		p    Palette
	}{{"dark", nt.Dark}, {"light", nt.Light}} {
		if v.p.Base == "" || v.p.Text == "" || v.p.Primary == "" || v.p.Success == "" ||
			v.p.Danger == "" || v.p.CodeBg == "" {
			t.Errorf("retro-82 %s variant is missing tokens: %+v", v.name, v.p)
		}
	}
	// retro-82 is a warm navy theme — its dark base must differ from event-horizon's.
	if nt.Dark.Base == DefaultPalette().Base {
		t.Errorf("retro-82 dark base should differ from event-horizon")
	}
}

// A user drop-in with a new name is added after the built-ins; a malformed file is
// skipped with a warning rather than breaking the load.
func TestLoadLibrary_UserDropInAndMalformed(t *testing.T) {
	dir := t.TempDir()
	good := `{"name":"acme","label":"Acme","dark":{"base":"#000000","text":"#ffffff"},"light":{"base":"#ffffff","text":"#000000"}}`
	if err := os.WriteFile(filepath.Join(dir, "acme.json"), []byte(good), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	lib := LoadLibrary(dir)
	if _, ok := lib.Get("acme"); !ok {
		t.Error("valid drop-in acme was not loaded")
	}
	// Built-ins still first, user theme after.
	if lib.Themes[len(lib.Themes)-1].Name != "acme" {
		t.Errorf("user theme should sort after built-ins; last = %q", lib.Themes[len(lib.Themes)-1].Name)
	}
	if len(lib.Warnings) == 0 {
		t.Error("malformed file should produce a warning")
	}
}

// A user drop-in whose name matches a built-in replaces it in place (no duplicate
// entry), so a user can retune a shipped theme.
func TestLoadLibrary_UserOverridesBuiltinInPlace(t *testing.T) {
	dir := t.TempDir()
	override := `{"name":"retro-82","label":"My retro","dark":{"base":"#010203","text":"#fefefe"},"light":{"base":"#fefefe","text":"#010203"}}`
	if err := os.WriteFile(filepath.Join(dir, "retro-82.json"), []byte(override), 0o644); err != nil {
		t.Fatal(err)
	}

	lib := LoadLibrary(dir)
	count := 0
	for _, tm := range lib.Themes {
		if tm.Name == "retro-82" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("retro-82 should appear once after override, got %d", count)
	}
	nt, _ := lib.Get("retro-82")
	if nt.Label != "My retro" || nt.Dark.Base != "#010203" {
		t.Errorf("override did not take effect: %+v", nt)
	}
}

// ResolveNamed layers override → theme variant → compiled default. A theme that
// omits tokens still resolves fully (fills from the mode default), and a non-empty
// override token wins.
func TestResolveNamed_Layering(t *testing.T) {
	partial := NamedTheme{
		Name: "partial",
		Dark: Palette{Base: "#111111"}, // only Base set
	}
	th := ResolveNamed(partial, ModeDark, Palette{})
	if string(th.Base) != "#111111" {
		t.Errorf("Base = %q, want #111111 (from theme variant)", th.Base)
	}
	if string(th.Primary) != DefaultPalette().Primary {
		t.Errorf("Primary = %q, want default %q (filled from mode base)", th.Primary, DefaultPalette().Primary)
	}

	// User override beats the theme variant.
	overridden := ResolveNamed(partial, ModeDark, Palette{Primary: "#abcabc"})
	if string(overridden.Primary) != "#abcabc" {
		t.Errorf("override Primary = %q, want #abcabc", overridden.Primary)
	}
}

// Every built-in variant's CodeBg must be visibly distinct from its Base, or
// inline code and fenced blocks blend into the page (retro-82 dark originally set
// CodeBg darker-than-and-nearly-equal-to Base — indistinguishable).
func TestBuiltinCodeBgDistinctFromBase(t *testing.T) {
	const minDelta = 0.03
	lib := LoadLibrary("")
	for _, nt := range lib.Themes {
		for _, v := range []struct {
			mode string
			p    Palette
		}{{"dark", nt.Dark}, {"light", nt.Light}} {
			if v.p.Base == "" || v.p.CodeBg == "" {
				continue
			}
			delta := luminance(v.p.CodeBg) - luminance(v.p.Base)
			if delta < 0 {
				delta = -delta
			}
			if delta < minDelta {
				t.Errorf("%s %s: CodeBg %s too close to Base %s (luminance delta %.3f < %.2f)",
					nt.Name, v.mode, v.p.CodeBg, v.p.Base, delta, minDelta)
			}
		}
	}
}

func TestGetOrDefault_FallsBack(t *testing.T) {
	lib := LoadLibrary("")
	nt := lib.GetOrDefault("does-not-exist")
	if nt.Name != "event-horizon" {
		t.Errorf("unknown name should fall back to event-horizon, got %q", nt.Name)
	}
}
