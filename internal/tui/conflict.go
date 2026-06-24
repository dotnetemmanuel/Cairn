package tui

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/dotnetemmanuel/cairn/internal/conflict"
	"github.com/dotnetemmanuel/cairn/internal/theme"
	"github.com/dotnetemmanuel/cairn/internal/townie"
)

// Layout breakpoints (terminal columns). Below triBreakpoint the three side
// columns can't all read, so we collapse to a resolution hero + reference strip;
// the file rail defaults visible only when there's room for it.
const (
	triBreakpoint  = 150
	railBreakpoint = 160
	railWidth      = 22
)

// conflictFile is one unmerged file: its parsed spans, the per-conflict choices
// (index-aligned to the conflict spans), and a highlighter for its language.
type conflictFile struct {
	path  string
	spans []conflict.Span
	res   []conflict.Resolution
	hl    highlighter
	err   error // parse failure → raw view / forced edit
}

func (f conflictFile) conflicts() int { return len(f.res) }

func (f conflictFile) resolved() int {
	n := 0
	for _, r := range f.res {
		if r.Choice != conflict.ChoiceUnresolved {
			n++
		}
	}
	return n
}

func (f conflictFile) done() bool { return f.resolved() == f.conflicts() }

// spanLoader reads and parses one file into spans; injected so the model is
// testable without disk.
type spanLoader func(path string) ([]conflict.Span, error)

// diskLoader parses a file relative to dir off disk.
func diskLoader(dir string) spanLoader {
	return func(path string) ([]conflict.Span, error) {
		full := path
		if dir != "" {
			full = dir + "/" + path
		}
		b, err := os.ReadFile(full)
		if err != nil {
			return nil, err
		}
		return conflict.Parse(string(b))
	}
}

// conflictModel is the full-screen guided resolver.
type conflictModel struct {
	th    theme.Theme
	dir   string
	st    conflict.State
	files []conflictFile

	fileIdx int // active file
	hunkIdx int // active conflict within the active file

	railOpen bool
	sized    bool // first WindowSizeMsg seen (so resizes don't clobber a toggle)
	editing  bool
	editor   textarea.Model
	confirm  bool

	width, height int
	status        string
	done          bool
}

func newConflictModel(th theme.Theme, dir string, st conflict.State, load spanLoader) conflictModel {
	var files []conflictFile
	for _, p := range st.Files {
		spans, err := load(p)
		files = append(files, conflictFile{
			path:  p,
			spans: spans,
			err:   err,
			res:   make([]conflict.Resolution, conflict.Conflicts(spans)),
			hl:    newHighlighter(p),
		})
	}
	ta := textarea.New()
	ta.ShowLineNumbers = false
	ta.Placeholder = "Type the resolved lines, then ctrl+s…"
	return conflictModel{th: th, dir: dir, st: st, files: files, editor: ta}
}

// ---- pure accounting & navigation ----

// progress returns the resolved and total conflict counts across all files.
func (m conflictModel) progress() (done, total int) {
	for _, f := range m.files {
		done += f.resolved()
		total += f.conflicts()
	}
	return done, total
}

func (m conflictModel) allResolved() bool {
	d, t := m.progress()
	return t > 0 && d == t
}

// activeRes returns a pointer to the resolution for the active conflict, or nil
// when the active file has no conflicts.
func (m *conflictModel) activeRes() *conflict.Resolution {
	if m.fileIdx >= len(m.files) {
		return nil
	}
	f := m.files[m.fileIdx]
	if m.hunkIdx >= len(f.res) {
		return nil
	}
	return &m.files[m.fileIdx].res[m.hunkIdx]
}

// activeRegion returns the conflict Region under the cursor (nil if none).
func (m conflictModel) activeRegion() *conflict.Region {
	if m.fileIdx >= len(m.files) {
		return nil
	}
	n := -1
	for _, sp := range m.files[m.fileIdx].spans {
		if sp.Conflict != nil {
			n++
			if n == m.hunkIdx {
				return sp.Conflict
			}
		}
	}
	return nil
}

