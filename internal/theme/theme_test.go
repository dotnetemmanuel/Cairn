package theme

import "testing"

func TestResolvePicksBaseByMode(t *testing.T) {
	dark := Resolve(ModeDark, Palette{})
	if string(dark.Base) != DefaultPalette().Base {
		t.Errorf("dark Base = %q, want %q", dark.Base, DefaultPalette().Base)
	}
	light := Resolve(ModeLight, Palette{})
	if string(light.Base) != LightPalette().Base {
		t.Errorf("light Base = %q, want %q", light.Base, LightPalette().Base)
	}
	// An unrecognized mode falls back to dark.
	if got := Resolve("nonsense", Palette{}); string(got.Base) != DefaultPalette().Base {
		t.Errorf("unknown mode Base = %q, want dark", got.Base)
	}
}

func TestResolveOverrideWinsOverBase(t *testing.T) {
	// A non-empty token in the override beats the mode base; omitted tokens fall
	// through to the base (here, the light palette).
	th := Resolve(ModeLight, Palette{Primary: "#abcdef"})
	if string(th.Primary) != "#abcdef" {
		t.Errorf("Primary = %q, want override #abcdef", th.Primary)
	}
	if string(th.Base) != LightPalette().Base {
		t.Errorf("Base = %q, want light base (override omitted it)", th.Base)
	}
}

func TestIsLight(t *testing.T) {
	if !IsLight(Resolve(ModeLight, Palette{})) {
		t.Error("light palette should report IsLight=true")
	}
	if IsLight(Resolve(ModeDark, Palette{})) {
		t.Error("dark palette should report IsLight=false")
	}
}

func TestToggle(t *testing.T) {
	if Toggle(ModeDark) != ModeLight {
		t.Error("Toggle(dark) should be light")
	}
	if Toggle(ModeLight) != ModeDark {
		t.Error("Toggle(light) should be dark")
	}
}
