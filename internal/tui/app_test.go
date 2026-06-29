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
		"r sync all", "q quit",
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

	// Both groups present, nothing folded → OPEN header, items, a blank spacer,
	// CLOSED header.
	rows := sectionRows(open, closed, false, false)
	if len(rows) != 5 {
		t.Fatalf("want 5 rows (OPEN + item + spacer + CLOSED + item), got %d", len(rows))
	}
	if h, ok := rows[0].(sectionHeader); !ok || h.label != "OPEN" || !h.collapsible {
		t.Errorf("row 0 should be the foldable OPEN header, got %#v", rows[0])
	}
	if h, ok := rows[2].(sectionHeader); !ok || h.label != "" || h.collapsible {
		t.Errorf("row 2 should be the blank spacer, got %#v", rows[2])
	}
	if h, ok := rows[3].(sectionHeader); !ok || h.label != "CLOSED" || !h.collapsible {
		t.Errorf("row 3 should be the foldable CLOSED header, got %#v", rows[3])
	}

	// A lone group needs no header.
	if rows := sectionRows(open, nil, false, false); len(rows) != 1 {
		t.Fatalf("open-only should be a flat list of 1, got %d", len(rows))
	} else if _, ok := rows[0].(prItem); !ok {
		t.Errorf("open-only row should be a prItem, got %#v", rows[0])
	}
}

func TestSectionRowsFolded(t *testing.T) {
	open := []gh.Item{
		{IsPR: true, Number: 1, State: "OPEN"},
		{IsPR: true, Number: 2, State: "OPEN"},
	}
	closed := []gh.Item{{IsPR: true, Number: 3, State: "MERGED"}}

	// OPEN folded → its items vanish, leaving the collapsed header, spacer, CLOSED
	// header, and the closed item.
	rows := sectionRows(open, closed, true, false)
	if len(rows) != 4 {
		t.Fatalf("OPEN folded: want 4 rows, got %d", len(rows))
	}
	if h, ok := rows[0].(sectionHeader); !ok || h.label != "OPEN" || !h.collapsed || h.count != 2 {
		t.Errorf("row 0 should be the collapsed OPEN header carrying its count, got %#v", rows[0])
	}
	for _, r := range rows {
		if it, ok := r.(prItem); ok && it.Number != 3 {
			t.Errorf("OPEN folded should hide open items, but found %#v", it)
		}
	}

	// CLOSED folded → the closed item vanishes; open items stay.
	rows = sectionRows(open, closed, false, true)
	if len(rows) != 5 { // OPEN, item, item, spacer, CLOSED
		t.Fatalf("CLOSED folded: want 5 rows, got %d", len(rows))
	}
	if h, ok := rows[4].(sectionHeader); !ok || h.label != "CLOSED" || !h.collapsed || h.count != 1 {
		t.Errorf("last row should be the collapsed CLOSED header, got %#v", rows[4])
	}
	if _, ok := rows[len(rows)-1].(prItem); ok {
		t.Error("CLOSED folded should hide the closed item")
	}
}

