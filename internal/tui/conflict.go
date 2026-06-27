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
	gitTown  bool // entered from a git-town op → continue via `git town continue`

	width, height int
	status        string
	done          bool
	output        string // git-town/continue output, shown on the done screen
	intro         bool   // auto-opened from a sync: show the "conflicts detected" gate first
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
// tree, wrapping at the ends so n/N (and the arrows) cycle. Files with zero
// conflicts are skipped.
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
	next := (cur + dir + len(flat)) % len(flat)
	m.fileIdx, m.hunkIdx = flat[next].file, flat[next].hunk
}

// flatPos is the 0-based position of the cursor among all conflicts (for the
// "CONFLICT n/total" header).
func (m conflictModel) flatPos() int {
	for i, fc := range m.flatten() {
		if fc.file == m.fileIdx && fc.hunk == m.hunkIdx {
			return i
		}
	}
	return 0
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
		m.editor.SetHeight(max(3, msg.Height-6))
		return m, nil

	case conflictContinueMsg:
		return m.handleContinue(msg)

	case tea.KeyMsg:
		if m.intro {
			// The gate: esc backs out (the conflict stays pending — resume with R);
			// any other key dismisses it into the resolver.
			if msg.String() == "esc" {
				return m, func() tea.Msg { return conflictExitMsg{} }
			}
			m.intro = false
			return m, nil
		}
		if m.done {
			// The done screen is a read-and-dismiss: any key returns to the stack.
			return m, func() tea.Msg { return conflictExitMsg{output: m.output} }
		}
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
		// A stale round-entry announcement ("More conflicts…") shouldn't outlive a
		// resolution — clear it so the footer can advance to "all resolved".
		m.status = ""
		// Stay on this hunk so the resolution pane shows the result immediately;
		// n/N moves on when the user is ready.
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
			m.status = "" // see pick(): a resolution clears any stale round status
		}
		m.editing = false
		m.editor.Blur()
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
	dir, files, op, viaGitTown := m.dir, m.files, m.st.Op, m.gitTown
	return func() tea.Msg {
		for _, f := range files {
			content := conflict.Apply(f.spans, f.res)
			if err := conflict.WriteResolved(dir, f.path, content); err != nil {
				return conflictContinueMsg{err: err, out: err.Error()}
			}
		}
		var out string
		var err error
		if viaGitTown {
			// git-town resumes the whole sync (rebase + remaining stack steps + push)
			// from its runstate; fall back to plain git if that runstate is gone.
			out, err = townie.New(dir).Run("continue", "")
			if err != nil {
				if pout, perr := conflict.ContinuePlain(dir, op); perr == nil {
					out, err = pout, nil
				} else {
					out = out + "\n" + pout
				}
			}
		} else {
			// A bare rebase/merge has no git-town runstate — `git town continue`
			// would no-op (or resume something stale), so finish it directly.
			out, err = conflict.ContinuePlain(dir, op)
		}
		st, _ := conflict.Detect(dir)
		return conflictContinueMsg{state: st, out: out, err: err}
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
	st := msg.state
	// A fresh round of conflicts → reload and keep resolving.
	if st.Op != conflict.OpNone && len(st.Files) > 0 {
		next := newConflictModel(m.th, m.dir, st, diskLoader(m.dir))
		next.width, next.height, next.sized, next.railOpen, next.gitTown =
			m.width, m.height, true, m.railOpen, m.gitTown
		next.editor.SetWidth(m.editor.Width())
		// Each fresh round from a sync gets the same gate (a later branch in the
		// stack just hit conflicts of its own). The gate already announces the new
		// round, so only fall back to a footer note when there's no gate.
		next.intro = m.gitTown
		if !next.intro {
			next.status = "More conflicts to resolve."
		}
		return next, nil
	}
	// Still mid-operation with nothing unmerged means continue didn't finish (it
	// errored). Surface that and stay, rather than pretending we're done — the
	// done check is Op==None, not "no unmerged files" (staging clears those while
	// the rebase is still open).
	if st.Op != conflict.OpNone {
		m.confirm = false
		m.status = "Continue failed: " + firstLine(msg.out)
		return m, nil
	}
	// Operation finished. Don't snap straight back to the stack — hold a done
	// screen showing the git-town flow (rebase + remaining stack steps + push) so
	// the user can read what happened, then any key returns (handled in Update).
	m.done = true
	m.output = msg.out
	m.status = ""
	return m, nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	if s == "" {
		return "unknown error"
	}
	return s
}

