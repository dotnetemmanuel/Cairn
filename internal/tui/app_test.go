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

func TestClosedFilter(t *testing.T) {
	cases := map[string]string{
		"is:open is:pr author:@me":       "is:closed is:pr author:@me sort:updated-desc",
		"is:pr review-requested:@me":     "is:pr review-requested:@me is:closed sort:updated-desc",
		"is:open is:pr sort:created-asc": "is:closed is:pr sort:created-asc",
	}
	for in, want := range cases {
		if got := closedFilter(in); got != want {
			t.Errorf("closedFilter(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSectionRowsDividers(t *testing.T) {
	open := []gh.Item{{IsPR: true, Number: 1, State: "OPEN"}}
	closed := []gh.Item{{IsPR: true, Number: 2, State: "MERGED"}}

	// Both groups present → OPEN header, items, a blank spacer, CLOSED header.
	rows := sectionRows(open, closed)
	if len(rows) != 5 {
		t.Fatalf("want 5 rows (OPEN + item + spacer + CLOSED + item), got %d", len(rows))
	}
	if h, ok := rows[0].(sectionHeader); !ok || h.label != "OPEN" {
		t.Errorf("row 0 should be the OPEN header, got %#v", rows[0])
	}
	if h, ok := rows[2].(sectionHeader); !ok || h.label != "" {
		t.Errorf("row 2 should be the blank spacer, got %#v", rows[2])
	}
	if h, ok := rows[3].(sectionHeader); !ok || h.label != "CLOSED" {
		t.Errorf("row 3 should be the CLOSED header, got %#v", rows[3])
	}

	// A lone group needs no header.
	if rows := sectionRows(open, nil); len(rows) != 1 {
		t.Fatalf("open-only should be a flat list of 1, got %d", len(rows))
	} else if _, ok := rows[0].(prItem); !ok {
		t.Errorf("open-only row should be a prItem, got %#v", rows[0])
	}
}

func TestNavSkipsDividers(t *testing.T) {
	cfg := config.Config{Sections: []config.Section{{Title: "My PRs", Filter: "x"}}}
	m := New(cfg)
	open := []gh.Item{
		{IsPR: true, Repo: "o/r", Number: 1, Title: "open A", State: "OPEN"},
		{IsPR: true, Repo: "o/r", Number: 2, Title: "open B", State: "OPEN"},
	}
	closed := []gh.Item{
		{IsPR: true, Repo: "o/r", Number: 3, Title: "closed C", State: "CLOSED"},
	}
	m = drive(m,
		tea.WindowSizeMsg{Width: 100, Height: 30},
		sectionLoadedMsg{idx: 0, items: open, closed: closed, total: 2},
	)
	lst := &m.sections[0].list

	// Initial selection must land on the first real item, not the OPEN header.
	if _, ok := lst.Items()[lst.Index()].(prItem); !ok {
		t.Fatalf("cursor started on a divider (index %d)", lst.Index())
	}

	// Walking the whole list with j must never rest on a divider, and must
	// cross the CLOSED divider to reach the closed item.
	sawClosed := false
	for i := 0; i < len(lst.Items())*2; i++ {
		m = drive(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
		lst = &m.sections[0].list
		it, ok := lst.Items()[lst.Index()].(prItem)
		if !ok {
			t.Fatalf("cursor landed on a divider at index %d", lst.Index())
		}
		if it.Number == 3 {
			sawClosed = true
		}
	}
	if !sawClosed {
		t.Error("navigation never reached the closed item across the divider")
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
	// The local-stack pane title is present in stack mode regardless of whether
	// the cwd repo has git-town configured (the action list vs the init CTA
	// depends on that, but this pane always renders).
	if !strings.Contains(view, "Local stack (cwd)") {
		t.Errorf("stack mode view should render the local stack pane:\n%s", view)
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
