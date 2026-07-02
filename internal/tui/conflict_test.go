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

func TestPickStaysInPlaceThenNavigate(t *testing.T) {
	m := newTestConflict(t, 200, []string{"a.go"}, map[string]string{"a.go": twoConflicts()})
	m, _ = m.Update(ckey("a")) // take incoming on hunk 0
	if m.files[0].res[0].Choice != conflict.ChoiceIncoming {
		t.Fatal("hunk 0 not set to incoming")
	}
	if m.hunkIdx != 0 {
		t.Fatalf("pick should stay on hunk 0 (showing the result), got %d", m.hunkIdx)
	}
	m, _ = m.Update(ckey("n")) // user moves to hunk 1
	if m.hunkIdx != 1 {
		t.Fatalf("n did not move to hunk 1 (at %d)", m.hunkIdx)
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

func TestViewHeightIsExactlyTerminalHeight(t *testing.T) {
	lipgloss.SetColorProfile(termenv.Ascii)
	files := map[string]string{"a.go": twoConflicts(), "b.go": oneConflict("X", "Y")}
	for _, dim := range [][2]int{{200, 40}, {100, 20}, {80, 12}, {160, 50}} {
		m := newTestConflict(t, dim[0], []string{"a.go", "b.go"}, files)
		m, _ = m.Update(tea.WindowSizeMsg{Width: dim[0], Height: dim[1]})
		rows := strings.Count(m.View(), "\n") + 1
		if rows != dim[1] {
			t.Errorf("at %dx%d, View() has %d rows, want %d (ghosting risk on resize)", dim[0], dim[1], rows, dim[1])
		}
	}
}

func TestEnterAndExitConflictMode(t *testing.T) {
	orig := detectConflict
	defer func() { detectConflict = orig }()
	detectConflict = func(dir string) (conflict.State, error) {
		return conflict.State{Op: conflict.OpRebase, Incoming: "main", Yours: "feat", Files: []string{"missing.go"}}, nil
	}
	m := Model{th: theme.New(theme.DefaultPalette()), width: 200, height: 50, mode: modeStack}

	got, _ := m.Update(enterConflictMsg{dir: ""})
	m = got.(Model)
	if m.mode != modeConflict {
		t.Fatalf("expected modeConflict after enter, got %d", m.mode)
	}

	got, _ = m.Update(conflictExitMsg{})
	m = got.(Model)
	if m.mode != modeStack {
		t.Fatalf("expected modeStack after exit, got %d", m.mode)
	}
}

func TestEnterConflictNoopWhenClean(t *testing.T) {
	orig := detectConflict
	defer func() { detectConflict = orig }()
	detectConflict = func(dir string) (conflict.State, error) {
		return conflict.State{Op: conflict.OpNone}, nil
	}
	m := Model{th: theme.New(theme.DefaultPalette()), width: 200, height: 50, mode: modeStack}
	got, _ := m.Update(enterConflictMsg{dir: ""})
	if got.(Model).mode != modeStack {
		t.Fatal("clean detect must not switch into conflict mode")
	}
}

func TestResolvingClearsStaleRoundStatus(t *testing.T) {
	lipgloss.SetColorProfile(termenv.Ascii)
	m := newTestConflict(t, 200, []string{"a.go"}, map[string]string{"a.go": oneConflict("INC", "YOU")})
	m.status = "More conflicts to resolve." // a fresh-round announcement
	m, _ = m.updateBrowsing(ckey("a"))      // resolve the only conflict
	if m.status != "" {
		t.Errorf("status should clear after a resolution, got %q", m.status)
	}
	view := m.View()
	if !strings.Contains(view, "all resolved") {
		t.Errorf("footer should advance to 'all resolved', got:\n%s", view)
	}
	if strings.Contains(view, "More conflicts") {
		t.Error("stale 'More conflicts' must not linger after resolving the last conflict")
	}
}

func TestIntroGateProceedsOnKeyAndCancelsOnEsc(t *testing.T) {
	lipgloss.SetColorProfile(termenv.Ascii)
	// A sync auto-opens with the gate up.
	mk := func() conflictModel {
		m := newTestConflict(t, 200, []string{"a.go"}, map[string]string{"a.go": oneConflict("INC", "YOU")})
		m.intro = true
		return m
	}
	// The gate announces the conflict, not the resolver panes.
	if v := mk().View(); !strings.Contains(v, "conflicts to resolve") {
		t.Errorf("intro view missing the heads-up, got:\n%s", v)
	}
	// Any key dismisses the gate into the resolver (no exit command).
	m, cmd := mk().Update(ckey("x"))
	if m.intro {
		t.Error("any key should dismiss the gate")
	}
	if cmd != nil {
		t.Error("dismissing the gate should not emit a command")
	}
	// Esc backs out to the stack.
	_, cmd = mk().Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Fatal("esc on the gate should emit an exit command")
	}
	if _, ok := cmd().(conflictExitMsg); !ok {
		t.Errorf("esc should emit conflictExitMsg, got %T", cmd())
	}
}

func TestFinishedContinueShowsDoneScreenThenAnyKeyExits(t *testing.T) {
	lipgloss.SetColorProfile(termenv.Ascii)
	m := newTestConflict(t, 200, []string{"a.go"}, map[string]string{"a.go": oneConflict("INC", "YOU")})
	// A continue that finished the op (Op==None) should park on the done screen and
	// surface the git-town output, not snap straight back to the stack.
	m, cmd := m.Update(conflictContinueMsg{state: conflict.State{Op: conflict.OpNone}, out: "[feat] git rebase\nbranch is now in sync"})
	if !m.done {
		t.Fatal("expected done after a finished continue")
	}
	if cmd != nil {
		t.Error("done screen should wait for a key, not emit a command")
	}
	view := m.View()
	for _, want := range []string{"branch is now in sync", "any key to return"} {
		if !strings.Contains(view, want) {
			t.Errorf("done view missing %q", want)
		}
	}
	// Any key returns to the stack.
	_, cmd = m.Update(ckey("x"))
	if cmd == nil {
		t.Fatal("expected an exit command from the done screen")
	}
	if _, ok := cmd().(conflictExitMsg); !ok {
		t.Errorf("expected conflictExitMsg, got %T", cmd())
	}
}

func TestViewRendersGlyphsAndProgress(t *testing.T) {
	lipgloss.SetColorProfile(termenv.Ascii) // plain text for assertions
	m := newTestConflict(t, 200,
		[]string{"a.go", "b.go"},
		map[string]string{"a.go": oneConflict("INC", "YOU"), "b.go": oneConflict("P", "Q")})
	m.files[0].res[0].Choice = conflict.ChoiceIncoming
	view := m.View()
	// The active hunk is resolved, so its resolution pane header reads "✓ RESOLVED".
	for _, want := range []string{"1 of 2 resolved", "RESOLVED", "main", "feat"} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q", want)
		}
	}
}