// enterConflictMsg asks the app to open the resolver for the repo at dir (emitted
// when a delegated op fails leaving unmerged paths). gitTown is true when the
// conflict came from a git-town op, so continue resumes via `git town continue`
// (which has the runstate); a bare rebase/merge continues with plain git.
type enterConflictMsg struct {
	dir     string
	gitTown bool
}

// detectConflict is the seam the app uses to inspect a conflicted repo; a package
// var so tests can stub it without a real git state.
var detectConflict = conflict.Detect

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
// honoring the rail toggle and the layout. A pane is 0 when not shown. The two
// inter-pane separators (and the rail's own right border) each cost one column.
func (m conflictModel) paneWidths() (rail, incoming, resolution, yours int) {
	used := 0
	if m.railOpen {
		rail = railWidth
		used = railWidth + 1 // + the rail's right border
	}
	body := max(8, m.width-used)
	if layoutFor(m.width) == layoutTri {
		avail := max(8, body-2) // two 1-col separators between the three panes
		side := max(8, avail*5/16)
		resolution = max(8, avail-2*side)
		return rail, side, resolution, side
	}
	// Hero: resolution owns the body; sides render in the reference strip.
	return rail, 0, body, 0
}

// bodyHeight is the row count between the header and footer — the height panes
// and the vertical separators stretch to.
func (m conflictModel) bodyHeight() int { return max(1, m.height-2) }

// vsep is a full-height vertical separator column h rows tall.
func (m conflictModel) vsep(h int) string {
	rows := make([]string, max(1, h))
	for i := range rows {
		rows[i] = "│"
	}
	return lipgloss.NewStyle().Foreground(m.th.Overlay).Render(strings.Join(rows, "\n"))
}

// ---- view ----

func (m conflictModel) View() string {
	if m.width == 0 || m.height == 0 {
		return ""
	}
	bodyH := m.bodyHeight()
	var body string
	switch {
	case m.intro:
		body = m.introView()
	case m.done:
		body = m.doneView()
	case m.editing:
		body = m.editBox()
	case m.confirm:
		body = m.confirmBox()
	default:
		rail, wIn, wRes, wYours := m.paneWidths()
		body = m.renderActive(wIn, wRes, wYours)
		if m.railOpen {
			body = lipgloss.JoinHorizontal(lipgloss.Top, m.renderRail(rail), body)
		}
	}
	// Emit exactly m.height rows (1 header + bodyH + 1 footer) so a resize can't
	// leave ghost lines from a taller previous frame.
	body = padToHeight(body, bodyH)
	return lipgloss.JoinVertical(lipgloss.Left, m.headerBar(), body, m.footer())
}

