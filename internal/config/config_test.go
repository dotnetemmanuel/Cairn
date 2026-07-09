package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestDefaultEnablesClosedTail(t *testing.T) {
	c := Default()
	if !c.ShowClosed {
		t.Error("ShowClosed should default to true (current behavior, non-breaking)")
	}
	if c.ClosedLimit != 15 {
		t.Errorf("ClosedLimit default = %d, want 15", c.ClosedLimit)
	}
}

func TestClosedTailOverridesFromYAML(t *testing.T) {
	c := Default()
	yml := `
showClosed: false
closedLimit: 5
sections:
  - title: My PRs
    filter: "is:open is:pr author:@me"
  - title: Needs Review
    filter: "is:open is:pr review-requested:@me"
    showClosed: true
    closedLimit: 3
`
	if err := yaml.Unmarshal([]byte(yml), &c); err != nil {
		t.Fatal(err)
	}
	if c.ShowClosed {
		t.Error("global showClosed:false should disable the tail")
	}
	if c.ClosedLimit != 5 {
		t.Errorf("global ClosedLimit = %d, want 5", c.ClosedLimit)
	}
	// Section 0 leaves it unset -> inherits (nil pointer).
	if c.Sections[0].ShowClosed != nil {
		t.Error("unset section ShowClosed should be nil (inherit)")
	}
	// Section 1 overrides both.
	if c.Sections[1].ShowClosed == nil || !*c.Sections[1].ShowClosed {
		t.Error("section showClosed:true should override the global off")
	}
	if c.Sections[1].ClosedLimit != 3 {
		t.Errorf("section ClosedLimit = %d, want 3", c.Sections[1].ClosedLimit)
	}
}

func TestSaveThemeModeRoundTrips(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	// No file yet: SaveThemeMode creates it, and Load reads the value back.
	if err := SaveThemeMode("light"); err != nil {
		t.Fatalf("SaveThemeMode (create): %v", err)
	}
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.ThemeMode != "light" {
		t.Errorf("ThemeMode = %q, want light", c.ThemeMode)
	}

	// Toggling back updates in place.
	if err := SaveThemeMode("dark"); err != nil {
		t.Fatalf("SaveThemeMode (update): %v", err)
	}
	if c, _ = Load(); c.ThemeMode != "dark" {
		t.Errorf("ThemeMode = %q, want dark", c.ThemeMode)
	}
}

func TestSaveThemeModePreservesOtherKeys(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	path := filepath.Join(dir, "cairn", "config.yml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	// A hand-written config with a comment and an unrelated key.
	original := "# my cairn config\ndefaultTrunk: develop\nshowClosed: false\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := SaveThemeMode("light"); err != nil {
		t.Fatalf("SaveThemeMode: %v", err)
	}

	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	if !strings.Contains(got, "# my cairn config") {
		t.Errorf("comment was dropped:\n%s", got)
	}
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.DefaultTrunk != "develop" {
		t.Errorf("DefaultTrunk = %q, want develop (preserved)", c.DefaultTrunk)
	}
	if c.ShowClosed {
		t.Error("showClosed:false should be preserved")
	}
	if c.ThemeMode != "light" {
		t.Errorf("ThemeMode = %q, want light", c.ThemeMode)
	}
}

func TestDefaultThemeName(t *testing.T) {
	if got := Default().ThemeName; got != "event-horizon" {
		t.Errorf("default ThemeName = %q, want event-horizon", got)
	}
}

func TestSaveThemeSelectionRoundTripsBothKeys(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	if err := SaveThemeSelection("retro-82", "light"); err != nil {
		t.Fatalf("SaveThemeSelection: %v", err)
	}
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.ThemeName != "retro-82" {
		t.Errorf("ThemeName = %q, want retro-82", c.ThemeName)
	}
	if c.ThemeMode != "light" {
		t.Errorf("ThemeMode = %q, want light", c.ThemeMode)
	}

	// Re-selecting updates both keys in place (no duplication).
	if err := SaveThemeSelection("event-horizon", "dark"); err != nil {
		t.Fatalf("SaveThemeSelection (update): %v", err)
	}
	c, err = Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.ThemeName != "event-horizon" || c.ThemeMode != "dark" {
		t.Errorf("after update: name=%q mode=%q, want event-horizon/dark", c.ThemeName, c.ThemeMode)
	}
}

func TestThemesDirUnderConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	got, err := ThemesDir()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "cairn", "themes"); got != want {
		t.Errorf("ThemesDir = %q, want %q", got, want)
	}
}