// step moves the (file, hunk) cursor by dir (±1) through every conflict in the
// tree, clamping at the ends. Files with zero conflicts are skipped.
func (m *conflictModel) step(dir int) {
	flat := m.flatten()
	if len(flat) == 0 {
		return
	}
	cur := 0
	for i, fc := range flat {
		if fc.file == m.fileIdx && fc.hunk == m.hunkIdx {
			cur = i
			break
		}
	}
	next := cur + dir
	if next < 0 {
		next = 0
	}
	if next >= len(flat) {
		next = len(flat) - 1
	}
	m.fileIdx, m.hunkIdx = flat[next].file, flat[next].hunk
}

type flatConflict struct{ file, hunk int }

func (m conflictModel) flatten() []flatConflict {
	var out []flatConflict
	for fi, f := range m.files {
		for hi := 0; hi < f.conflicts(); hi++ {
			out = append(out, flatConflict{fi, hi})
		}
	}
	return out
}

// advance moves to the next unresolved conflict (wrapping across files); if none
// remain it leaves the cursor put.
func (m *conflictModel) advance() {
	flat := m.flatten()
	if len(flat) == 0 {
		return
	}
	start := 0
	for i, fc := range flat {
		if fc.file == m.fileIdx && fc.hunk == m.hunkIdx {
			start = i
			break
		}
	}
	for off := 1; off <= len(flat); off++ {
		fc := flat[(start+off)%len(flat)]
		if m.files[fc.file].res[fc.hunk].Choice == conflict.ChoiceUnresolved {
			m.fileIdx, m.hunkIdx = fc.file, fc.hunk
			return
		}
	}
}

// nextFile / prevFile move to the first conflict of the adjacent file with any.
func (m *conflictModel) jumpFile(dir int) {
	n := len(m.files)
	for off := 1; off <= n; off++ {
		fi := (m.fileIdx + dir*off + n*off) % n
		if m.files[fi].conflicts() > 0 {
			m.fileIdx, m.hunkIdx = fi, 0
			return
		}
	}
}

// ---- update ----

func (m conflictModel) capturing() bool { return m.editing }

func (m conflictModel) Update(msg tea.Msg) (conflictModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		if !m.sized {
			m.railOpen = msg.Width >= railBreakpoint
			m.sized = true
		}
		m.editor.SetWidth(max(20, msg.Width-8))
		return m, nil

	case conflictContinueMsg:
		return m.handleContinue(msg)

	case tea.KeyMsg:
		if m.editing {
			return m.updateEditing(msg)
		}
		if m.confirm {
			return m.updateConfirm(msg)
		}
		return m.updateBrowsing(msg)
	}
	return m, nil
}

func (m conflictModel) updateBrowsing(msg tea.KeyMsg) (conflictModel, tea.Cmd) {
	switch msg.String() {
	case "n", "tab", "down", "j":
		m.step(1)
	case "N", "shift+tab", "up", "k":
		m.step(-1)
	case "]":
		m.jumpFile(1)
	case "[":
		m.jumpFile(-1)
	case "f":
		m.railOpen = !m.railOpen
	case "a":
		m.pick(conflict.ChoiceIncoming)
	case "d":
		m.pick(conflict.ChoiceYours)
	case "b":
		m.pick(conflict.ChoiceBoth)
	case "e":
		m.beginEdit()
	case "c", "enter":
		if m.allResolved() {
			m.confirm = true
		}
	case "esc":
		return m, func() tea.Msg { return conflictExitMsg{} }
	}
	return m, nil
}

func (m *conflictModel) pick(c conflict.Choice) {
	if r := m.activeRes(); r != nil {
		r.Choice = c
		r.Custom = ""
		m.advance()
	}
}

func (m *conflictModel) beginEdit() {
	reg := m.activeRegion()
	if reg == nil {
		return
	}
	seed := append(append([]string{}, reg.Incoming...), reg.Yours...)
	m.editor.SetValue(strings.Join(seed, "\n"))
	m.editor.Focus()
	m.editing = true
}

func (m conflictModel) updateEditing(msg tea.KeyMsg) (conflictModel, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.editing = false
		m.editor.Blur()
		return m, nil
	case "ctrl+s":
		if r := m.activeRes(); r != nil {
			r.Choice = conflict.ChoiceCustom
			r.Custom = m.editor.Value()
		}
		m.editing = false
		m.editor.Blur()
		m.advance()
		return m, nil
	}
	var cmd tea.Cmd
	m.editor, cmd = m.editor.Update(msg)
	return m, cmd
}