// padToHeight forces a block to exactly h rows (truncating or padding with blank
// lines), so every frame is the same height regardless of content.
func padToHeight(s string, h int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > h {
		lines = lines[:h]
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

func (m conflictModel) headerBar() string {
	_, total := m.progress()
	file := ""
	hunks := ""
	if m.fileIdx < len(m.files) {
		f := m.files[m.fileIdx]
		file = shortRepo(f.path)
		if f.conflicts() > 0 {
			hunks = fmt.Sprintf(" (hunk %d/%d)", m.hunkIdx+1, f.conflicts())
			if m.hunkIdx < len(f.res) && f.res[m.hunkIdx].Choice != conflict.ChoiceUnresolved {
				hunks += " ✓"
			}
		}
	}
	left := infoStyle(m.th).Bold(true).Render(fmt.Sprintf("CONFLICT %d/%d", m.flatPos()+1, total))
	mid := mutedStyle(m.th).Render(" · " + file + hunks)
	right := mutedStyle(m.th).Render("   incoming ") + infoStyle(m.th).Render(m.st.Incoming) +
		mutedStyle(m.th).Render(" into yours ") + infoStyle(m.th).Render(m.st.Yours)
	bar := left + mid + right
	return lipgloss.NewStyle().Width(m.width).MaxHeight(1).Background(m.th.Surface).Padding(0, 1).Render(bar)
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
			row = lipgloss.NewStyle().Foreground(m.th.Primary).Bold(true).Render(focusGlyph + row)
		} else {
			row = " " + row
		}
		b.WriteString(row + "\n")
	}
	rail := b.String()
	return lipgloss.NewStyle().Width(w).Height(m.bodyHeight()).
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
		bodyH := m.bodyHeight()
		fill := lipgloss.NewStyle().Height(bodyH)
		inc := fill.Render(m.sidePane(m.st.Incoming, reg.Incoming, f.hl, wIn, m.th.Info))
		mid := fill.Render(m.resolutionPane(reg, res, f.hl, wRes))
		you := fill.Render(m.sidePane(m.st.Yours, reg.Yours, f.hl, wYours, m.th.Warning))
		v := m.vsep(bodyH)
		return lipgloss.JoinHorizontal(lipgloss.Top, inc, v, mid, v, you)
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
	// The pane header doubles as the per-hunk status: a green "✓ RESOLVED" once this
	// hunk has a choice (the file-rail glyph stays file-level), so the eye gets
	// per-conflict feedback without waiting for the whole file to finish.
	headStyle := lipgloss.NewStyle().Background(m.th.Surface).Bold(true).Width(w)
	head := headStyle.Foreground(m.th.Primary).Render(focusGlyph + " RESOLUTION")
	if res.Choice != conflict.ChoiceUnresolved {
		head = headStyle.Foreground(m.th.Success).Render("✓ RESOLVED")
	}
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
		// Unresolved: the incoming/yours panes already show the two sides, so the
		// resolution pane just offers the choice — no need to repeat the code here.
		menu := mutedStyle(m.th).Render("Not resolved — choose:") + "\n\n" +
			infoStyle(m.th).Render("  a") + mutedStyle(m.th).Render("  take incoming · "+m.st.Incoming) + "\n" +
			warnStyle(m.th).Render("  d") + mutedStyle(m.th).Render("  take yours · "+m.st.Yours) + "\n" +
			okStyle(m.th).Render("  b") + mutedStyle(m.th).Render("  take both") + "\n" +
			mutedStyle(m.th).Render("  e  edit by hand")
		return lipgloss.NewStyle().Width(w).Render(head + "\n\n" + menu)
	}
	return lipgloss.NewStyle().Width(w).Render(head + "\n" + m.tintedCode(lines, hl, w, bg))
}

func (m conflictModel) referenceStrip(reg *conflict.Region, hl highlighter, w int) string {
	half := max(6, (w-1)/2)
	inc := m.sidePane(m.st.Incoming, reg.Incoming, hl, half, m.th.Info)
	you := m.sidePane(m.st.Yours, reg.Yours, hl, half, m.th.Warning)
	h := 1 + max(len(reg.Incoming), len(reg.Yours))
	return lipgloss.JoinHorizontal(lipgloss.Top, inc, m.vsep(h), you)
}

// tintedCode highlights each line and lays it on a faint background to the pane
// width, reusing the diff pane's highlighter and tab handling.
func (m conflictModel) tintedCode(lines []string, hl highlighter, w int, bg string) string {
	if len(lines) == 0 {
		return mutedStyle(m.th).Render("(empty)")
	}
	// Reuse the diff pane's tintRow: it re-asserts the "R;G;B" background after each
	// chroma per-token reset and pads to width, so the tint spans the whole line
	// instead of bleeding to the page background between syntax tokens.
	var out []string
	for _, ln := range lines {
		rendered := hl.line(expandTabs(ln))
		out = append(out, tintRow(truncate(rendered, w), bg, w))
	}
	return strings.Join(out, "\n")
}

// doneView is the read-and-dismiss screen shown after a successful continue: the
// git-town command flow that finished the op (rebase + remaining stack steps +
// push), tailed to fit, with a prompt to return to the stack.
func (m conflictModel) doneView() string {
	w := max(8, m.width-2)
	title := okStyle(m.th).Bold(true).Render("✓ Resolved — " + opWord(m.st.Op) + " continued")
	rule := lipgloss.NewStyle().Foreground(m.th.Focus).Render(strings.Repeat("─", w))
	out := strings.TrimRight(m.output, "\n")
	logBlock := mutedStyle(m.th).Render("(no output)")
	if out != "" {
		logBlock = styleRunLog(m.th, out, w)
	}
	// If the log is taller than the body, keep the tail — the push / "branch is now
	// in sync" lines at the bottom are the ones worth seeing.
	avail := max(1, m.bodyHeight()-4) // title, rule, blank, prompt
	logBlock = lastLines(logBlock, avail)
	prompt := mutedStyle(m.th).Render("any key to return to the stack")
	return lipgloss.JoinVertical(lipgloss.Left, title, rule, logBlock, "", prompt)
}

