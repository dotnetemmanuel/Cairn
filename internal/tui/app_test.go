package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/list"
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
		// Wide enough that the footer keybinding line fits without wrapping (it's a
		// ~150-col strip), so the hint substrings below aren't split mid-phrase.
		tea.WindowSizeMsg{Width: 160, Height: 30},
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
	rows := sectionRows(open, closed, map[string]bool{}, false)
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
	if rows := sectionRows(open, nil, map[string]bool{}, false); len(rows) != 1 {
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
	rows := sectionRows(open, closed, map[string]bool{"OPEN": true}, false)
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
	rows = sectionRows(open, closed, map[string]bool{"CLOSED": true}, false)
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
	rows := groupedRows(groups, map[string]bool{}, false)
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
	folded := groupedRows(groups, map[string]bool{"ASSIGNED TO ME": true}, false)
	if h, ok := folded[0].(sectionHeader); !ok || !h.collapsed || h.count != 1 {
		t.Errorf("ASSIGNED header should be collapsed carrying count 1, got %#v", folded[0])
	}
	if _, ok := folded[1].(sectionHeader); !ok {
		t.Errorf("folded ASSIGNED should be followed by the spacer, not its item, got %#v", folded[1])
	}
}

func TestSectionRowsByRepo(t *testing.T) {
	now := time.Now()
	open := []gh.Item{
		{IsPR: true, Repo: "o/api", Number: 1, State: "OPEN", UpdatedAt: now.Add(-3 * time.Hour)},
		{IsPR: true, Repo: "o/cli", Number: 2, State: "OPEN", UpdatedAt: now.Add(-1 * time.Hour)},
		{IsPR: true, Repo: "o/api", Number: 3, State: "OPEN", UpdatedAt: now.Add(-10 * time.Hour)},
	}
	// By repo: cli is freshest (1h) so its header leads; api follows with its two
	// items newest-first (#1 at 3h before #3 at 10h).
	rows := sectionRows(open, nil, map[string]bool{}, true)
	if len(rows) != 6 { // cli header, #2, spacer, api header, #1, #3
		t.Fatalf("want 6 rows, got %d: %#v", len(rows), rows)
	}
	if h, ok := rows[0].(sectionHeader); !ok || h.label != "cli" || !h.collapsible || h.count != 1 || h.key != repoKey("", "o/cli") {
		t.Errorf("row0 should be the cli repo header (freshest, count 1, repo-scoped key), got %#v", rows[0])
	}
	if it, ok := rows[1].(prItem); !ok || it.Number != 2 {
		t.Errorf("row1 should be cli #2, got %#v", rows[1])
	}
	if h, ok := rows[2].(sectionHeader); !ok || h.collapsible || h.label != "" {
		t.Errorf("row2 should be the blank spacer between repo groups, got %#v", rows[2])
	}
	if h, ok := rows[3].(sectionHeader); !ok || h.label != "api" || h.count != 2 {
		t.Errorf("row3 should be the api repo header (count 2), got %#v", rows[3])
	}
	if it, ok := rows[4].(prItem); !ok || it.Number != 1 {
		t.Errorf("row4 should be api #1 (newest of its repo), got %#v", rows[4])
	}
	if it, ok := rows[5].(prItem); !ok || it.Number != 3 {
		t.Errorf("row5 should be api #3, got %#v", rows[5])
	}
}