func (m conflictModel) updateConfirm(msg tea.KeyMsg) (conflictModel, tea.Cmd) {
	switch msg.String() {
	case "enter":
		m.confirm = false
		m.status = "Staging and continuing…"
		return m, m.continueCmd()
	case "u":
		m.confirm = false
		m.status = "Undoing…"
		return m, m.undoCmd()
	case "esc":
		m.confirm = false
	}
	return m, nil
}

// ---- continue / undo (integration) ----

type conflictContinueMsg struct {
	state conflict.State
	out   string
	err   error
	undo  bool
}

func (m conflictModel) continueCmd() tea.Cmd {
	dir, files := m.dir, m.files
	return func() tea.Msg {
		for _, f := range files {
			content := conflict.Apply(f.spans, f.res)
			if err := conflict.WriteResolved(dir, f.path, content); err != nil {
				return conflictContinueMsg{err: err}
			}
		}
		out, err := townie.New(dir).Run("continue", "")
		if err != nil {
			// Another round may have stopped on a fresh conflict; re-detect.
			st, _ := conflict.Detect(dir)
			return conflictContinueMsg{state: st, out: out, err: err}
		}
		st, _ := conflict.Detect(dir)
		return conflictContinueMsg{state: st, out: out}
	}
}

func (m conflictModel) undoCmd() tea.Cmd {
	dir := m.dir
	return func() tea.Msg {
		out, err := townie.New(dir).Run("undo", "")
		return conflictContinueMsg{out: out, err: err, undo: true}
	}
}

func (m conflictModel) handleContinue(msg conflictContinueMsg) (conflictModel, tea.Cmd) {
	if msg.undo {
		return m, func() tea.Msg { return conflictExitMsg{aborted: true, output: msg.out} }
	}
	// A fresh round of conflicts → reload and keep resolving.
	if msg.state.Op != conflict.OpNone && len(msg.state.Files) > 0 {
		next := newConflictModel(m.th, m.dir, msg.state, diskLoader(m.dir))
		next.width, next.height, next.sized, next.railOpen = m.width, m.height, true, m.railOpen
		next.editor.SetWidth(m.editor.Width())
		next.status = "Next round: more conflicts to resolve."
		return next, nil
	}
	// Clean → the operation finished.
	m.done = true
	return m, func() tea.Msg { return conflictExitMsg{output: msg.out} }
}

// conflictExitMsg leaves conflict mode; aborted marks an undo, done a finish.
type conflictExitMsg struct {
	aborted bool
	output  string
}

// ---- layout ----

type layoutKind int

const (
	layoutTri layoutKind = iota
	layoutHero
)

func layoutFor(width int) layoutKind {
	if width >= triBreakpoint {
		return layoutTri
	}
	return layoutHero
}

// paneWidths splits the body width into rail / incoming / resolution / yours,
// honoring the rail toggle and the layout. A pane is 0 when not shown.
func (m conflictModel) paneWidths() (rail, incoming, resolution, yours int) {
	if m.railOpen {
		rail = railWidth
	}
	body := max(8, m.width-rail)
	if layoutFor(m.width) == layoutTri {
		// Favor the resolution column slightly.
		side := max(8, (body-2)*5/16)
		resolution = max(8, body-2*side-2)
		return rail, side, resolution, side
	}
	// Hero: resolution owns the body; sides render in the reference strip.
	return rail, 0, body, 0
}

// ---- view ----

func (m conflictModel) View() string {
	if m.width == 0 {
		return ""
	}
	rail, wIn, wRes, wYours := m.paneWidths()

	body := m.renderActive(wIn, wRes, wYours)
	if m.railOpen {
		body = lipgloss.JoinHorizontal(lipgloss.Top, m.renderRail(rail), body)
	}

	parts := []string{m.headerBar(), body, m.footer()}
	view := lipgloss.JoinVertical(lipgloss.Left, parts...)
	if m.confirm {
		return lipgloss.JoinVertical(lipgloss.Left, view, m.confirmBox())
	}
	if m.editing {
		return lipgloss.JoinVertical(lipgloss.Left, view, m.editBox())
	}
	return view
}

