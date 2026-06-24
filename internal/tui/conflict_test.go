package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/dotnetemmanuel/cairn/internal/conflict"
	"github.com/dotnetemmanuel/cairn/internal/theme"
	"github.com/muesli/termenv"
)

// loaderFrom maps file path -> raw conflicted content, parsed on demand.
func loaderFrom(contents map[string]string) spanLoader {
	return func(path string) ([]conflict.Span, error) {
		return conflict.Parse(contents[path])
	}
}

func oneConflict(a, b string) string {
	return "ctx\n<<<<<<<\n" + a + "\n=======\n" + b + "\n>>>>>>>\ntail\n"
}

func twoConflicts() string {
	return "h\n<<<<<<<\nA1\n=======\nB1\n>>>>>>>\nm\n<<<<<<<\nA2\n=======\nB2\n>>>>>>>\nt\n"
}

func newTestConflict(t *testing.T, width int, files []string, contents map[string]string) conflictModel {
	t.Helper()
	st := conflict.State{Op: conflict.OpRebase, Incoming: "main", Yours: "feat", Files: files}
	m := newConflictModel(theme.New(theme.DefaultPalette()), "", st, loaderFrom(contents))
	m, _ = m.Update(tea.WindowSizeMsg{Width: width, Height: 40})
	return m
}

func ckey(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

func TestNewConflictModelSizesResolutions(t *testing.T) {
	m := newTestConflict(t, 200, []string{"a.go"}, map[string]string{"a.go": twoConflicts()})
	if len(m.files) != 1 || m.files[0].conflicts() != 2 {
		t.Fatalf("want 1 file / 2 conflicts, got %d files", len(m.files))
	}
	if !m.railOpen {
		t.Error("rail should default open on wide width")
	}
	m2 := newTestConflict(t, 100, []string{"a.go"}, map[string]string{"a.go": twoConflicts()})
	if m2.railOpen {
		t.Error("rail should default hidden on narrow width")
	}
}

func TestProgressCounts(t *testing.T) {
	m := newTestConflict(t, 200,
		[]string{"a.go", "b.go"},
		map[string]string{"a.go": twoConflicts(), "b.go": oneConflict("X", "Y")})
	if d, total := m.progress(); d != 0 || total != 3 {
		t.Fatalf("initial progress = %d/%d, want 0/3", d, total)
	}
	m.files[0].res[0].Choice = conflict.ChoiceIncoming
	if d, total := m.progress(); d != 1 || total != 3 {
		t.Fatalf("after one pick = %d/%d, want 1/3", d, total)
	}
	if m.files[0].done() {
		t.Error("file 0 has 2 conflicts, 1 resolved — not done")
	}
}

func TestStepCrossesFiles(t *testing.T) {
	m := newTestConflict(t, 200,
		[]string{"a.go", "b.go"},
		map[string]string{"a.go": oneConflict("X", "Y"), "b.go": twoConflicts()})
	if m.fileIdx != 0 || m.hunkIdx != 0 {
		t.Fatalf("start at %d/%d", m.fileIdx, m.hunkIdx)
	}
	m, _ = m.Update(ckey("n")) // a.go has 1 conflict → cross to b.go hunk 0
	if m.fileIdx != 1 || m.hunkIdx != 0 {
		t.Fatalf("after n at %d/%d, want 1/0", m.fileIdx, m.hunkIdx)
	}
	m, _ = m.Update(ckey("N")) // back to a.go
	if m.fileIdx != 0 || m.hunkIdx != 0 {
		t.Fatalf("after N at %d/%d, want 0/0", m.fileIdx, m.hunkIdx)
	}
}

func TestPickAdvancesToNextUnresolved(t *testing.T) {
	m := newTestConflict(t, 200, []string{"a.go"}, map[string]string{"a.go": twoConflicts()})
	m, _ = m.Update(ckey("a")) // take incoming on hunk 0
	if m.files[0].res[0].Choice != conflict.ChoiceIncoming {
		t.Fatal("hunk 0 not set to incoming")
	}
	if m.hunkIdx != 1 {
		t.Fatalf("did not advance to hunk 1 (at %d)", m.hunkIdx)
	}
	m, _ = m.Update(ckey("d")) // take yours on hunk 1
	if m.files[0].res[1].Choice != conflict.ChoiceYours {
		t.Fatal("hunk 1 not set to yours")
	}
	if !m.allResolved() {
		t.Error("both hunks chosen → allResolved should be true")
	}
}

func TestBothAndCustomApply(t *testing.T) {
	m := newTestConflict(t, 200, []string{"a.go"}, map[string]string{"a.go": oneConflict("INC", "YOU")})
	m, _ = m.Update(ckey("b"))
	out := conflict.Apply(m.files[0].spans, m.files[0].res)
	if out != "ctx\nINC\nYOU\ntail\n" {
		t.Errorf("both apply = %q", out)
	}
}

func TestEditModeCapturesAndStoresCustom(t *testing.T) {
	m := newTestConflict(t, 200, []string{"a.go"}, map[string]string{"a.go": oneConflict("INC", "YOU")})
	m, _ = m.Update(ckey("e"))
	if !m.editing || !m.capturing() {
		t.Fatal("e should enter edit mode and capture keys")
	}
	m.editor.SetValue("MERGED")
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	if m.editing {
		t.Error("ctrl+s should leave edit mode")
	}
	if m.files[0].res[0].Choice != conflict.ChoiceCustom || m.files[0].res[0].Custom != "MERGED" {
		t.Errorf("custom not stored: %+v", m.files[0].res[0])
	}
}

func TestRailToggle(t *testing.T) {
	m := newTestConflict(t, 200, []string{"a.go"}, map[string]string{"a.go": oneConflict("X", "Y")})
	open := m.railOpen
	m, _ = m.Update(ckey("f"))
	if m.railOpen == open {
		t.Error("f should toggle the rail")
	}
}

func TestLayoutFor(t *testing.T) {
	if layoutFor(200) != layoutTri {
		t.Error("wide -> tri")
	}
	if layoutFor(100) != layoutHero {
		t.Error("narrow -> hero")
	}
}

func TestViewRendersGlyphsAndProgress(t *testing.T) {
	lipgloss.SetColorProfile(termenv.Ascii) // plain text for assertions
	m := newTestConflict(t, 200,
		[]string{"a.go", "b.go"},
		map[string]string{"a.go": oneConflict("INC", "YOU"), "b.go": oneConflict("P", "Q")})
	m.files[0].res[0].Choice = conflict.ChoiceIncoming
	view := m.View()
	for _, want := range []string{"1 of 2 resolved", "RESOLUTION", "main", "feat"} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q", want)
		}
	}
}