func TestGroupedRowsByRepo(t *testing.T) {
	now := time.Now()
	groups := []group{{title: "ACME", items: []gh.Item{
		{IsPR: true, Repo: "ACME/api", Number: 1, State: "OPEN", UpdatedAt: now.Add(-2 * time.Hour)},
		{IsPR: true, Repo: "ACME/web", Number: 2, State: "OPEN", UpdatedAt: now.Add(-1 * time.Hour)},
	}}}
	// Org header stays at depth 0; repos nest under it as depth-1 subheaders, web
	// (freshest) first.
	rows := groupedRows(groups, map[string]bool{}, true)
	if len(rows) != 5 { // ACME, web header, #2, api header, #1
		t.Fatalf("want 5 rows, got %d: %#v", len(rows), rows)
	}
	if h, ok := rows[0].(sectionHeader); !ok || h.label != "ACME" || h.depth != 0 {
		t.Errorf("row0 should be the ACME org header at depth 0, got %#v", rows[0])
	}
	if h, ok := rows[1].(sectionHeader); !ok || h.label != "web" || h.depth != 1 || h.key != repoKey("ACME", "ACME/web") {
		t.Errorf("row1 should be the nested web repo subheader (depth 1, org-scoped key), got %#v", rows[1])
	}
	// Folding one repo within the org hides only that repo's PR.
	folded := groupedRows(groups, map[string]bool{repoKey("ACME", "ACME/web"): true}, true)
	for _, r := range folded {
		if it, ok := r.(prItem); ok && it.Number == 2 {
			t.Error("folding the web repo should hide its PR #2")
		}
	}
}

func TestSortToggleGroupsByRepo(t *testing.T) {
	cfg := config.Config{Sections: []config.Section{{Title: "My PRs", Filter: "x"}}}
	m := New(cfg)
	now := time.Now()
	open := []gh.Item{
		{IsPR: true, Repo: "o/api", Number: 1, Title: "a", State: "OPEN", UpdatedAt: now.Add(-2 * time.Hour)},
		{IsPR: true, Repo: "o/cli", Number: 2, Title: "b", State: "OPEN", UpdatedAt: now.Add(-1 * time.Hour)},
	}
	m = drive(m,
		tea.WindowSizeMsg{Width: 100, Height: 30},
		sectionLoadedMsg{idx: 0, items: open, total: 2},
	)
	collapsible := func() int {
		n := 0
		for _, li := range m.sections[0].list.Items() {
			if h, ok := li.(sectionHeader); ok && h.collapsible {
				n++
			}
		}
		return n
	}
	if collapsible() != 0 {
		t.Fatal("default order should have no group headers")
	}
	// o → grouped by repo: a foldable header per repo.
	m = drive(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("o")})
	if !m.sortByRepo {
		t.Fatal("o should enable sortByRepo")
	}
	if got := collapsible(); got != 2 {
		t.Fatalf("by-repo should add a foldable header per repo (want 2), got %d", got)
	}
	// o again → back to the flat default.
	m = drive(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("o")})
	if m.sortByRepo || collapsible() != 0 {
		t.Fatalf("second o should restore the flat default (sortByRepo=%v, headers=%d)", m.sortByRepo, collapsible())
	}
}

func TestSectionRowsByRepoGroupsClosedTail(t *testing.T) {
	now := time.Now()
	// Zero open, two closed in different repos — the "Needs my review" shape.
	closed := []gh.Item{
		{IsPR: true, Repo: "o/mpd", Number: 18, State: "CLOSED", UpdatedAt: now.Add(-100 * 24 * time.Hour)},
		{IsPR: true, Repo: "o/ad", Number: 8, State: "MERGED", UpdatedAt: now.Add(-365 * 24 * time.Hour)},
	}
	rows := sectionRows(nil, closed, map[string]bool{}, true)
	// Expect: OPEN header, "nothing open" note, spacer, CLOSED header, then a repo
	// subheader + its item for each of the two repos.
	var repoHeaders, closedItems int
	sawOpen, sawClosed := false, false
	for _, r := range rows {
		switch it := r.(type) {
		case sectionHeader:
			switch {
			case it.label == "OPEN":
				sawOpen = true
			case it.label == "CLOSED":
				sawClosed = true
			case it.depth == 1 && it.collapsible: // repo subheader nested under CLOSED
				repoHeaders++
			}
		case prItem:
			closedItems++
		}
	}
	if !sawOpen {
		t.Error("OPEN header must remain even with zero open PRs (it should not vanish in by-repo mode)")
	}
	if !sawClosed {
		t.Error("closed tail should keep its CLOSED header")
	}
	if repoHeaders != 2 {
		t.Errorf("closed tail should nest a repo subheader per repo (want 2), got %d", repoHeaders)
	}
	if closedItems != 2 {
		t.Errorf("both closed PRs should still render, got %d", closedItems)
	}
	// Open and closed repo scopes are independent, so an open "api" and a closed
	// "api" never share a fold key.
	if repoKey("", "o/mpd") == repoKey("closed", "o/mpd") {
		t.Error("open and closed repo fold keys must differ")
	}
}