// lastLines keeps the final n lines of s (all of it when it's already shorter).
func lastLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}

// introView is the gate shown when a sync auto-opens the resolver: a calm,
// professional heads-up with the conflict count and the affected files, before
// the user commits to resolving. Any key proceeds; esc backs out (resume with R).
func (m conflictModel) introView() string {
	_, total := m.progress()
	title := warnStyle(m.th).Bold(true).Render("Sync paused — conflicts to resolve")
	lead := mutedStyle(m.th).Render(fmt.Sprintf(
		"The same lines changed on both sides, so %s can't replay cleanly. %d conflict(s) across %d file(s) need a decision before the sync can finish.",
		opWord(m.st.Op), total, len(m.files)))
	var files strings.Builder
	for _, f := range m.files {
		files.WriteString("\n  " + infoStyle(m.th).Render("• "+shortRepo(f.path)) +
			mutedStyle(m.th).Render(fmt.Sprintf("  %d conflict(s)", f.conflicts())))
	}
	keys := okStyle(m.th).Bold(true).Render("press any key") + mutedStyle(m.th).Render(" to resolve   ·   ") +
		warnStyle(m.th).Render("esc") + mutedStyle(m.th).Render(" to cancel (resume anytime with R)")
	box := title + "\n" + lead + "\n" + files.String() + "\n\n" + keys
	return lipgloss.NewStyle().Width(m.width).Border(lipgloss.RoundedBorder()).
		BorderForeground(m.th.Warning).Padding(0, 1).Render(box)
}

func (m conflictModel) confirmBox() string {
	_, t := m.progress()
	title := okStyle(m.th).Bold(true).Render("Continue?")
	desc := lipgloss.NewStyle().Foreground(m.th.Text).Render(
		fmt.Sprintf("Stage %d file(s) & continue %s.", len(m.files), opWord(m.st.Op)))
	// Colour the keys by intent: green to proceed, red for the destructive undo,
	// blue to back out — so the eye finds the action it wants at a glance.
	key := func(style lipgloss.Style, k, label string) string {
		return style.Bold(true).Render("["+k+"]") + mutedStyle(m.th).Render(" "+label)
	}
	keys := key(okStyle(m.th), "enter", "continue") + "   " +
		key(errStyle(m.th), "u", "undo all") + "   " +
		key(infoStyle(m.th), "esc", "back")
	status := okStyle(m.th).Render(fmt.Sprintf("✓ %d conflict(s) resolved.", t))
	box := title + "\n" + desc + "\n" + keys + "\n" + status
	return lipgloss.NewStyle().Width(m.width).Border(lipgloss.RoundedBorder()).
		BorderForeground(m.th.Success).Padding(0, 1).Render(box)
}

func (m conflictModel) editBox() string {
	head := warnStyle(m.th).Render("Edit resolution") + mutedStyle(m.th).Render("   ctrl+s save · esc cancel")
	return lipgloss.JoinVertical(lipgloss.Left, head, m.editor.View())
}

func (m conflictModel) footer() string {
	base := lipgloss.NewStyle().Width(m.width).Padding(0, 1).MaxHeight(1)
	if m.done || m.intro {
		return base.Render("")
	}
	if m.status != "" {
		return base.Foreground(m.th.Muted).Render(truncate(m.status, max(10, m.width-2)))
	}
	nav := "n/N conflict · [ ] file · a incoming · d yours · b both · e edit · f rail · esc back"
	if m.allResolved() {
		lead := okStyle(m.th).Bold(true).Render("all resolved · c continue")
		return base.Render(lead + mutedStyle(m.th).Render(" · "+nav))
	}
	return base.Foreground(m.th.Muted).Render(truncate(nav, max(10, m.width-2)))
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
