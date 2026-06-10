package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/dotnetemmanuel/cairn/internal/gh"
	"github.com/dotnetemmanuel/cairn/internal/theme"
)

func driveDetail(m detailModel, msgs ...tea.Msg) detailModel {
	for _, msg := range msgs {
		m, _ = m.Update(msg)
	}
	return m
}

func loadedDetail(t *testing.T) detailModel {
	t.Helper()
	th := theme.New(theme.DefaultPalette())
	m := newDetail(th, gh.Item{IsPR: true, Repo: "Grantigo/grantigo", Number: 327,
		Title: "Add a DrinkModal", URL: "https://github.com/Grantigo/grantigo/pull/327"})

	detail := gh.PRDetail{
		Number: 327, Title: "Add a DrinkModal", State: "OPEN",
		BaseRef: "main", HeadRef: "funfeat/emmanuel",
		Author: "dotnetemmanuel", Additions: 246, Deletions: 10, ChangedFiles: 2,
		Body:     "Adds a drink modal.",
		Reviews:  []gh.Review{{Author: "github-actions", State: "CHANGES_REQUESTED", CreatedAt: time.Now()}},
		Comments: []gh.Comment{{Author: "octocat", Body: "looks tasty", CreatedAt: time.Now()}},
		Checks:   []gh.Check{{Name: "review", Conclusion: "SUCCESS"}, {Name: "snyk", Conclusion: "SUCCESS"}},
	}
	files := []gh.FileDiff{
		{Filename: "src/DrinkModal.svelte", Status: "added", Additions: 183,
			Patch: "@@ -0,0 +1,2 @@\n+<script lang=\"ts\">\n+export let isOpen = false;"},
		{Filename: "README.md", Status: "modified", Additions: 5, Deletions: 1,
			Patch: "@@ -1,1 +1,1 @@\n-old line\n+new line"},
	}
	return driveDetail(m,
		tea.WindowSizeMsg{Width: 140, Height: 40},
		prLoadedMsg{detail: detail, files: files},
	)
}

func TestDetailRendersDiffConversationChecks(t *testing.T) {
	m := loadedDetail(t)
	view := m.View()

	wants := []string{
		"#327", "Add a DrinkModal", "OPEN",
		"main", "funfeat/emmanuel", "+246", "-10",
		"DrinkModal.svelte",         // file list
		"isOpen",                    // diff content of selected file
		"Checks (2)", "review", "snyk",
		"changes",                   // review badge for CHANGES_REQUESTED
		"looks tasty",               // comment body
	}
	for _, w := range wants {
		if !strings.Contains(view, w) {
			t.Errorf("detail view missing %q", w)
		}
	}
}

func TestApproveConfirmFlow(t *testing.T) {
	m := loadedDetail(t)

	// 'a' arms the confirm prompt without submitting.
	m = driveDetail(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if m.state != stateConfirmApprove {
		t.Fatalf("expected stateConfirmApprove, got %d", m.state)
	}
	if !strings.Contains(m.View(), "Approve PR #327") {
		t.Errorf("expected approve confirmation in footer")
	}

	// A non-y key cancels.
	cancelled := driveDetail(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	if cancelled.state != stateBrowsing {
		t.Fatalf("expected cancel back to browsing, got %d", cancelled.state)
	}

	// 'y' confirms — transitions to submitting and returns a command.
	confirmed, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if confirmed.state != stateSubmitting {
		t.Fatalf("expected stateSubmitting after confirm, got %d", confirmed.state)
	}
	if cmd == nil {
		t.Fatalf("expected a submit command after confirm")
	}
}

func TestCommentComposerOpens(t *testing.T) {
	m := loadedDetail(t)
	m = driveDetail(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	if m.state != stateComment {
		t.Fatalf("expected stateComment, got %d", m.state)
	}
	if !m.composer.Focused() {
		t.Errorf("expected composer focused")
	}
	// esc cancels back to browsing.
	m = driveDetail(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.state != stateBrowsing {
		t.Fatalf("expected browsing after esc, got %d", m.state)
	}
}

func TestEscEmitsExit(t *testing.T) {
	m := loadedDetail(t)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Fatal("expected a command on esc")
	}
	if _, ok := cmd().(detailExitMsg); !ok {
		t.Fatalf("expected detailExitMsg from esc command")
	}
}

func TestRenderDiffMarksAddsAndDels(t *testing.T) {
	th := theme.New(theme.DefaultPalette())
	out := renderDiff(th, gh.FileDiff{
		Filename: "x.go",
		Patch:    "@@ -1,2 +1,2 @@\n-removed\n+added\n unchanged",
	}, 80)
	for _, want := range []string{"removed", "added", "unchanged"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered diff missing %q", want)
		}
	}
}

func TestConversationPageShowsComments(t *testing.T) {
	m := loadedDetail(t)
	m = driveDetail(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("v")})
	if m.page != pageConversation {
		t.Fatalf("expected conversation page")
	}
	view := m.View()
	for _, w := range []string{"Conversation", "@octocat", "looks tasty", "@github-actions", "c reply"} {
		if !strings.Contains(view, w) {
			t.Errorf("conversation view missing %q", w)
		}
	}
	// esc returns to diff page, not exit.
	m, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.page != pageDiff {
		t.Fatalf("expected esc to return to diff page")
	}
	if cmd != nil {
		if _, ok := cmd().(detailExitMsg); ok {
			t.Fatalf("esc from conversation should not exit to dashboard")
		}
	}
}