func TestSortToggleSurvivesListShrink(t *testing.T) {
	// Grouping by repo adds repo subheaders (the list grows); toggling back drops
	// them (it shrinks). With the cursor parked at the bottom, the second toggle
	// must not panic on a now-out-of-range cursor.
	cfg := config.Config{Sections: []config.Section{{Title: "Orgs", Type: config.SectionOrgs}}}
	m := New(cfg)
	now := time.Now()
	var items []gh.Item
	for i, repo := range []string{"o/a", "o/b", "o/c"} {
		for j := 0; j < 3; j++ {
			items = append(items, gh.Item{IsPR: true, Repo: repo, Number: i*10 + j, State: "OPEN",
				UpdatedAt: now.Add(time.Duration(-(i*3 + j)) * time.Hour)})
		}
	}
	m = drive(m,
		tea.WindowSizeMsg{Width: 120, Height: 30},
		groupedLoadedMsg{idx: 0, groups: []group{{title: "ORG", items: items}}, total: len(items)},
	)
	// o → grouped by repo (list grows); park the cursor on the very last row.
	m = drive(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("o")})
	m.sections[0].list.Select(len(m.sections[0].list.Items()) - 1)
	// o → back to the flat default (list shrinks below the cursor). Must not panic.
	m = drive(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("o")})
	if m.sortByRepo {
		t.Fatal("second o should have turned grouping off")
	}
	if idx := m.sections[0].list.Index(); idx >= len(m.sections[0].list.Items()) {
		t.Fatalf("cursor %d left out of range for %d rows", idx, len(m.sections[0].list.Items()))
	}
}

func TestPRRowIndentDepth(t *testing.T) {
	depthOf := func(rows []list.Item, number int) int {
		for _, r := range rows {
			if it, ok := r.(prItem); ok && it.Number == number {
				return it.depth
			}
		}
		t.Fatalf("PR #%d not found in rows", number)
		return -1
	}
	open := []gh.Item{{IsPR: true, Repo: "o/api", Number: 1, State: "OPEN"}}
	closed := []gh.Item{{IsPR: true, Repo: "o/api", Number: 2, State: "MERGED"}}

	// A lone open list has no header above it → depth 0 (no indent).
	if d := depthOf(sectionRows(open, nil, map[string]bool{}, false), 1); d != 0 {
		t.Errorf("lone open row should be depth 0, got %d", d)
	}
	// With a closed tail the OPEN/CLOSED headers appear → rows indent one step.
	withClosed := sectionRows(open, closed, map[string]bool{}, false)
	if d := depthOf(withClosed, 1); d != 1 {
		t.Errorf("open row under OPEN header should be depth 1, got %d", d)
	}
	if d := depthOf(withClosed, 2); d != 1 {
		t.Errorf("closed row under CLOSED header should be depth 1, got %d", d)
	}
	// By repo with a closed tail: OPEN and CLOSED headers stay, repos nest under
	// each (depth 1), so their PRs are depth 2 on both sides.
	byRepoRows := sectionRows(open, closed, map[string]bool{}, true)
	if d := depthOf(byRepoRows, 1); d != 2 {
		t.Errorf("open PR under OPEN→repo should be depth 2, got %d", d)
	}
	if d := depthOf(byRepoRows, 2); d != 2 {
		t.Errorf("closed PR under CLOSED→repo should be depth 2, got %d", d)
	}
	// Grouped tabs: chronological → depth 1 under the org/role header; by repo →
	// depth 2 (group header → repo subheader → PR).
	groups := []group{{title: "ORG", items: []gh.Item{{IsPR: true, Repo: "o/api", Number: 3, State: "OPEN"}}}}
	if d := depthOf(groupedRows(groups, map[string]bool{}, false), 3); d != 1 {
		t.Errorf("grouped-chronological row should be depth 1, got %d", d)
	}
	if d := depthOf(groupedRows(groups, map[string]bool{}, true), 3); d != 2 {
		t.Errorf("grouped-by-repo row should be depth 2, got %d", d)
	}
}