// maxRowWidth returns the widest visible row (ANSI-aware) in a rendered frame.
func maxRowWidth(s string) int {
	mx := 0
	for _, ln := range strings.Split(s, "\n") {
		if w := lipgloss.Width(ln); w > mx {
			mx = w
		}
	}
	return mx
}

// TestConflictFramesFitEveryScreen guards the class of bug where a conflict-mode
// frame has a row WIDER than the terminal: paintBackground then wraps it, the
// fixed-height frame grows past the screen height, and the whole thing scrolls out
// of view (a blank "empty" screen). Every phase, at laptop→4K widths (incl. the
// tri/rail layout breakpoints at 150/160), must keep every row <= terminal width.
func TestConflictFramesFitEveryScreen(t *testing.T) {
	widths := []int{50, 60, 72, 80, 100, 120, 149, 150, 151, 159, 160, 161, 200, 234, 262, 320, 400, 500}
	heights := []int{18, 20, 24, 30, 40, 50, 60, 80, 91, 100}
	for _, w := range widths {
		for _, h := range heights {
			base := newTestConflict(t, w, []string{"a.go"}, map[string]string{"a.go": twoConflicts()})
			base, _ = base.Update(tea.WindowSizeMsg{Width: w, Height: h})

			phases := map[string]conflictModel{"resolver": base}
			intro := base
			intro.intro = true
			phases["intro"] = intro
			conf := base
			for i := range conf.files {
				for j := range conf.files[i].res {
					conf.files[i].res[j].Choice = conflict.ChoiceBoth
				}
			}
			conf.confirm = true
			phases["confirm"] = conf
			done := base
			done.done = true
			done.output = "line one\nline two\nline three"
			phases["done"] = done

			for name, cm := range phases {
				if mx := maxRowWidth(cm.View()); mx > w {
					t.Errorf("%s @ %dx%d: widest row %d > terminal width %d (wraps -> frame overflows -> blank screen)", name, w, h, mx, w)
				}
			}
		}
	}
}

// TestPaintBackgroundClipsOverWideRows is the systemic backstop: even if some view
// ever emits a row wider than the terminal, paintBackground must CLIP it (not wrap
// it), keeping the frame exactly m.height rows so it can't scroll out of view.
func TestPaintBackgroundClipsOverWideRows(t *testing.T) {
	m := Model{th: theme.New(theme.DefaultPalette()), width: 80, height: 24}
	over := strings.Repeat("X", 200) // 200 columns on an 80-column screen
	out := m.paintBackground(over + "\nsecond row")
	if rows := strings.Count(out, "\n") + 1; rows != 24 {
		t.Errorf("paintBackground produced %d rows, want 24 (an over-wide row must be clipped, not wrapped)", rows)
	}
	if mx := maxRowWidth(out); mx > 80 {
		t.Errorf("paintBackground left a %d-wide row on an 80-col screen", mx)
	}
}

// TestConfirmGateContinuesOnCandEnter: the confirm gate advertises "c continue"
// (footer) alongside "[enter] continue", so BOTH keys must continue — pressing c
// there used to be a no-op that forced the user to hit enter.
func TestConfirmGateContinuesOnCandEnter(t *testing.T) {
	for _, k := range []string{"enter", "c"} {
		m := newTestConflict(t, 200, []string{"a.go"}, map[string]string{"a.go": oneConflict("x", "y")})
		m.files[0].res[0].Choice = conflict.ChoiceIncoming // resolve the one conflict
		m.confirm = true
		m2, cmd := m.updateConfirm(ckey(k))
		if m2.confirm {
			t.Errorf("%q should leave the confirm gate", k)
		}
		if cmd == nil {
			t.Errorf("%q on the confirm gate should trigger continue (non-nil cmd)", k)
		}
	}
}