func (m conflictModel) headerBar() string {
	done, total := m.progress()
	file := ""
	hunks := ""
	if m.fileIdx < len(m.files) {
		f := m.files[m.fileIdx]
		file = shortRepo(f.path)
		if f.conflicts() > 0 {
			hunks = fmt.Sprintf(" (hunk %d/%d)", m.hunkIdx+1, f.conflicts())
		}
	}
	left := infoStyle(m.th).Bold(true).Render(fmt.Sprintf("CONFLICT %d/%d", min(done+1, total), total))
	mid := mutedStyle(m.th).Render(" · " + file + hunks)
	right := fmt.Sprintf("  %s ← %s", infoStyle(m.th).Render(m.st.Yours), infoStyle(m.th).Render(m.st.Incoming))
	bar := left + mid + right
	return lipgloss.NewStyle().Width(m.width).Background(m.th.Surface).Padding(0, 1).Render(bar)
}

func (m conflictModel) renderRail(w int) string {
	var b strings.Builder
	done, total := m.progress()
	b.WriteString(infoStyle(m.th).Bold(true).Render(fmt.Sprintf("%d of %d resolved", done, total)) + "\n\n")
	for i, f := range m.files {
		glyph := mutedStyle(m.th).Render("○")
		switch {
		case f.done():
			glyph = okStyle(m.th).Render("✓")
		case f.resolved() > 0 || i == m.fileIdx:
			glyph = lipgloss.NewStyle().Foreground(m.th.Focus).Render("●")
		}
		name := truncate(shortRepo(f.path), max(4, w-9))
		row := fmt.Sprintf("%s %s %d/%d", glyph, name, f.resolved(), f.conflicts())
		if i == m.fileIdx {
			row = lipgloss.NewStyle().Foreground(m.th.Primary).Bold(true).Render("▌" + row)
		} else {
			row = " " + row
		}
		b.WriteString(row + "\n")
	}
	rail := b.String()
	return lipgloss.NewStyle().Width(w).Height(max(1, m.height-4)).
		BorderRight(true).Border(lipgloss.Border{Right: "│"}, false, true, false, false).
		BorderForeground(m.th.Overlay).Render(rail)
}

// renderActive renders the region under the cursor across the visible panes.
func (m conflictModel) renderActive(wIn, wRes, wYours int) string {
	if m.fileIdx >= len(m.files) {
		return ""
	}
	f := m.files[m.fileIdx]
	reg := m.activeRegion()
	if reg == nil {
		return mutedStyle(m.th).Render("\n  No conflicts in this file.")
	}
	res := m.files[m.fileIdx].res[m.hunkIdx]

	if layoutFor(m.width) == layoutTri {
		inc := m.sidePane(m.st.Incoming, reg.Incoming, f.hl, wIn, m.th.Info)
		you := m.sidePane(m.st.Yours, reg.Yours, f.hl, wYours, m.th.Warning)
		mid := m.resolutionPane(reg, res, f.hl, wRes)
		return lipgloss.JoinHorizontal(lipgloss.Top, inc, sep(m.th), mid, sep(m.th), you)
	}
	// Hero: a compact 2-up reference strip above the resolution.
	strip := m.referenceStrip(reg, f.hl, wRes)
	mid := m.resolutionPane(reg, res, f.hl, wRes)
	return lipgloss.JoinVertical(lipgloss.Left, strip, mid)
}

func (m conflictModel) sidePane(label string, lines []string, hl highlighter, w int, accent lipgloss.Color) string {
	head := infoStyle(m.th).Bold(true).Render(label)
	bg := blendRGB(m.th.Base, accent, 0.16)
	body := m.tintedCode(lines, hl, w, bg)
	return lipgloss.NewStyle().Width(w).Render(head + "\n" + body)
}