func TestJumpBetweenGroups(t *testing.T) {
	cfg := config.Config{Sections: []config.Section{{Title: "My PRs", Filter: "x"}}}
	m := New(cfg)
	open := []gh.Item{
		{IsPR: true, Repo: "o/r", Number: 1, Title: "open A", State: "OPEN"},
		{IsPR: true, Repo: "o/r", Number: 2, Title: "open B", State: "OPEN"},
	}
	closed := []gh.Item{{IsPR: true, Repo: "o/r", Number: 3, Title: "closed C", State: "CLOSED"}}
	m = drive(m,
		tea.WindowSizeMsg{Width: 100, Height: 30},
		sectionLoadedMsg{idx: 0, items: open, closed: closed, total: 2},
	)

	// n jumps to the next group header regardless of where the cursor sits.
	m = drive(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	if h, ok := m.sections[0].list.SelectedItem().(sectionHeader); !ok || h.label != "CLOSED" {
		t.Fatalf("n should jump to the CLOSED header, got %#v", m.sections[0].list.SelectedItem())
	}
	// n again wraps back to OPEN.
	m = drive(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	if h, ok := m.sections[0].list.SelectedItem().(sectionHeader); !ok || h.label != "OPEN" {
		t.Fatalf("a second n should wrap to the OPEN header, got %#v", m.sections[0].list.SelectedItem())
	}
	// N goes the other way: from OPEN back to CLOSED.
	m = drive(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("N")})
	if h, ok := m.sections[0].list.SelectedItem().(sectionHeader); !ok || h.label != "CLOSED" {
		t.Fatalf("N should jump to the CLOSED header, got %#v", m.sections[0].list.SelectedItem())
	}
}

func TestToggleSelectedGroup(t *testing.T) {
	cfg := config.Config{Sections: []config.Section{{Title: "My PRs", Filter: "x"}}}
	m := New(cfg)
	open := []gh.Item{{IsPR: true, Repo: "o/r", Number: 1, Title: "open A", State: "OPEN"}}
	closed := []gh.Item{{IsPR: true, Repo: "o/r", Number: 2, Title: "closed B", State: "CLOSED"}}
	m = drive(m,
		tea.WindowSizeMsg{Width: 100, Height: 30},
		sectionLoadedMsg{idx: 0, items: open, closed: closed, total: 1},
	)

	// Move up onto the OPEN header (cursor starts on the first item), then fold it.
	m = drive(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	lst := &m.sections[0].list
	if h, ok := lst.SelectedItem().(sectionHeader); !ok || h.label != "OPEN" {
		t.Fatalf("cursor should rest on the OPEN header, got %#v", lst.SelectedItem())
	}
	m = drive(m, tea.KeyMsg{Type: tea.KeyEnter})
	if !m.sections[0].collapsed["OPEN"] {
		t.Fatal("enter on the OPEN header should have folded the group")
	}
	// The open item must no longer be a row, and the cursor stays on the header.
	for _, li := range m.sections[0].list.Items() {
		if it, ok := li.(prItem); ok && it.Number == 1 {
			t.Error("open item should be hidden after folding OPEN")
		}
	}
	if h, ok := m.sections[0].list.SelectedItem().(sectionHeader); !ok || h.label != "OPEN" {
		t.Errorf("cursor should still be on the OPEN header after folding, got %#v", m.sections[0].list.SelectedItem())
	}

	// Enter again unfolds.
	m = drive(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.sections[0].collapsed["OPEN"] {
		t.Fatal("a second enter should have unfolded the OPEN group")
	}
}

func TestGroupedRows(t *testing.T) {
	groups := []group{
		{title: "ASSIGNED TO ME", items: []gh.Item{{IsPR: true, Number: 1, State: "OPEN"}}},
		{title: "MENTIONED", items: nil}, // empty → muted "none"
		{title: "PARTICIPATING", items: []gh.Item{{IsPR: true, Number: 2, State: "OPEN"}}},
	}
	rows := groupedRows(groups, map[string]bool{})
	// ASSIGNED header + item, spacer + MENTIONED header + none-note, spacer +
	// PARTICIPATING header + item = 8 rows.
	if len(rows) != 8 {
		t.Fatalf("want 8 rows, got %d: %#v", len(rows), rows)
	}
	if h, ok := rows[0].(sectionHeader); !ok || h.label != "ASSIGNED TO ME" || !h.collapsible {
		t.Errorf("row 0 should be the ASSIGNED header, got %#v", rows[0])
	}
	if _, ok := rows[4].(listNote); !ok {
		t.Errorf("empty MENTIONED group should show a 'none' note, got %#v", rows[4])
	}

	// Folding ASSIGNED hides its item but keeps the header (with its count).
	folded := groupedRows(groups, map[string]bool{"ASSIGNED TO ME": true})
	if h, ok := folded[0].(sectionHeader); !ok || !h.collapsed || h.count != 1 {
		t.Errorf("ASSIGNED header should be collapsed carrying count 1, got %#v", folded[0])
	}
	if _, ok := folded[1].(sectionHeader); !ok {
		t.Errorf("folded ASSIGNED should be followed by the spacer, not its item, got %#v", folded[1])
	}
}

func TestRefreshSyncsAllTabs(t *testing.T) {
	cfg := config.Config{Sections: []config.Section{
		{Title: "My PRs", Filter: "a"},
		{Title: "Involved", Type: config.SectionInvolved},
		{Title: "Orgs", Type: config.SectionOrgs},
	}}
	m := New(cfg)
	m = drive(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	// Mark every section settled so we can see refresh flip them back to loading.
	for i := range m.sections {
		m.sections[i].loading = false
		m.sections[i].loaded = true
	}
	// r while on tab 0 must mark ALL tabs loading — a whole-board sync — so a PR
	// that moved tabs reaches its new home without a per-tab refresh.
	m = drive(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	for i := range m.sections {
		if !m.sections[i].loading {
			t.Errorf("section %d (%s) should be loading after r", i, m.sections[i].title)
		}
	}
	if !m.headerLoading {
		t.Error("r should also refresh the header/viewer")
	}
}

func TestPostThenExitSyncsBoard(t *testing.T) {
	cfg := config.Config{Sections: []config.Section{
		{Title: "My PRs", Filter: "a"},
		{Title: "Orgs", Type: config.SectionOrgs},
	}}
	m := New(cfg)
	m = drive(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	settle := func() {
		for i := range m.sections {
			m.sections[i].loading = false
			m.sections[i].loaded = true
		}
		m.flash = ""
	}

	// Exiting detail without posting must not resync or flash.
	settle()
	m = drive(m, detailExitMsg{posted: false})
	if m.flash != "" {
		t.Errorf("no flash expected on a no-op exit, got %q", m.flash)
	}
	for i := range m.sections {
		if m.sections[i].loading {
			t.Errorf("section %d should not reload on a no-op exit", i)
		}
	}

	// Exiting after a post syncs every tab and shows an explanatory flash.
	settle()
	m = drive(m, detailExitMsg{posted: true})
	if m.flash == "" {
		t.Error("expected a header flash explaining the auto-sync")
	}
	if !m.headerLoading {
		t.Error("post-exit sync should refresh the header too")
	}
	for i := range m.sections {
		if !m.sections[i].loading {
			t.Errorf("section %d (%s) should reload after a post-exit", i, m.sections[i].title)
		}
	}

	// The flash dismisses on its timer.
	m = drive(m, flashClearMsg{})
	if m.flash != "" {
		t.Errorf("flashClearMsg should clear the flash, got %q", m.flash)
	}
}

func TestOrgGroups(t *testing.T) {
	orgs := []string{"Mindful-Stack", "Veygr-watch"}
	items := []gh.Item{
		{IsPR: true, Repo: "Mindful-Stack/mpd", Number: 33, State: "OPEN"},
		{IsPR: true, Repo: "mindful-stack/web", Number: 2, State: "OPEN"}, // case differs on purpose
		{IsPR: true, Repo: "Other-Org/x", Number: 9, State: "OPEN"},       // not in our orgs → dropped
	}
	groups, total := orgGroups(orgs, items)
	if len(groups) != 2 {
		t.Fatalf("want one group per org (2), got %d", len(groups))
	}
	if groups[0].title != "MINDFUL-STACK" || len(groups[0].items) != 2 {
		t.Errorf("Mindful-Stack group should hold both items (case-insensitive), got %q n=%d", groups[0].title, len(groups[0].items))
	}
	if groups[1].title != "VEYGR-WATCH" || len(groups[1].items) != 0 {
		t.Errorf("empty Veygr-watch group should still appear with 0 items, got %q n=%d", groups[1].title, len(groups[1].items))
	}
	if total != 2 { // the Other-Org PR isn't counted — it's not one of our orgs
		t.Errorf("total should count only matched-org items (2), got %d", total)
	}
}

func TestInvolvedSpecsExcludeOwnAndReview(t *testing.T) {
	// Every Involved sub-query must exclude your own PRs and review requests so a
	// PR has a single home (the most-actionable tab). Guards the dedup contract.
	for _, sp := range involvedSpecs() {
		if !strings.Contains(sp.filter, "-author:@me") || !strings.Contains(sp.filter, "-review-requested:@me") {
			t.Errorf("group %q filter %q must exclude -author:@me and -review-requested:@me", sp.title, sp.filter)
		}
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

	// Initial selection must land on the first real item, not a header.
	if _, ok := lst.Items()[lst.Index()].(prItem); !ok {
		t.Fatalf("cursor started on a divider (index %d)", lst.Index())
	}

	// Walking the whole list with j may rest on foldable headers (they're
	// navigable now) but never on the blank spacer or a note, and must cross the
	// CLOSED divider to reach the closed item.
	sawClosed := false
	for i := 0; i < len(lst.Items())*2; i++ {
		m = drive(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
		lst = &m.sections[0].list
		sel := lst.Items()[lst.Index()]
		if !navigable(sel) {
			t.Fatalf("cursor landed on a non-navigable row at index %d: %#v", lst.Index(), sel)
		}
		if it, ok := sel.(prItem); ok && it.Number == 3 {
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
