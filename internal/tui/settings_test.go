package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/dotnetemmanuel/cairn/internal/config"
)

func keyMsg(s string) tea.KeyMsg {
	if len(s) == 1 {
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "space":
		return tea.KeyMsg{Type: tea.KeySpace}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func newSettingsTestModel(t *testing.T) Model {
	t.Helper()
	cfg := config.Default()
	m := New(cfg)
	return drive(m, tea.WindowSizeMsg{Width: 120, Height: 40})
}

// Comma opens the settings screen, seeded on the currently-active theme.
func TestCommaOpensSettingsOnActiveTheme(t *testing.T) {
	m := newSettingsTestModel(t)
	if m.themeName != "event-horizon" {
		t.Fatalf("precondition: active theme = %q, want event-horizon", m.themeName)
	}
	m = drive(m, keyMsg(","))
	if m.mode != modeSettings {
		t.Fatalf("mode = %v, want modeSettings", m.mode)
	}
	if m.settings.selectedName() != "event-horizon" {
		t.Errorf("picker opened on %q, want event-horizon", m.settings.selectedName())
	}
}

// Moving down and pressing enter applies retro-82 to the whole model and returns
// to the dashboard.
func TestSettingsApplySelectsTheme(t *testing.T) {
	m := newSettingsTestModel(t)
	m = drive(m, keyMsg(","), keyMsg("down"), keyMsg("enter"))
	if m.mode != modeDashboard {
		t.Fatalf("after apply, mode = %v, want modeDashboard", m.mode)
	}
	if m.themeName != "retro-82" {
		t.Errorf("applied theme = %q, want retro-82", m.themeName)
	}
	// The live theme must actually be retro-82's navy base, not event-horizon's.
	if string(m.th.Base) != "#05182e" {
		t.Errorf("live Base = %q, want retro-82 navy #05182e", m.th.Base)
	}
}

// Esc cancels: the previewed theme is discarded and the active theme is unchanged.
func TestSettingsCancelKeepsTheme(t *testing.T) {
	m := newSettingsTestModel(t)
	baseBefore := string(m.th.Base)
	m = drive(m, keyMsg(","), keyMsg("down"), keyMsg("esc"))
	if m.mode != modeDashboard {
		t.Fatalf("after cancel, mode = %v, want modeDashboard", m.mode)
	}
	if m.themeName != "event-horizon" {
		t.Errorf("theme changed on cancel: %q", m.themeName)
	}
	if string(m.th.Base) != baseBefore {
		t.Errorf("live Base changed on cancel: %q -> %q", baseBefore, m.th.Base)
	}
}

// Space toggles the dark/light variant within the picker; applying persists it.
func TestSettingsVariantToggle(t *testing.T) {
	m := newSettingsTestModel(t)
	if m.themeMode != "dark" {
		t.Fatalf("precondition: mode = %q, want dark", m.themeMode)
	}
	m = drive(m, keyMsg(","), keyMsg("space"), keyMsg("enter"))
	if m.themeMode != "light" {
		t.Errorf("after space+apply, themeMode = %q, want light", m.themeMode)
	}
}

// ctrl+c must quit even from inside the settings picker (never trap the user).
func TestSettingsCtrlCQuits(t *testing.T) {
	m := newSettingsTestModel(t)
	m = drive(m, keyMsg(","))
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("ctrl+c in settings returned no command, want tea.Quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("ctrl+c did not produce tea.QuitMsg")
	}
}

// Regression: while previewing a light theme, the whole-frame background must be
// reasserted in the CANDIDATE theme's base, not the still-active theme's. Using the
// active (dark) base ghosts dark rectangles through the light preview.
func TestSettingsPreviewPaintsCandidateBackground(t *testing.T) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(termenv.Ascii) })

	m := newSettingsTestModel(t)
	// Move to retro-82 and toggle to its light variant while event-horizon dark
	// stays the active theme.
	m = drive(m, keyMsg(","), keyMsg("down"), keyMsg("space"))
	view := m.View()
	if !strings.Contains(view, "246;232;205") { // retro-82 light base #f6e8cd
		t.Error("preview frame should reassert the candidate (cream) base")
	}
	if strings.Contains(view, "28;30;38") { // event-horizon dark base #1c1e26
		t.Error("preview frame must not ghost the active (dark) base")
	}
}

// The settings view renders the theme labels and the preview affordances.
func TestSettingsViewRendersThemesAndPreview(t *testing.T) {
	m := newSettingsTestModel(t)
	m = drive(m, keyMsg(","))
	view := m.settings.View()
	for _, want := range []string{"Event Horizon", "retro-82", "Preview", "Variant", "apply"} {
		if !strings.Contains(view, want) {
			t.Errorf("settings view missing %q", want)
		}
	}
}