func (m conflictModel) resolutionPane(reg *conflict.Region, res conflict.Resolution, hl highlighter, w int) string {
	head := lipgloss.NewStyle().Foreground(m.th.Primary).Background(m.th.Surface).Bold(true).
		Width(w).Render("▸ RESOLUTION")
	var lines []string
	var bg string
	switch res.Choice {
	case conflict.ChoiceIncoming:
		lines, bg = reg.Incoming, blendRGB(m.th.Base, m.th.Success, 0.16)
	case conflict.ChoiceYours:
		lines, bg = reg.Yours, blendRGB(m.th.Base, m.th.Success, 0.16)
	case conflict.ChoiceBoth:
		lines, bg = append(append([]string{}, reg.Incoming...), reg.Yours...), blendRGB(m.th.Base, m.th.Success, 0.16)
	case conflict.ChoiceCustom:
		lines, bg = strings.Split(res.Custom, "\n"), blendRGB(m.th.Base, m.th.Success, 0.16)
	default:
		// Unresolved: show both sides faint so the choice is obvious.
		body := m.tintedCode(reg.Incoming, hl, w, blendRGB(m.th.Base, m.th.Info, 0.10)) + "\n" +
			mutedStyle(m.th).Render(strings.Repeat("╌", max(1, w))) + "\n" +
			m.tintedCode(reg.Yours, hl, w, blendRGB(m.th.Base, m.th.Warning, 0.10))
		hint := mutedStyle(m.th).Render("\n  a incoming · d yours · b both · e edit")
		return lipgloss.NewStyle().Width(w).Render(head + "\n" + body + hint)
	}
	return lipgloss.NewStyle().Width(w).Render(head + "\n" + m.tintedCode(lines, hl, w, bg))
}

func (m conflictModel) referenceStrip(reg *conflict.Region, hl highlighter, w int) string {
	half := max(6, (w-3)/2)
	inc := m.sidePane(m.st.Incoming, reg.Incoming, hl, half, m.th.Info)
	you := m.sidePane(m.st.Yours, reg.Yours, hl, half, m.th.Warning)
	return lipgloss.JoinHorizontal(lipgloss.Top, inc, sep(m.th), you)
}

// tintedCode highlights each line and lays it on a faint background to the pane
// width, reusing the diff pane's highlighter and tab handling.
func (m conflictModel) tintedCode(lines []string, hl highlighter, w int, bg string) string {
	if len(lines) == 0 {
		return mutedStyle(m.th).Render("(empty)")
	}
	bgStyle := lipgloss.NewStyle().Background(lipgloss.Color(bg)).Width(w)
	var out []string
	for _, ln := range lines {
		rendered := hl.line(expandTabs(ln))
		out = append(out, bgStyle.Render(truncate(rendered, w)))
	}
	return strings.Join(out, "\n")
}

func (m conflictModel) confirmBox() string {
	d, t := m.progress()
	_ = d
	msg := fmt.Sprintf("Stage %d file(s) & continue %s.\n[enter] continue   [u] undo all   [esc] back",
		len(m.files), opWord(m.st.Op))
	box := warnStyle(m.th).Bold(true).Render("Continue?") + "\n" + mutedStyle(m.th).Render(msg) +
		mutedStyle(m.th).Render(fmt.Sprintf("\n%d conflicts resolved.", t))
	return lipgloss.NewStyle().Width(m.width).Border(lipgloss.RoundedBorder()).
		BorderForeground(m.th.Focus).Padding(0, 1).Render(box)
}

func (m conflictModel) editBox() string {
	head := warnStyle(m.th).Render("Edit resolution") + mutedStyle(m.th).Render("   ctrl+s save · esc cancel")
	return lipgloss.JoinVertical(lipgloss.Left, head, m.editor.View())
}

func (m conflictModel) footer() string {
	help := "n/N conflict · [ ] file · a incoming · d yours · b both · e edit · f rail · esc back"
	if m.allResolved() {
		help = "all resolved · c continue · " + help
	}
	if m.status != "" {
		help = m.status
	}
	return lipgloss.NewStyle().Width(m.width).Foreground(m.th.Muted).Padding(0, 1).
		Render(truncate(help, max(10, m.width-2)))
}

func sep(th theme.Theme) string {
	return lipgloss.NewStyle().Foreground(th.Overlay).Render(" │ ")
}

func opWord(op conflict.Op) string {
	switch op {
	case conflict.OpRebase:
		return "the rebase"
	case conflict.OpMerge:
		return "the merge"
	default:
		return "the operation"
	}
}