func TestNotifPreviewFocusRequiresEngaging(t *testing.T) {
	cfg := config.Config{Sections: []config.Section{
		{Title: "My PRs", Filter: "x"},
		{Title: "Notifications", Type: config.SectionNotifications},
	}}
	m := New(cfg)
	now := time.Now()
	unread := []gh.Notification{
		{ThreadID: "t1", Type: "PullRequest", Repo: "o/r", Number: 5, Title: "one", Unread: true, UpdatedAt: now},
		{ThreadID: "t2", Type: "PullRequest", Repo: "o/r", Number: 6, Title: "two", Unread: true, UpdatedAt: now.Add(-time.Hour)},
	}
	m = drive(m,
		tea.WindowSizeMsg{Width: 160, Height: 30},
		notifFeedMsg{idx: 1, unread: unread},
	)

	// Land on the inbox from My PRs. Right again (still unarmed) must navigate tabs,
	// not reach over to the preview pane.
	m = drive(m, tea.KeyMsg{Type: tea.KeyRight})
	if !m.sections[m.active].isNotif() {
		t.Fatalf("right from My PRs should land on the inbox, active=%d", m.active)
	}
	m = drive(m, tea.KeyMsg{Type: tea.KeyRight})
	if m.notifPrev.focused {
		t.Error("right on a freshly-landed inbox must not focus the preview")
	}
	if m.sections[m.active].isNotif() {
		t.Error("right on an unarmed inbox should navigate to the next tab")
	}

	// Re-enter the inbox, engage the list with ↓ (arms it), then right focuses the
	// preview and stays on the inbox.
	m = drive(m, tea.KeyMsg{Type: tea.KeyRight}) // back onto the inbox
	m = drive(m, tea.KeyMsg{Type: tea.KeyDown})
	if !m.notifArmed {
		t.Fatal("↓ on the inbox list should arm it")
	}
	m = drive(m, tea.KeyMsg{Type: tea.KeyRight})
	if !m.notifPrev.focused {
		t.Error("right after engaging the list should focus the preview")
	}
	if !m.sections[m.active].isNotif() {
		t.Error("focusing the preview should keep us on the inbox")
	}
}

func TestNotifLandsOnHeaderNotItem(t *testing.T) {
	cfg := config.Config{Sections: []config.Section{{Title: "Notifications", Type: config.SectionNotifications}}}
	m := New(cfg)
	m = drive(m, tea.WindowSizeMsg{Width: 120, Height: 40})

	// Unread present → rest on the UNREAD header; nothing selected to preview.
	m = drive(m, notifFeedMsg{idx: 0,
		unread: []gh.Notification{{ThreadID: "u1", Title: "u", Unread: true, Type: "PullRequest", Number: 1, Repo: "o/r"}},
		read:   []gh.Notification{{ThreadID: "r1", Title: "r", Type: "PullRequest", Number: 2, Repo: "o/r"}},
	})
	if h, ok := m.sections[0].list.SelectedItem().(sectionHeader); !ok || h.label != "UNREAD" {
		t.Fatalf("with unread present, cursor should rest on the UNREAD header, got %#v", m.sections[0].list.SelectedItem())
	}
	if _, ok := m.selectedNotif(); ok {
		t.Error("a fresh inbox should not have a notification selected (it would auto-preview)")
	}

	// No unread → rest on the READ header instead.
	m = drive(m, notifFeedMsg{idx: 0,
		read: []gh.Notification{{ThreadID: "r1", Title: "r", Type: "PullRequest", Number: 2, Repo: "o/r"}},
	})
	if h, ok := m.sections[0].list.SelectedItem().(sectionHeader); !ok || h.label != "READ" {
		t.Fatalf("with no unread, cursor should rest on the READ header, got %#v", m.sections[0].list.SelectedItem())
	}
}

