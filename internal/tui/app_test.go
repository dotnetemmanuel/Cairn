package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/dotnetemmanuel/cairn/internal/config"
	"github.com/dotnetemmanuel/cairn/internal/gh"
)

// drive applies a sequence of messages to the model, returning the final state.
func drive(m Model, msgs ...tea.Msg) Model {
	for _, msg := range msgs {
		next, _ := m.Update(msg)
		m = next.(Model)
	}
	return m
}

func TestDashboardRendersHeaderTabsAndRows(t *testing.T) {
	// Stable clock so relative times are deterministic.
	fixed := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	clock = func() time.Time { return fixed }
	t.Cleanup(func() { clock = time.Now })

	cfg := config.Config{
		Sections: []config.Section{
			{Title: "My PRs", Filter: "is:open is:pr author:@me"},
			{Title: "Needs Review", Filter: "review-requested:@me"},
		},
	}
	m := New(cfg)

	items := []gh.Item{
		{IsPR: true, Repo: "Mindful-Stack/mpd", Number: 29, Title: "PWA UI improvements",
			Author: "dotnetemmanuel", Review: gh.ReviewApproved, Checks: gh.CheckSuccess,
			UpdatedAt: fixed.Add(-3 * time.Hour)},
		{IsPR: true, Repo: "Grantigo/grantigo", Number: 327, Title: "Drink modal",
			Author: "someone", Review: gh.ReviewChangesRequested, Checks: gh.CheckFailure,
			UpdatedAt: fixed.Add(-48 * time.Hour)},
	}

	m = drive(m,
		tea.WindowSizeMsg{Width: 100, Height: 30},
		viewerMsg{v: gh.Viewer{Login: "octocat", RateRemaining: 4992, RateLimit: 5000}},
		sectionLoadedMsg{idx: 0, items: items, total: 6},
	)

	view := m.View()

	wants := []string{
		"Logged in as", "octocat", "4992 API calls remaining",
		"My PRs", "Needs Review",
		"mpd#29", "PWA UI improvements", "3h",
		"grantigo#327", "Drink modal", "2d",
		"r refresh", "q quit",
	}
	for _, w := range wants {
		if !strings.Contains(view, w) {
			t.Errorf("view missing %q\n---\n%s", w, view)
		}
	}

	// Tab label should reflect the total match count once loaded.
	if !strings.Contains(view, "My PRs (6)") {
		t.Errorf("expected loaded tab to show count; got:\n%s", view)
	}
}

func TestTabCyclingWraps(t *testing.T) {
	cfg := config.Config{Sections: []config.Section{
		{Title: "A"}, {Title: "B"}, {Title: "C"},
	}}
	m := drive(New(cfg), tea.WindowSizeMsg{Width: 80, Height: 24})

	if m.active != 0 {
		t.Fatalf("expected start at 0, got %d", m.active)
	}
	m = drive(m, tea.KeyMsg{Type: tea.KeyTab})
	m = drive(m, tea.KeyMsg{Type: tea.KeyTab})
	if m.active != 2 {
		t.Fatalf("after two tabs expected 2, got %d", m.active)
	}
	m = drive(m, tea.KeyMsg{Type: tea.KeyTab}) // wrap
	if m.active != 0 {
		t.Fatalf("expected wrap to 0, got %d", m.active)
	}
	m = drive(m, tea.KeyMsg{Type: tea.KeyShiftTab}) // wrap backwards
	if m.active != 2 {
		t.Fatalf("expected back-wrap to 2, got %d", m.active)
	}
}

func dashboardWithItems(t *testing.T, n int) Model {
	t.Helper()
	cfg := config.Config{Sections: []config.Section{{Title: "My PRs", Filter: "x"}}}
	m := New(cfg)
	items := make([]gh.Item, n)
	for i := range items {
		items[i] = gh.Item{IsPR: true, Repo: "o/r", Number: i + 1, Title: fmt.Sprintf("PR %d", i+1)}
	}
	return drive(m,
		tea.WindowSizeMsg{Width: 100, Height: 30},
		sectionLoadedMsg{idx: 0, items: items, total: n},
	)
}

func TestDashboardListWrapsAround(t *testing.T) {
	m := dashboardWithItems(t, 3)
	idx := func() int { return m.sections[m.active].list.Index() }
	if idx() != 0 {
		t.Fatalf("expected to start at 0, got %d", idx())
	}
	// up on the first row wraps to the last.
	m = drive(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	if idx() != 2 {
		t.Fatalf("up on first should wrap to last (2), got %d", idx())
	}
	// down on the last row wraps back to the first.
	m = drive(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if idx() != 0 {
		t.Fatalf("down on last should wrap to first (0), got %d", idx())
	}
}

func TestDashboardEscDoesNotQuitButQDoes(t *testing.T) {
	m := dashboardWithItems(t, 3)
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc}); cmd != nil {
		if _, ok := cmd().(tea.QuitMsg); ok {
			t.Fatal("esc must not quit the dashboard")
		}
	}
	_, qcmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if qcmd == nil {
		t.Fatal("q should return a command")
	}
	if _, ok := qcmd().(tea.QuitMsg); !ok {
		t.Fatal("q should quit the dashboard")
	}
}

func TestEnterStackModeFromDashboard(t *testing.T) {
	cfg := config.Config{Sections: []config.Section{{Title: "My PRs", Filter: "is:open is:pr"}}}
	m := New(cfg)
	m = drive(m,
		tea.WindowSizeMsg{Width: 120, Height: 40},
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("S")},
	)
	if m.mode != modeStack {
		t.Fatalf("S should switch to stack mode, got mode %d", m.mode)
	}
	view := m.View()
	if !strings.Contains(view, "Stack actions") {
		t.Errorf("stack mode view should show the action list:\n%s", view)
	}
	// Esc emits a stackExitMsg command; run it and feed the result back.
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(Model)
	if cmd == nil {
		t.Fatal("esc in stack mode should return an exit command")
	}
	m = drive(m, cmd())
	if m.mode != modeDashboard {
		t.Errorf("esc should return to the dashboard, got mode %d", m.mode)
	}
}