func TestIsMine(t *testing.T) {
	old := viewerLogin
	defer func() { viewerLogin = old }()

	viewerLogin = ""
	if isMine("octocat") {
		t.Error("with no viewer set, nothing should read as mine")
	}
	viewerLogin = "OctoCat"
	if !isMine("octocat") {
		t.Error("author should match the viewer case-insensitively")
	}
	if isMine("someone-else") {
		t.Error("a different author should not read as mine")
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

func TestNotifRowsSplit(t *testing.T) {
	unread := []gh.Notification{{ThreadID: "1", Title: "a", Unread: true}}
	read := []gh.Notification{{ThreadID: "2", Title: "b"}, {ThreadID: "3", Title: "c"}}
	rows := notifRows(unread, read, map[string]bool{})
	// UNREAD header + item, spacer + READ header + 2 items = 6 rows.
	if len(rows) != 6 {
		t.Fatalf("want 6 rows, got %d: %#v", len(rows), rows)
	}
	if h, ok := rows[0].(sectionHeader); !ok || h.label != "UNREAD" || h.count != 1 {
		t.Errorf("row 0 should be UNREAD header (count 1), got %#v", rows[0])
	}
	if h, ok := rows[3].(sectionHeader); !ok || h.label != "READ" || h.count != 2 {
		t.Errorf("row 3 should be READ header (count 2), got %#v", rows[3])
	}
	// An empty side still shows its header plus a muted note.
	rows = notifRows(nil, read, map[string]bool{})
	if _, ok := rows[1].(listNote); !ok {
		t.Errorf("empty UNREAD should show a note, got %#v", rows[1])
	}
}

func TestMarkSelectedReadMovesRow(t *testing.T) {
	cfg := config.Config{Sections: []config.Section{{Title: "Notifications", Type: config.SectionNotifications}}}
	m := New(cfg)
	m = drive(m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = drive(m, notifFeedMsg{idx: 0,
		unread: []gh.Notification{{ThreadID: "1", Title: "unread one", Unread: true, Type: "PullRequest", Number: 7, Repo: "o/r"}},
		read:   []gh.Notification{{ThreadID: "2", Title: "read one", Type: "PullRequest"}},
	})
	// A fresh inbox rests on the UNREAD header; ↓ steps onto the unread item.
	m = drive(m, tea.KeyMsg{Type: tea.KeyDown})
	if n, ok := m.selectedNotif(); !ok || n.ThreadID != "1" {
		t.Fatalf("cursor should rest on the unread item, got %#v ok=%v", n, ok)
	}
	m = drive(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if len(m.sections[0].notifUnread) != 0 {
		t.Errorf("unread list should be empty after marking read, got %d", len(m.sections[0].notifUnread))
	}
	if len(m.sections[0].notifRead) != 2 {
		t.Errorf("read list should now hold 2, got %d", len(m.sections[0].notifRead))
	}
	// The freshly-read one goes to the top of READ.
	if m.sections[0].notifRead[0].ThreadID != "1" || m.sections[0].notifRead[0].Unread {
		t.Errorf("freshly-read item should be first and not unread, got %#v", m.sections[0].notifRead[0])
	}
}

func TestNotifPreviewFocus(t *testing.T) {
	cfg := config.Config{Sections: []config.Section{{Title: "Notifications", Type: config.SectionNotifications}}}
	m := New(cfg)
	m = drive(m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = drive(m, notifFeedMsg{idx: 0,
		read: []gh.Notification{{ThreadID: "1", Title: "a", Type: "PullRequest", Number: 7, Repo: "o/r"}},
	})
	// A fresh inbox rests on the category header, not a notification; ↓ steps onto
	// the (read) item so the preview-focus assertions below have one selected.
	m = drive(m, tea.KeyMsg{Type: tea.KeyDown})
	if _, ok := m.selectedNotif(); !ok {
		t.Fatal("expected a PR notification selected")
	}
	// enter focuses the preview; esc returns to the list.
	m = drive(m, tea.KeyMsg{Type: tea.KeyEnter})
	if !m.notifPrev.focused {
		t.Fatal("enter should focus the preview")
	}
	m = drive(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.notifPrev.focused {
		t.Fatal("esc should return focus to the list")
	}
	// right opens (focuses) the preview; right again hides it. Arm the inbox first
	// (normally done by engaging the list with ↑/↓); this case is about the toggle,
	// not the arming gate, which TestNotifPreviewFocusRequiresEngaging covers.
	m.notifArmed = true
	m = drive(m, tea.KeyMsg{Type: tea.KeyRight})
	if !m.notifPrev.focused {
		t.Fatal("right (armed) should focus the preview")
	}
	m = drive(m, tea.KeyMsg{Type: tea.KeyRight})
	if m.notifPrev.focused {
		t.Fatal("right again should hide the preview")
	}
}

func TestRenderNotifConversationNoPanic(t *testing.T) {
	m := New(config.Config{})
	m = drive(m, tea.WindowSizeMsg{Width: 120, Height: 40})
	// Empty detail → the "no conversation yet" path; a populated one → real render.
	if got := renderNotifConversation(gh.PRDetail{}, 60, m.th); got == "" {
		t.Error("empty conversation should still render a placeholder line")
	}
	d := gh.PRDetail{
		Author: "octocat", Body: "Fixes the thing. cc @dotnetemmanuel",
		Comments: []gh.Comment{{Author: "someone", Body: "looks good @octocat", CreatedAt: time.Now()}},
	}
	_ = renderNotifConversation(d, 60, m.th) // must not panic
}

func TestEnterHintContext(t *testing.T) {
	cfg := config.Config{Sections: []config.Section{{Title: "Notifications", Type: config.SectionNotifications}}}
	m := New(cfg)
	m = drive(m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = drive(m, notifFeedMsg{idx: 0,
		unread: []gh.Notification{{ThreadID: "1", Title: "a", Unread: true, Type: "PullRequest", Number: 7, Repo: "o/r"}},
	})
	// A fresh inbox rests on the UNREAD header; ↓ steps onto the notification.
	m = drive(m, tea.KeyMsg{Type: tea.KeyDown})
	// List-focused on a PR notification: enter reads (focuses the preview).
	if got := m.enterHint(); got != "read" {
		t.Errorf("on a PR notification (list) want %q, got %q", "read", got)
	}
	// Once the preview is focused, enter opens the full detail.
	m.notifPrev.focused = true
	if got := m.enterHint(); got != "open PR" {
		t.Errorf("preview-focused want %q, got %q", "open PR", got)
	}
	m.notifPrev.focused = false
	// Up onto the UNREAD header → the hint becomes fold/unfold.
	m = drive(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	if _, ok := m.sections[0].list.SelectedItem().(sectionHeader); !ok {
		t.Fatalf("expected cursor on a header, got %#v", m.sections[0].list.SelectedItem())
	}
	if got := m.enterHint(); got != "fold / unfold" {
		t.Errorf("on a header want %q, got %q", "fold / unfold", got)
	}
}

func TestSelectedAsItemFromNotif(t *testing.T) {
	pr := selectedAsItem(notifItem{gh.Notification{Type: "PullRequest", Repo: "o/r", Number: 9, Title: "t"}})
	if !pr.IsPR || pr.Number != 9 || pr.Repo != "o/r" {
		t.Errorf("PR notification should map to a PR item, got %#v", pr)
	}
	issue := selectedAsItem(notifItem{gh.Notification{Type: "Issue", Repo: "o/r", Number: 3}})
	if issue.IsPR {
		t.Errorf("issue notification should not map to a PR item, got %#v", issue)
	}
}

func TestNotifInboxRenders(t *testing.T) {
	cfg := config.Config{Sections: []config.Section{
		{Title: "My PRs", Filter: "a"},
		{Title: "Notifications", Type: config.SectionNotifications},
	}}
	m := New(cfg)
	m = drive(m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = drive(m, notifFeedMsg{idx: 1,
		unread: []gh.Notification{{ThreadID: "1", Title: "review me", Unread: true, Reason: "review_requested", Type: "PullRequest", Repo: "o/r", Number: 7}},
		read:   nil,
	})
	// Switch to the inbox and render — must not panic and must show the panes.
	m = drive(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
	view := m.View()
	for _, want := range []string{"UNREAD", "review me", "review"} {
		if !strings.Contains(view, want) {
			t.Errorf("inbox view missing %q\n---\n%s", want, view)
		}
	}
	// The footer shows the inbox glyph legend (TYPE/WHY), not the PR one.
	if !strings.Contains(view, "TYPE") || !strings.Contains(view, "WHY") {
		t.Errorf("inbox footer should show the TYPE/WHY glyph legend\n---\n%s", view)
	}
	if strings.Contains(view, "CHECKS") {
		t.Errorf("inbox footer should not show the PR CHECKS legend\n---\n%s", view)
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

func TestHelpOverlayFitsTerminal(t *testing.T) {
	cfg := config.Config{Sections: []config.Section{{Title: "My PRs", Filter: "is:open"}}}
	m := New(cfg)
	// A short terminal where stack mode's full command reference can't fit at once.
	m = drive(m, tea.WindowSizeMsg{Width: 120, Height: 24})
	m.mode = modeStack
	m.openHelp()
	if got := m.helpVP.Height; got > 24-5 {
		t.Errorf("help body height %d should be clamped to the terminal (<= %d)", got, 24-5)
	}
	if m.helpVP.TotalLineCount() <= m.helpVP.Height {
		t.Error("stack help should overflow on a short terminal (so it must scroll)")
	}
	// The rendered overlay must not be taller than the terminal.
	if h := strings.Count(m.renderHelp(), "\n") + 1; h > 24 {
		t.Errorf("rendered help height %d exceeds terminal height 24", h)
	}
}

func TestSubheaderShowsWithoutSidebar(t *testing.T) {
	cfg := config.Config{Sections: []config.Section{{Title: "Orgs", Filter: "is:open"}}}
	m := New(cfg)
	items := []gh.Item{{IsPR: true, Repo: "o/r", Number: 1, Title: "x", Author: "a", UpdatedAt: time.Now()}}
	m = drive(m,
		tea.WindowSizeMsg{Width: 120, Height: 30},
		viewerMsg{v: gh.Viewer{Login: "me"}},
		sectionLoadedMsg{idx: 0, items: items, total: 10},
	)
	if m.sidebarVisible() {
		t.Skip("test assumes no stack sidebar (no stacks loaded)")
	}
	// The section subheader ("Orgs ▴ x/y ▾") must render even with the sidebar off.
	if v := m.View(); !strings.Contains(v, "Orgs  ▴") {
		t.Errorf("expected the section subheader without a sidebar; got:\n%s", v)
	}
}
