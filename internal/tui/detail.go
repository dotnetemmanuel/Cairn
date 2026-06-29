package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/dotnetemmanuel/cairn/internal/gh"
	"github.com/dotnetemmanuel/cairn/internal/theme"
)

// detailPage is the top-level view of the detail screen.
type detailPage int

const (
	pageDiff detailPage = iota
	pageConversation
)

// iconChat marks files/threads that carry inline review comments (width 2).
const iconChat = "💬"

// convReadW caps the conversation's text column so prose stays readable on wide
// terminals instead of wrapping edge-to-edge.
const convReadW = 100

// focusPane identifies which detail-screen region receives navigation keys.
type focusPane int

const (
	focusFiles focusPane = iota
	focusDiff
	focusInfo
)

// detailState is the modal state of the detail screen.
type detailState int

const (
	stateBrowsing detailState = iota
	stateComment
	stateReject      // request-changes reason
	stateLineComment // inline comment anchored to a diff line
	stateReply       // reply threaded under an existing inline comment
	stateConfirmApprove
	stateSubmitting
)

// detailModel is the PR review screen.
type detailModel struct {
	th     theme.Theme
	owner  string
	repo   string
	number int
	url    string
	title  string

	width, height int

	loading    bool
	refreshing bool // a reload of an already-loaded PR (r) vs the first load
	err        error
	detail     gh.PRDetail
	files      []gh.FileDiff

	page     detailPage
	selected int // index into files
	focus    focusPane
	showInfo bool

	diffVP     viewport.Model
	infoVP     viewport.Model
	convVP     viewport.Model
	hunks      []int          // hunk start line offsets for the selected file
	curHunk    int            // index into hunks of the currently-marked hunk
	diffCursor int            // patch-line index of the diff line cursor
	diffNLines int            // number of patch lines (for clamping)
	lineMeta   []diffLineMeta // per patch line: side + file line + code
	rowAt      []int          // patch-line index → first visual (wrapped) row

	state    detailState
	composer textarea.Model
	status   string // transient feedback line
	posted   bool   // user submitted a comment/reply/review this session → sync board on exit

	// Anchor for an in-progress inline comment (stateLineComment).
	anchorPath string
	anchorLine int
	anchorSide string

	// Target review-comment id for an in-progress reply (stateReply).
	replyTo     int
	replyAuthor string // whose comment we're replying to (for the composer title)

	// Conversation-page thread cursor: repliable inline threads in render order,
	// and the selected one (-1 = none). Lets you n/N to any thread and reply.
	convThreads []convThread
	convCursor  int
}

// convThread is a repliable inline-comment thread located in the rendered
// conversation: its starting visual row (for scrolling) and the anchor comment's
// REST id + author (for the reply).
type convThread struct {
	row    int
	id     int
	author string
}

func newDetail(th theme.Theme, it gh.Item) detailModel {
	owner, repo, _ := gh.SplitRepo(it.Repo)
	ta := textarea.New()
	ta.Placeholder = "Write a comment (GitHub-flavored Markdown)…"
	ta.ShowLineNumbers = false
	return detailModel{
		th:       th,
		owner:    owner,
		repo:     repo,
		number:   it.Number,
		url:      it.URL,
		title:    it.Title,
		loading:  true,
		showInfo: true,
		composer: ta,
		diffVP:   newVP(),
		infoVP:   newVP(),
		convVP:   newVP(),
	}
}

// newVP builds a viewport with vim-style scroll keys added alongside arrows.
func newVP() viewport.Model {
	vp := viewport.New(0, 0)
	vp.KeyMap.Up = key.NewBinding(key.WithKeys("up", "k"))
	vp.KeyMap.Down = key.NewBinding(key.WithKeys("down", "j"))
	return vp
}

// ---- messages & commands ----

type prLoadedMsg struct {
	detail gh.PRDetail
	files  []gh.FileDiff
	err    error
	keep   bool // preserve the current file/cursor/scroll (a post-action reload)
}

type actionDoneMsg struct {
	verb string
	err  error
}

// detailExitMsg returns control to the dashboard. posted is true when the user
// submitted something this session, so the dashboard can auto-sync all tabs.
type detailExitMsg struct{ posted bool }

func loadPR(owner, repo string, number int, keep bool) tea.Cmd {
	return func() tea.Msg {
		detail, derr := gh.FetchPRDetail(owner, repo, number)
		files, ferr := gh.FetchPRFiles(owner, repo, number)
		err := derr
		if err == nil {
			err = ferr
		}
		return prLoadedMsg{detail: detail, files: files, err: err, keep: keep}
	}
}

func doComment(owner, repo string, number int, body string) tea.Cmd {
	return func() tea.Msg {
		return actionDoneMsg{verb: "comment", err: gh.AddComment(owner, repo, number, body)}
	}
}

func doReview(owner, repo string, number int, event, body string) tea.Cmd {
	verb := map[string]string{"APPROVE": "approve", "REQUEST_CHANGES": "request-changes"}[event]
	return func() tea.Msg {
		return actionDoneMsg{verb: verb, err: gh.SubmitReview(owner, repo, number, event, body)}
	}
}

func doLineComment(owner, repo string, number int, sha, path string, line int, side, body string) tea.Cmd {
	return func() tea.Msg {
		err := gh.AddReviewComment(owner, repo, number, sha, path, line, side, body)
		return actionDoneMsg{verb: "line comment", err: err}
	}
}

func doReply(owner, repo string, number, commentID int, body string) tea.Cmd {
	return func() tea.Msg {
		err := gh.ReplyToReviewComment(owner, repo, number, commentID, body)
		return actionDoneMsg{verb: "reply", err: err}
	}
}

func (m detailModel) Init() tea.Cmd {
	return loadPR(m.owner, m.repo, m.number, false)
}

// ---- update ----

func (m detailModel) Update(msg tea.Msg) (detailModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.relayout()
		// A narrower window can hide the info pane out from under the focus.
		if !m.infoVisible() && m.focus == focusInfo {
			m.focus = focusDiff
		}
		if !m.loading && m.err == nil {
			m.renderDiffContent()
			m.refreshInfo()
		}
		return m, nil

	case prLoadedMsg:
		m.loading = false
		m.refreshing = false
		m.err = msg.err
		m.detail = msg.detail
		// A post-action reload keeps you where you were; the initial load
		// starts at the top of the first file.
		prevSel, prevCursor, prevHunk := m.selected, m.diffCursor, m.curHunk
		prevDiffY, prevConvY := m.diffVP.YOffset, m.convVP.YOffset
		m.files = msg.files
		if msg.err == nil {
			m.selected = 0
			if msg.keep && prevSel < len(m.files) {
				m.selected = prevSel
			}
			m.relayout()
			m.refreshDiff() // resets cursor/hunk to the file's defaults
			if msg.keep {
				if len(m.hunks) > 0 {
					m.curHunk = clamp(prevHunk, 0, len(m.hunks)-1)
				}
				m.diffCursor = clamp(prevCursor, 0, max(0, m.diffNLines-1))
				m.renderDiffContent()
				m.diffVP.SetYOffset(prevDiffY)
			}
			m.refreshInfo()
			m.refreshConv()
			if msg.keep {
				m.convVP.SetYOffset(prevConvY)
			}
		}
		return m, nil

	case actionDoneMsg:
		m.state = stateBrowsing
		if msg.err != nil {
			m.status = errStyle(m.th).Render("✗ " + msg.verb + " failed: " + msg.err.Error())
			return m, nil
		}
		m.status = okStyle(m.th).Render("✓ " + msg.verb + " submitted")
		// Remember the user changed their involvement so the dashboard syncs every
		// tab on exit (e.g. an Orgs PR you just commented on moves into Involved).
		m.posted = true
		// Reload conversation/checks so the new review/comment shows — but stay
		// on the current file, line, and scroll position.
		return m, loadPR(m.owner, m.repo, m.number, true)

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

// composing reports whether a text composer is open and should receive raw
// keystrokes (so global shortcuts like ? must not steal them).
func (m detailModel) composing() bool {
	return m.state == stateComment || m.state == stateReject ||
		m.state == stateLineComment || m.state == stateReply
}

func (m detailModel) handleKey(msg tea.KeyMsg) (detailModel, tea.Cmd) {
	// Composer states capture most keys.
	if m.composing() {
		switch msg.String() {
		case "esc":
			m.state = stateBrowsing
			m.composer.Reset()
			m.composer.Blur()
			return m, nil
		case "ctrl+s":
			body := strings.TrimSpace(m.composer.Value())
			st := m.state
			m.composer.Reset()
			m.composer.Blur()
			m.state = stateSubmitting
			m.status = mutedStyle(m.th).Render("submitting…")
			switch st {
			case stateComment:
				return m, doComment(m.owner, m.repo, m.number, body)
			case stateLineComment:
				return m, doLineComment(m.owner, m.repo, m.number, m.detail.HeadSHA,
					m.anchorPath, m.anchorLine, m.anchorSide, body)
			case stateReply:
				return m, doReply(m.owner, m.repo, m.number, m.replyTo, body)
			default:
				return m, doReview(m.owner, m.repo, m.number, "REQUEST_CHANGES", body)
			}
		default:
			var cmd tea.Cmd
			m.composer, cmd = m.composer.Update(msg)
			return m, cmd
		}
	}

	if m.state == stateConfirmApprove {
		switch msg.String() {
		case "y", "Y":
			m.state = stateSubmitting
			m.status = mutedStyle(m.th).Render("submitting…")
			return m, doReview(m.owner, m.repo, m.number, "APPROVE", "")
		default:
			m.state = stateBrowsing
			m.status = ""
			return m, nil
		}
	}

	// Browsing. Any keystroke dismisses a lingering transient status line
	// (e.g. a prior "approve failed" error); keys that set their own status
	// below re-set it after this clear.
	m.status = ""
	switch msg.String() {
	case "esc":
		// From the conversation page, esc returns to the diff; from the diff
		// page it exits back to the dashboard.
		if m.page == pageConversation {
			m.page = pageDiff
			return m, nil
		}
		posted := m.posted
		return m, func() tea.Msg { return detailExitMsg{posted: posted} }
	case "v":
		// Toggle the full conversation: v opens it, v again closes it back to
		// the diff (esc also closes it).
		if m.page == pageConversation {
			m.page = pageDiff
		} else {
			m.page = pageConversation
		}
		return m, nil
	case "d":
		m.page = pageDiff
		return m, nil
	case "tab", "right":
		m.focus = m.nextFocus(1)
		return m, nil
	case "shift+tab", "left":
		m.focus = m.nextFocus(-1)
		return m, nil
	case "i":
		m.showInfo = !m.showInfo
		m.relayout()
		if !m.infoVisible() && m.focus == focusInfo {
			m.focus = focusDiff
		}
		m.refreshDiff()
		return m, nil
	case "o":
		return m, openBrowser(m.url)
	case "c":
		// On the diff with the diff pane focused, c comments on the cursor
		// line (GitHub's "Add single comment"); otherwise it's a PR-level
		// conversation comment.
		if m.page == pageDiff && m.focus == focusDiff {
			return m.startLineComment("")
		}
		m.state = stateComment
		m.composer.Reset()
		m.composer.Placeholder = "Write a comment (GitHub-flavored Markdown)…"
		m.composer.Focus()
		return m, textarea.Blink
	case "s":
		// Suggest a change on the cursor line: a comment pre-filled with a
		// GitHub ```suggestion block seeded from the line's current content.
		if m.page == pageDiff && m.focus == focusDiff {
			return m.startLineComment("suggest")
		}
	case "r":
		// Reply when a thread is in focus (conversation thread, or the cursor's
		// diff line); otherwise refresh the PR — r=refresh, matching the dashboard.
		if m.page == pageConversation && len(m.convThreads) > 0 {
			return m.startConvReply()
		}
		if m.page == pageDiff && m.focus == focusDiff && len(m.lineComments()) > 0 {
			return m.startReply()
		}
		m.loading, m.refreshing = true, true
		return m, loadPR(m.owner, m.repo, m.number, true)
	case "x":
		m.state = stateReject
		m.composer.Reset()
		m.composer.Placeholder = "Reason for requesting changes…"
		m.composer.Focus()
		return m, textarea.Blink
	case "a":
		m.state = stateConfirmApprove
		m.status = warnStyle(m.th).Render(fmt.Sprintf("Approve PR #%d? y to confirm · any key cancels", m.number))
		return m, nil
	case "[", "p":
		if m.focus != focusDiff || msg.String() == "[" {
			m.selectFile(m.selected - 1)
			return m, nil
		}
	case "]":
		m.selectFile(m.selected + 1)
		return m, nil
	case "n":
		if m.page == pageConversation {
			m.moveConvThread(1)
			return m, nil
		}
		m.gotoHunk(1)
		return m, nil
	case "N":
		if m.page == pageConversation {
			m.moveConvThread(-1)
			return m, nil
		}
		m.gotoHunk(-1)
		return m, nil
	}

	// On the conversation page, navigation scrolls the thread.
	if m.page == pageConversation {
		var cmd tea.Cmd
		m.convVP, cmd = m.convVP.Update(msg)
		return m, cmd
	}

	// Route navigation to the focused pane.
	switch m.focus {
	case focusFiles:
		switch msg.String() {
		case "down", "j":
			m.selectFile(m.selected + 1)
		case "up", "k":
			m.selectFile(m.selected - 1)
		}
	case focusDiff:
		switch msg.String() {
		case "down", "j":
			m.moveCursor(1)
			return m, nil
		case "up", "k":
			m.moveCursor(-1)
			return m, nil
		default:
			var cmd tea.Cmd
			m.diffVP, cmd = m.diffVP.Update(msg)
			return m, cmd
		}
	case focusInfo:
		var cmd tea.Cmd
		m.infoVP, cmd = m.infoVP.Update(msg)
		return m, cmd
	}
	return m, nil
}

// infoVisible reports whether the right pane is actually on screen — both
// toggled on and wide enough to render. Tab focus and rendering both key off
// this so you can never tab into an invisible pane.
func (m *detailModel) infoVisible() bool {
	_, _, infoW := m.paneWidths()
	return m.showInfo && infoW > 0
}

func (m *detailModel) nextFocus(dir int) focusPane {
	order := []focusPane{focusFiles, focusDiff}
	if m.infoVisible() {
		order = append(order, focusInfo)
	}
	cur := 0
	for i, f := range order {
		if f == m.focus {
			cur = i
		}
	}
	return order[(cur+dir+len(order))%len(order)]
}

func (m *detailModel) selectFile(i int) {
	if len(m.files) == 0 {
		return
	}
	if i < 0 {
		i = 0
	}
	if i >= len(m.files) {
		i = len(m.files) - 1
	}
	if i != m.selected {
		m.selected = i
		m.refreshDiff()
	}
}

func (m *detailModel) gotoHunk(dir int) {
	if len(m.hunks) == 0 {
		return
	}
	// Cycle: n past the last hunk wraps to the first, N past the first to the last.
	n := len(m.hunks)
	m.curHunk = (m.curHunk + dir + n) % n
	// Park the line cursor on the hunk header, re-render (marks the active hunk +
	// cursor), then bring it into view. Move the cursor first so the contextual
	// right pane updates too.
	m.diffCursor = m.hunks[m.curHunk]
	m.renderDiffContent()
	m.refreshInfo()
	// Minimal scroll: only move the viewport when the active hunk header is
	// outside it, with a small lead-in — staying put when it's already visible.
	// Matches the conversation pane's n/N thread jumps so the two feel the same.
	row := m.visualRow(m.curHunkRow())
	top := m.diffVP.YOffset
	bottom := top + m.diffVP.Height - 1
	if row < top || row > bottom {
		target := row - 2
		if target < 0 {
			target = 0
		}
		m.diffVP.SetYOffset(target)
	}
}

// curHunkRow is the patch-line index of the active hunk header.
func (m *detailModel) curHunkRow() int {
	if m.curHunk >= 0 && m.curHunk < len(m.hunks) {
		return m.hunks[m.curHunk]
	}
	return 0
}

// commentCounts maps each rendered diff-line index to the number of inline
// comments anchored there, for the 💬N badges.
func (m *detailModel) commentCounts() map[int]int {
	if len(m.lineMeta) == 0 || len(m.detail.ReviewComments) == 0 {
		return nil
	}
	path := m.files[m.selected].Filename
	counts := map[int]int{}
	for _, c := range m.detail.ReviewComments {
		if c.Path != path {
			continue
		}
		for i, meta := range m.lineMeta {
			if meta.side == c.Side && meta.line == c.AnchorLine() {
				counts[i]++
			}
		}
	}
	return counts
}

// lineComments returns the inline comments anchored to the cursor's line.
func (m *detailModel) lineComments() []gh.ReviewComment {
	if m.diffCursor >= len(m.lineMeta) {
		return nil
	}
	meta := m.lineMeta[m.diffCursor]
	if meta.side == "" {
		return nil
	}
	path := m.files[m.selected].Filename
	var out []gh.ReviewComment
	for _, c := range m.detail.ReviewComments {
		if c.Path == path && c.Side == meta.side && c.AnchorLine() == meta.line {
			out = append(out, c)
		}
	}
	return out
}

// renderDiffContent rebuilds the diff viewport content for the current file,
// active hunk, cursor, and comment badges.
func (m *detailModel) renderDiffContent() {
	if len(m.files) == 0 {
		return
	}
	content, rowAt := renderDiff(m.th, m.files[m.selected], m.diffVP.Width,
		m.curHunk, m.diffCursor, m.commentCounts())
	m.rowAt = rowAt
	m.diffVP.SetContent(content)
}

// visualRow maps a patch-line index to its first visual row (accounting for
// soft-wrapped lines), for scrolling the viewport.
func (m *detailModel) visualRow(patchIdx int) int {
	if patchIdx >= 0 && patchIdx < len(m.rowAt) {
		return m.rowAt[patchIdx]
	}
	return 0
}

// moveCursor steps the diff line cursor, keeps it on screen, and refreshes the
// contextual right pane.
func (m *detailModel) moveCursor(dir int) {
	if m.diffNLines == 0 {
		return
	}
	m.diffCursor += dir
	if m.diffCursor < 0 {
		m.diffCursor = 0
	}
	if m.diffCursor >= m.diffNLines {
		m.diffCursor = m.diffNLines - 1
	}
	m.renderDiffContent()
	m.ensureCursorVisible()
	m.refreshInfo()
}

// ensureCursorVisible scrolls the diff viewport just enough to keep the cursor
// line within view.
func (m *detailModel) ensureCursorVisible() {
	row := m.visualRow(m.diffCursor)
	end := row // last visual row of this (possibly wrapped) line
	if m.diffCursor+1 < len(m.rowAt) {
		end = m.rowAt[m.diffCursor+1] - 1
	}
	top := m.diffVP.YOffset
	bottom := top + m.diffVP.Height - 1
	switch {
	case row < top:
		m.diffVP.SetYOffset(row)
	case end > bottom:
		off := end - m.diffVP.Height + 1
		if off > row { // a line taller than the pane: prefer showing its start
			off = row
		}
		m.diffVP.SetYOffset(off)
	}
}

// startReply opens the composer to reply to the inline-comment thread on the
// cursor's diff line, threading under the first comment found there. No-op when
// the line carries no comment thread.
func (m detailModel) startReply() (detailModel, tea.Cmd) {
	cs := m.lineComments()
	if len(cs) == 0 {
		m.status = mutedStyle(m.th).Render("no comment thread on this line — press c to start one")
		return m, nil
	}
	target := cs[0] // any comment in the thread; the reply joins that thread
	if target.DatabaseID == 0 {
		m.status = mutedStyle(m.th).Render("can't reply to this comment")
		return m, nil
	}
	m.replyTo = target.DatabaseID
	m.replyAuthor = target.Author
	m.state = stateReply
	m.composer.Reset()
	m.composer.Placeholder = "Reply to " + target.Author + " (GitHub-flavored Markdown)…"
	m.composer.Focus()
	return m, textarea.Blink
}

// startConvReply opens the composer to reply to the conversation thread under the
// thread cursor (full-conversation view). No-op when nothing is selected.
func (m detailModel) startConvReply() (detailModel, tea.Cmd) {
	if m.convCursor < 0 || m.convCursor >= len(m.convThreads) {
		m.status = mutedStyle(m.th).Render("no thread selected — press n/N to pick one")
		return m, nil
	}
	t := m.convThreads[m.convCursor]
	m.replyTo = t.id
	m.replyAuthor = t.author
	m.state = stateReply
	m.composer.Reset()
	m.composer.Placeholder = "Reply to " + t.author + " (GitHub-flavored Markdown)…"
	m.composer.Focus()
	return m, textarea.Blink
}

// startLineComment opens the composer anchored to the cursor's diff line. mode
// "suggest" pre-fills a GitHub ```suggestion block seeded with the line's text.
func (m detailModel) startLineComment(mode string) (detailModel, tea.Cmd) {
	if m.diffCursor >= len(m.lineMeta) {
		return m, nil
	}
	meta := m.lineMeta[m.diffCursor]
	if meta.side == "" {
		m.status = mutedStyle(m.th).Render("can't comment here — pick a code line")
		return m, nil
	}
	m.anchorPath = m.files[m.selected].Filename
	m.anchorLine = meta.line
	m.anchorSide = meta.side
	m.state = stateLineComment
	m.composer.Reset()
	if mode == "suggest" {
		m.composer.SetValue("```suggestion\n" + meta.code + "\n```\n")
		m.composer.Placeholder = "Edit the suggested change…"
	} else {
		m.composer.Placeholder = "Comment on this line (GitHub-flavored Markdown)…"
	}
	m.composer.Focus()
	return m, textarea.Blink
}

// ---- layout ----

const (
	detailHeaderH = 3 // two info lines + a focus-colored rule
	detailFooterH = 1
	composerH     = 8 // textarea rows when the comment composer is open
)

// bottomReserve is how many lines the bottom strip needs: a one-line footer
// while browsing, or the taller composer (textarea + its title) while writing.
func (m detailModel) bottomReserve() int {
	switch m.state {
	case stateComment, stateReject, stateLineComment, stateReply:
		return composerH + 1
	}
	return detailFooterH
}

func (m *detailModel) paneWidths() (files, diff, info int) {
	// Lay out within the indented body width so header/footer bars stay flush.
	w := bodyWidth(m.width)
	files = 30
	if files > w/3 {
		files = w / 3
	}
	if files < 16 {
		files = 16
	}
	info = 0
	if m.showInfo && w >= 100 {
		info = 52
		if info > w*2/5 {
			info = w * 2 / 5
		}
	}
	gaps := 1
	if info > 0 {
		gaps = 2
	}
	diff = w - files - info - gaps
	if diff < 10 {
		diff = 10
	}
	return
}

func (m *detailModel) relayout() {
	if m.width == 0 || m.height == 0 {
		return
	}
	bodyH := m.height - detailHeaderH - detailFooterH
	if bodyH < 3 {
		bodyH = 3
	}
	_, diffW, infoW := m.paneWidths()
	vpH := bodyH - 2 // pane title + its underline rule
	if vpH < 1 {
		vpH = 1
	}
	m.diffVP.Width = diffW
	m.diffVP.Height = vpH
	if infoW > 0 {
		m.infoVP.Width = infoW
		m.infoVP.Height = vpH
	}
	// The conversation page spans the indented body width.
	m.convVP.Width = bodyWidth(m.width)
	m.convVP.Height = vpH
	m.composer.SetWidth(m.width - 4)
	m.composer.SetHeight(composerH)
}

func (m *detailModel) refreshDiff() {
	if len(m.files) == 0 {
		m.diffVP.SetContent(mutedStyle(m.th).Render("  (no files changed)"))
		m.hunks = nil
		m.lineMeta = nil
		m.diffNLines = 0
		return
	}
	f := m.files[m.selected]
	m.hunks = hunkLineIndexes(f.Patch)
	m.lineMeta = patchLineMeta(f.Patch)
	m.diffNLines = len(m.lineMeta)
	m.curHunk = 0
	// Start the cursor on the first real change rather than the hunk header.
	m.diffCursor = 0
	if len(m.hunks) > 0 && m.hunks[0]+1 < m.diffNLines {
		m.diffCursor = m.hunks[0] + 1
	}
	m.renderDiffContent()
	m.diffVP.GotoTop()
}

func (m *detailModel) refreshInfo() {
	m.infoVP.SetContent(m.renderInfo())
}

// restyle swaps the theme and re-renders the cached viewports in place (a live
// light/dark toggle). It preserves the cursor, selection, and scroll positions —
// only the colors change. A still-loading screen needs no work: its next render
// already uses the new theme.
func (m *detailModel) restyle(th theme.Theme) {
	m.th = th
	if m.loading || m.err != nil {
		return
	}
	if len(m.files) > 0 {
		m.renderDiffContent()
	} else {
		m.diffVP.SetContent(mutedStyle(m.th).Render("  (no files changed)"))
	}
	m.refreshInfo()
	m.renderConvContent()
}

// refreshConv re-renders the conversation, places the thread cursor on the first
// repliable thread (if any), and scrolls to the top — a full reload.
func (m *detailModel) refreshConv() {
	_, threads := m.renderConversation()
	switch {
	case len(threads) == 0:
		m.convCursor = -1
	case m.convCursor < 0 || m.convCursor >= len(threads):
		m.convCursor = 0
	}
	m.renderConvContent()
	m.convVP.GotoTop()
}

// renderConvContent rebuilds the conversation viewport content + thread index for
// the current cursor, leaving scroll position untouched.
func (m *detailModel) renderConvContent() {
	content, threads := m.renderConversation()
	m.convThreads = threads
	m.convVP.SetContent(content)
}

// moveConvThread steps the conversation thread cursor (dir +1/-1), re-renders to
// move the highlight, and scrolls the selected thread into view.
func (m *detailModel) moveConvThread(dir int) {
	n := len(m.convThreads)
	if n == 0 {
		m.status = mutedStyle(m.th).Render("no inline threads to navigate")
		return
	}
	if m.convCursor < 0 {
		m.convCursor = 0
	} else {
		m.convCursor = (m.convCursor + dir + n) % n
	}
	m.renderConvContent()
	// Only scroll when the selected thread's header is outside the viewport —
	// staying put when it's already visible (no jolt on a short hop).
	row := m.convThreads[m.convCursor].row
	top := m.convVP.YOffset
	bottom := top + m.convVP.Height - 1
	if row < top || row > bottom {
		target := row - 2 // a little lead-in above the header
		if target < 0 {
			target = 0
		}
		m.convVP.SetYOffset(target)
	}
}

// ---- view ----

func (m detailModel) View(spinner string) string {
	if m.width == 0 {
		return "loading…"
	}
	if m.loading {
		// A refresh keeps the header/footer chrome (we still hold the PR data) and
		// shows an animated spinner in the body, like the dashboard's section
		// loading; the initial load is just the spinner line.
		if m.refreshing {
			// White text, blue spinner — same as the dashboard's loading line.
			msg := lipgloss.NewStyle().Foreground(m.th.Text).
				Render(fmt.Sprintf("refreshing PR #%d…", m.number))
			body := indentBody("\n  " + spinner + " " + msg)
			return lipgloss.JoinVertical(lipgloss.Left, m.viewHeader(), body, m.viewFooter())
		}
		return fmt.Sprintf("\n  %s loading PR #%d…", spinner, m.number)
	}
	if m.err != nil {
		return errStyle(m.th).Render(fmt.Sprintf("\n  failed to load PR #%d: %v\n\n  esc to go back", m.number, m.err))
	}

	header := m.viewHeader()
	var body string
	if m.page == pageConversation {
		body = m.viewConversation()
	} else {
		body = m.viewBody()
	}
	// Header and footer are full-width bars (flush); the body is indented.
	body = indentBody(body)

	if m.composing() {
		return lipgloss.JoinVertical(lipgloss.Left, header, body, m.viewComposer())
	}
	return lipgloss.JoinVertical(lipgloss.Left, header, body, m.viewFooter())
}

func (m detailModel) viewConversation() string {
	bodyH := m.height - detailHeaderH - m.bottomReserve()
	if bodyH < 3 {
		bodyH = 3
	}
	n := len(m.detail.Comments) + len(m.detail.Reviews) + len(m.detail.ReviewComments)
	title := fmt.Sprintf("Conversation · %d items", n)
	return m.paneBox(title, m.convVP.View(), bodyWidth(m.width), bodyH, true)
}

// renderConversation builds the full-width thread. Reviews lead with their
// summary; the inline comments left as part of that review are rendered indented
// beneath it, each with the cited code shown above the comment.
func (m detailModel) renderConversation() (string, []convThread) {
	w := m.convVP.Width
	if w < 8 {
		w = 8
	}
	// Cap prose at a comfortable reading column on wide terminals — full-width
	// wrapping is what made long comments hard to scan.
	tw := w
	if tw > convReadW {
		tw = convReadW
	}

	entries := m.detail.Timeline()
	var b strings.Builder
	var threads []convThread
	row := 0
	write := func(s string) { b.WriteString(s); row += strings.Count(s, "\n") }
	// anchor records a repliable inline thread at the current row and reports
	// whether it is the selected one (so its header gets the cursor bar).
	anchor := func(e gh.TimelineEntry, startRow int) bool {
		if e.DatabaseID == 0 {
			return false
		}
		sel := len(threads) == m.convCursor
		threads = append(threads, convThread{row: startRow, id: e.DatabaseID, author: e.Author})
		return sel
	}

	rule := mutedStyle(m.th).Render(strings.Repeat("─", tw))
	compact := func(k gh.TimelineKind) bool { return k == gh.KindCommit || k == gh.KindEvent }
	for i, e := range entries {
		if i > 0 {
			// Blank lines above and below the rule so each block reads on its own;
			// consecutive compact rows (commits/events) stack tightly with no divider.
			if !(compact(e.Kind) && compact(entries[i-1].Kind)) {
				write("\n" + rule + "\n\n")
			}
		}

		// Compact single-line rows: pushed commits and lifecycle events.
		if e.Kind == gh.KindCommit {
			write(m.renderCommitRow(e, tw) + "\n")
			continue
		}
		if e.Kind == gh.KindEvent {
			write(m.renderEventRow(e) + "\n")
			continue
		}

		// A standalone inline comment (not surfaced under a review) renders its own
		// header + cited code + body.
		if e.Kind == gh.KindInline {
			sel := anchor(e, row)
			write(m.renderInlineComment(e, tw, sel) + "\n")
			continue
		}

		write(m.conversationHeader(e) + "\n")
		body := strings.TrimSpace(e.Body)
		switch {
		case body != "":
			write(renderMarkdown(body, tw, m.th) + "\n")
		case len(e.Children) > 0:
			write(mutedStyle(m.th).Render(reviewInlineNote(len(e.Children))) + "\n")
		default:
			write(mutedStyle(m.th).Render("(no message)") + "\n")
		}

		// A review's inline comments, indented under it with a dotted guide. Each is
		// fenced by the same horizontal rule as top-level comments — including before
		// the first one, which separates the review summary from its inline notes — so
		// every comment reads as its own block.
		if len(e.Children) > 0 {
			prefix := mutedStyle(m.th).Render("  ┊ ")
			childW := tw - 4
			if childW < 8 {
				childW = 8
			}
			rule := mutedStyle(m.th).Render(strings.Repeat("─", tw))
			for _, ch := range e.Children {
				write("\n" + rule + "\n\n")
				sel := anchor(ch, row)
				write(indentBlock(m.renderInlineComment(ch, childW, sel), prefix) + "\n")
			}
		}
	}
	if len(entries) <= 1 && strings.TrimSpace(m.detail.Body) == "" {
		return mutedStyle(m.th).Render("No conversation yet. Press c to comment."), nil
	}
	return b.String(), threads
}

// renderCommitRow is the compact one-line timeline row for a pushed commit:
// a node glyph, the message headline, the short sha, then the author.
func (m detailModel) renderCommitRow(e gh.TimelineEntry, w int) string {
	node := mutedStyle(m.th).Render("◦")
	sha := ""
	if len(e.SHA) >= 7 {
		sha = mutedStyle(m.th).Render(e.SHA[:7])
	}
	who := infoStyle(m.th).Render("@" + e.Author)
	msg := lipgloss.NewStyle().Foreground(m.th.Text).
		Render(truncate(strings.TrimSpace(e.Body), max(10, w-26)))
	return node + " " + msg + "  " + sha + "  " + who
}

// renderEventRow is the compact one-line timeline row for a lifecycle event
// (ready-for-review, review requested, merged, closed, reopened, draft).
func (m detailModel) renderEventRow(e gh.TimelineEntry) string {
	glyph, text, c := m.eventDesc(e)
	icon := lipgloss.NewStyle().Foreground(c).Render(glyph)
	who := infoStyle(m.th).Bold(true).Render("@" + e.Author)
	return icon + " " + who + " " + mutedStyle(m.th).Render(text+" · "+relTime(e.CreatedAt))
}

// eventDesc maps a lifecycle event to its glyph, phrase, and accent color.
func (m detailModel) eventDesc(e gh.TimelineEntry) (string, string, lipgloss.Color) {
	switch e.Event {
	case "READY_FOR_REVIEW":
		return "✓", "marked this ready for review", m.th.Success
	case "REVIEW_REQUESTED":
		if e.Subject != "" {
			return "◇", "requested a review from @" + e.Subject, m.th.Focus
		}
		return "◇", "requested a review", m.th.Focus
	case "MERGED":
		return "●", "merged this", m.th.Primary
	case "CLOSED":
		return "●", "closed this", m.th.Danger
	case "REOPENED":
		return "●", "reopened this", m.th.Success
	case "CONVERT_TO_DRAFT":
		return "✎", "marked this as a draft", m.th.Muted
	}
	return "•", strings.ToLower(e.Event), m.th.Muted
}

// renderInlineComment renders one inline code comment: a "who · where" header,
// the cited code context, a blank line, then the comment body.
func (m detailModel) renderInlineComment(e gh.TimelineEntry, w int, selected bool) string {
	who := infoStyle(m.th).Bold(true).Render("@" + e.Author)
	loc := infoStyle(m.th).Render(fmt.Sprintf("%s:%d", shortRepo(e.Path), e.Line))
	header := who + " " + mutedStyle(m.th).Render("on ") + loc + mutedStyle(m.th).Render(" · "+relTime(e.CreatedAt))
	if selected {
		// Conversation thread cursor: a full-width primary-on-surface bar makes the
		// selected thread unmistakable (a lone ▌ read too faint).
		plain := fmt.Sprintf("%s @%s on %s:%d · %s", focusGlyph, e.Author, shortRepo(e.Path), e.Line, relTime(e.CreatedAt))
		header = lipgloss.NewStyle().Foreground(m.th.Primary).Background(m.th.Surface).
			Bold(true).Width(w).Render(plain)
	}

	var b strings.Builder
	b.WriteString(header + "\n")
	if cite := m.renderCitation(e.DiffHunk, e.Side, w); cite != "" {
		b.WriteString(cite + "\n\n") // blank line separates the code from the comment
	}
	body := strings.TrimSpace(e.Body)
	if body == "" {
		b.WriteString(mutedStyle(m.th).Render("(no message)"))
	} else {
		b.WriteString(renderMarkdown(body, w, m.th))
	}

	// Threaded replies render beneath the anchor with a deeper guide and no
	// citation — they share the anchor's code location, so re-citing it is noise.
	for _, r := range e.Replies {
		b.WriteString("\n" + m.renderThreadReply(r.Author, r.Body, r.CreatedAt, w))
	}
	return b.String()
}

// renderThreadReply renders one threaded reply — a │ guide, a ╰→ author line, and
// the body indented to align beneath it — the vocabulary shared by the full
// conversation thread and the contextual line-thread pane. The returned block has
// no leading newline; callers separate replies with one.
//
// The ╰→ marks the author line once; the body aligns beneath it (7 cols: 4 indent
// + "╰→ "), so multi-line replies don't repeat the arrow. Box-drawing corner so the
// guide pipe lands exactly on it (│ and ╰ are both centered in their cell, where an
// arrow glyph's stem sits left-of-centre and never quite lines up). The guide sits
// one row above each reply, threading it to the comment above.
func (m detailModel) renderThreadReply(author, body string, when time.Time, w int) string {
	marker := mutedStyle(m.th).Render("    ╰→ ")
	guide := mutedStyle(m.th).Render("    │")
	cont := "       "
	replyW := w - 7
	if replyW < 8 {
		replyW = 8
	}
	header := infoStyle(m.th).Bold(true).Render("@"+author) + mutedStyle(m.th).Render(" · "+relTime(when))
	rbody := strings.TrimSpace(body)
	if rbody == "" {
		rbody = mutedStyle(m.th).Render("(no message)")
	} else {
		rbody = renderMarkdown(rbody, replyW, m.th)
	}
	return guide + "\n" + indentBlock(header, marker) + "\n" + indentBlock(rbody, cont)
}

// citeLine is one line of a code citation: its file line number on the comment's
// side (0 if the line doesn't exist on that side, e.g. a deletion shown on the
// new side), the diff marker, and the de-tabbed text.
type citeLine struct {
	num    int
	marker byte
	text   string
}

// renderCitation renders the cited code above a comment: the trailing lines of
// the diff hunk the comment is anchored to, with file line numbers in a gutter,
// a guide, and add/remove coloring. It is a code citation, not the full diff —
// just enough context. Tabs are expanded and common indentation stripped so the
// snippet stays inside the column (raw tabs were overflowing the pane).
func (m detailModel) renderCitation(diffHunk, side string, w int) string {
	if strings.TrimSpace(diffHunk) == "" {
		return ""
	}
	leftSide := side == "LEFT"

	// Walk the hunk, tracking old/new line counters from the @@ header, and tag
	// each line with the number to show for the comment's side.
	var rows []citeLine
	oldLn, newLn := 0, 0
	for _, ln := range strings.Split(strings.TrimRight(diffHunk, "\n"), "\n") {
		if strings.HasPrefix(ln, "@@") {
			oldLn, newLn = parseHunkStarts(ln)
			continue
		}
		marker, body := byte(' '), ln
		if len(ln) > 0 {
			marker, body = ln[0], ln[1:]
		}
		num := 0
		switch marker {
		case '+':
			if !leftSide {
				num = newLn
			}
			newLn++
		case '-':
			if leftSide {
				num = oldLn
			}
			oldLn++
		default: // context line — present on both sides
			if leftSide {
				num = oldLn
			} else {
				num = newLn
			}
			oldLn++
			newLn++
		}
		rows = append(rows, citeLine{num: num, marker: marker, text: strings.ReplaceAll(body, "\t", "  ")})
	}
	for len(rows) > 0 && strings.TrimSpace(rows[0].text) == "" {
		rows = rows[1:] // start on real code, not blank context
	}
	const maxLines = 4 // the lines just before/at the anchored line
	if len(rows) > maxLines {
		rows = rows[len(rows)-maxLines:]
	}

	// Strip the common leading indentation so deep code starts near the guide.
	texts := make([]string, len(rows))
	for i, r := range rows {
		texts[i] = r.text
	}
	if d := commonIndent(texts); d > 0 {
		for i := range rows {
			if len(rows[i].text) >= d {
				rows[i].text = rows[i].text[d:]
			}
		}
	}

	// Gutter wide enough for the largest line number.
	gw := 1
	for _, r := range rows {
		if n := len(fmt.Sprintf("%d", r.num)); r.num > 0 && n > gw {
			gw = n
		}
	}
	var out []string
	for _, r := range rows {
		st := mutedStyle(m.th)
		switch r.marker {
		case '+':
			st = lipgloss.NewStyle().Foreground(m.th.Success)
		case '-':
			st = lipgloss.NewStyle().Foreground(m.th.Danger)
		}
		gutter := strings.Repeat(" ", gw)
		if r.num > 0 {
			gutter = fmt.Sprintf("%*d", gw, r.num)
		}
		prefix := mutedStyle(m.th).Render(gutter + " │ ")
		out = append(out, prefix+st.Render(truncate(r.text, max(1, w-gw-3))))
	}
	return strings.Join(out, "\n")
}

// commonIndent returns the number of leading spaces shared by all non-blank
// lines (for de-indenting a code citation).
func commonIndent(lines []string) int {
	min := -1
	for _, ln := range lines {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		lead := len(ln) - len(strings.TrimLeft(ln, " "))
		if min < 0 || lead < min {
			min = lead
		}
	}
	if min < 0 {
		return 0
	}
	return min
}

// reviewInlineNote labels a review that has only inline comments (empty body).
func reviewInlineNote(n int) string {
	if n == 1 {
		return "left 1 inline comment:"
	}
	return fmt.Sprintf("left %d inline comments:", n)
}

// indentBlock prefixes every line of s with prefix (for nesting a rendered
// block under a parent entry).
func indentBlock(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		lines[i] = prefix + ln
	}
	return strings.Join(lines, "\n")
}

func (m detailModel) conversationHeader(e gh.TimelineEntry) string {
	who := infoStyle(m.th).Bold(true).Render("@" + e.Author)
	when := mutedStyle(m.th).Render(relTime(e.CreatedAt))
	switch e.Kind {
	case gh.KindDescription:
		return who + " " + mutedStyle(m.th).Render("opened this PR · "+relTime(e.CreatedAt))
	case gh.KindReview:
		return who + " " + reviewBadge(m.th, e.State) + " · " + when
	case gh.KindInline:
		loc := mutedStyle(m.th).Render(fmt.Sprintf("on %s:%d", shortRepo(e.Path), e.Line))
		return who + " " + loc + " · " + when
	default:
		return who + " " + mutedStyle(m.th).Render("commented · "+relTime(e.CreatedAt))
	}
}

func (m detailModel) viewHeader() string {
	num := lipgloss.NewStyle().Foreground(m.th.Info).Render(fmt.Sprintf("#%d", m.number))
	title := lipgloss.NewStyle().Foreground(m.th.Text).Bold(true).
		Render(truncate(m.title, max(10, m.width-20)))
	state := stateBadge(m.th, m.detail.State)
	if m.detail.IsDraft {
		state += "  " + draftBadge(m.th)
	}
	// Surface bar via surfaceBar so the trailing pad doesn't bleed to Base (the
	// "double background" on the header) — same fix as the dashboard masthead.
	line1 := surfaceBar(m.th, m.width, " "+fmt.Sprintf("%s %s  %s", num, title, state))

	refs := fmt.Sprintf("%s ← %s", m.detail.BaseRef, m.detail.HeadRef)
	stats := lipgloss.NewStyle().Foreground(m.th.Success).Render(fmt.Sprintf("+%d", m.detail.Additions)) +
		" " + lipgloss.NewStyle().Foreground(m.th.Danger).Render(fmt.Sprintf("-%d", m.detail.Deletions)) +
		mutedStyle(m.th).Render(fmt.Sprintf("  %d files · @%s", m.detail.ChangedFiles, m.detail.Author))
	line2 := lipgloss.NewStyle().Width(m.width).Padding(0, 1).
		Render(infoStyle(m.th).Render(refs) + "  " + stats)

	// A focus-colored rule under the header mirrors the dashboard's tab line.
	rule := lipgloss.NewStyle().Foreground(m.th.Focus).Render(strings.Repeat("─", max(1, m.width)))

	return lipgloss.JoinVertical(lipgloss.Left, line1, line2, rule)
}

func (m detailModel) viewBody() string {
	bodyH := m.height - detailHeaderH - m.bottomReserve()
	if bodyH < 3 {
		bodyH = 3
	}
	filesW, _, infoW := m.paneWidths()

	filePane := m.paneBox("Files", m.renderFileList(filesW, bodyH-2), filesW, bodyH, m.focus == focusFiles)
	diffTitle := "Diff"
	if len(m.files) > 0 {
		diffTitle = "Diff · " + shortPath(m.files[m.selected].Filename, m.diffVP.Width-8)
		if len(m.hunks) > 1 {
			diffTitle += fmt.Sprintf("  hunk %d/%d", m.curHunk+1, len(m.hunks))
		}
	}
	diffPane := m.paneBox(diffTitle, m.diffVP.View(), m.diffVP.Width, bodyH, m.focus == focusDiff)

	panes := []string{filePane, diffPane}
	if infoW > 0 {
		infoTitle := "Conversation · Checks"
		if len(m.lineComments()) > 0 {
			infoTitle = "💬 Line thread"
		} else {
			// Mirror the header's status here in the pane title — the PR state
			// (OPEN/CLOSED/MERGED), plus a DRAFT flag since drafts stay under
			// OPEN — so it's visible without scrolling up.
			infoTitle += "   " + stateBadge(m.th, m.detail.State)
			if m.detail.IsDraft {
				infoTitle += "  " + draftBadge(m.th)
			}
		}
		infoPane := m.paneBox(infoTitle, m.infoVP.View(), infoW, bodyH, m.focus == focusInfo)
		panes = append(panes, infoPane)
	}
	// Vertical rules between the panes make the columns read clearly.
	sep := m.vSep(bodyH)
	cols := make([]string, 0, len(panes)*2-1)
	for i, p := range panes {
		if i > 0 {
			cols = append(cols, sep)
		}
		cols = append(cols, p)
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, cols...)
}

// paneBox renders a titled, fixed-size pane. The title sits over a rule that
// reads like a tab underline — focus-colored when active, a subtle overlay
// divider otherwise.
func (m detailModel) paneBox(title, content string, w, h int, focused bool) string {
	titleStyle := lipgloss.NewStyle().Width(w).Foreground(m.th.Muted)
	ruleColor := m.th.Overlay
	if focused {
		titleStyle = titleStyle.Foreground(m.th.Focus).Bold(true)
		ruleColor = m.th.Focus
	}
	rule := lipgloss.NewStyle().Foreground(ruleColor).Render(strings.Repeat("─", max(1, w)))
	body := lipgloss.NewStyle().Width(w).Height(h - 2).MaxHeight(h - 2).Render(content)
	return lipgloss.JoinVertical(lipgloss.Left, titleStyle.Render(title), rule, body)
}

// vSep is a full-height vertical divider drawn between panes.
func (m detailModel) vSep(h int) string {
	bar := lipgloss.NewStyle().Foreground(m.th.Overlay).Render("│")
	lines := make([]string, h)
	for i := range lines {
		lines[i] = bar
	}
	return strings.Join(lines, "\n")
}

func (m detailModel) renderFileList(w, h int) string {
	if len(m.files) == 0 {
		return mutedStyle(m.th).Render("  no files")
	}
	// Keep the selection within a scrolling window of height h.
	start := 0
	if m.selected >= h {
		start = m.selected - h + 1
	}
	end := start + h
	if end > len(m.files) {
		end = len(m.files)
	}

	var lines []string
	for i := start; i < end; i++ {
		f := m.files[i]
		// A 💬 to the right of files that carry inline review comments, so you
		// know to open them. Reserve its slot (2 cols) even when absent so the
		// rows stay aligned.
		bubble := "  "
		if m.fileHasComments(f.Filename) {
			bubble = iconChat
		}
		if i == m.selected {
			// A filled bar + ❯ cursor makes the current file unmistakable.
			sel := lipgloss.NewStyle().Width(w).Foreground(m.th.Base).Background(m.th.Primary).Bold(true)
			name := truncate(shortRepo(f.Filename), max(1, w-8))
			lines = append(lines, sel.Render(fmt.Sprintf("%s %s %s %s", focusGlyph, statusLetterPlain(f.Status), pad(name, max(1, w-8)), bubble)))
			continue
		}
		letter := statusLetter(m.th, f.Status)
		name := mutedStyle(m.th).Render(pad(truncate(shortRepo(f.Filename), w-13), max(1, w-13)))
		counts := lipgloss.NewStyle().Foreground(m.th.Success).Render(fmt.Sprintf("+%d", f.Additions))
		lines = append(lines, fmt.Sprintf("  %s %s %s %s", letter, name, bubble, counts))
	}
	return strings.Join(lines, "\n")
}

// fileHasComments reports whether path carries any inline review comments — used
// to flag files in the list with a 💬 so the reader knows to look.
func (m detailModel) fileHasComments(path string) bool {
	for _, c := range m.detail.ReviewComments {
		if c.Path == path {
			return true
		}
	}
	return false
}

func (m detailModel) renderInfo() string {
	var b strings.Builder
	h := func(s string) string { return infoStyle(m.th).Bold(true).Render(s) }

	// When the diff cursor sits on a commented line, lead with that line's
	// thread — the contextual half of GitHub's "Files changed" view.
	if lc := m.lineComments(); len(lc) > 0 {
		meta := m.lineMeta[m.diffCursor]
		b.WriteString(h(fmt.Sprintf("💬 Comments on %s:%d", shortRepo(m.files[m.selected].Filename), meta.line)) + "\n")
		// The first comment is the thread anchor; later ones are replies, threaded
		// under it with the same ╰→ guide as the full conversation view.
		for i, c := range lc {
			if i == 0 {
				b.WriteString(infoStyle(m.th).Render("@"+c.Author) + " " + mutedStyle(m.th).Render("· "+relTime(c.CreatedAt)) + "\n")
				b.WriteString(renderMarkdown(c.Body, m.infoVP.Width, m.th) + "\n")
				continue
			}
			b.WriteString("\n" + m.renderThreadReply(c.Author, c.Body, c.CreatedAt, m.infoVP.Width) + "\n")
		}
		b.WriteString("\n")
		b.WriteString(mutedStyle(m.th).Render("r reply · c new comment · v full conversation") + "\n")
		// A rule divides the contextual line thread from the PR-level info below.
		b.WriteString(mutedStyle(m.th).Render(strings.Repeat("─", max(1, m.infoVP.Width))) + "\n\n")
	}

	if strings.TrimSpace(m.detail.Body) != "" {
		b.WriteString(h("Description") + "\n")
		b.WriteString(renderMarkdown(m.detail.Body, m.infoVP.Width, m.th) + "\n\n")
	}

	b.WriteString(h(fmt.Sprintf("Checks (%d)", len(m.detail.Checks))) + "\n")
	if len(m.detail.Checks) == 0 {
		b.WriteString(mutedStyle(m.th).Render("  none") + "\n")
	}
	for _, c := range m.detail.Checks {
		b.WriteString(checkGlyph(m.th, c.Conclusion) + " " + c.Name + "\n")
	}
	b.WriteString("\n")

	if len(m.detail.ReviewRequests) > 0 {
		b.WriteString(h(fmt.Sprintf("Review requested (%d)", len(m.detail.ReviewRequests))) + "\n")
		for _, rr := range m.detail.ReviewRequests {
			// Same ◆ you / ◇ others vocabulary as the dashboard.
			glyph := mutedStyle(m.th).Render("◇")
			name := "@" + rr.Name
			if rr.IsTeam {
				name = "team " + rr.Name
			}
			if rr.IsYou {
				glyph = lipgloss.NewStyle().Foreground(m.th.Focus).Bold(true).Render("◆")
				name += mutedStyle(m.th).Render(" (you)")
			}
			b.WriteString(glyph + " " + name + "\n")
		}
		b.WriteString("\n")
	}

	// A bodyless COMMENTED review is just the wrapper GitHub creates when someone
	// replies to an inline thread — its substance shows in the diff and conversation
	// views, not in this summary. Mirror Timeline()'s filter so the list shows only
	// reviews that carry their own verdict or prose.
	var reviews []gh.Review
	for _, r := range m.detail.Reviews {
		if r.State == "COMMENTED" && strings.TrimSpace(r.Body) == "" {
			continue
		}
		reviews = append(reviews, r)
	}
	b.WriteString(h(fmt.Sprintf("Reviews (%d)", len(reviews))) + "\n")
	if len(reviews) == 0 {
		b.WriteString(mutedStyle(m.th).Render("  none") + "\n")
	}
	for _, r := range reviews {
		b.WriteString(reviewBadge(m.th, r.State) + " " + mutedStyle(m.th).Render("by") + " " +
			infoStyle(m.th).Render("@"+r.Author) + " " + mutedStyle(m.th).Render("· "+relTime(r.CreatedAt)) + "\n")
		if strings.TrimSpace(r.Body) != "" {
			b.WriteString(renderMarkdown(r.Body, m.infoVP.Width, m.th) + "\n")
		}
	}
	b.WriteString("\n")

	b.WriteString(h(fmt.Sprintf("Comments (%d)", len(m.detail.Comments))) + "\n")
	for _, c := range m.detail.Comments {
		b.WriteString(mutedStyle(m.th).Render("• ") + infoStyle(m.th).Render("@"+c.Author) + " " + mutedStyle(m.th).Render("· "+relTime(c.CreatedAt)) + "\n")
		b.WriteString(renderMarkdown(c.Body, m.infoVP.Width, m.th) + "\n\n")
	}
	return b.String()
}

func (m detailModel) viewComposer() string {
	title := "Comment"
	switch m.state {
	case stateReject:
		title = "Request changes — reason"
	case stateLineComment:
		title = fmt.Sprintf("Comment on %s:%d", shortRepo(m.anchorPath), m.anchorLine)
	case stateReply:
		title = "Reply to " + m.replyAuthor
	}
	head := warnStyle(m.th).Render(title) + mutedStyle(m.th).Render("   ctrl+s submit · esc cancel")
	return lipgloss.JoinVertical(lipgloss.Left, head, m.composer.View())
}

func (m detailModel) viewFooter() string {
	if m.status != "" {
		return surfaceBar(m.th, m.width,
			lipgloss.NewStyle().Width(m.width).Padding(0, 1).Render(m.status))
	}
	var help string
	switch {
	case m.page == pageConversation:
		// r replies when a thread is selected, else it refreshes.
		rk := " · r refresh"
		if len(m.convThreads) > 0 {
			rk = " · n/N thread · r reply"
		}
		help = "↑/↓ scroll" + rk + " · c comment · a approve · x request-changes · o open · v/d/esc close"
	case m.focus == focusDiff:
		// On the diff, c/s act on the cursor line; r replies when it has a thread,
		// otherwise r refreshes.
		rk := " · r refresh"
		if len(m.lineComments()) > 0 {
			rk = " · r reply"
		}
		help = "↑/↓ line · n/N change · c comment line · s suggest" + rk +
			" · i panel · ←/→ focus · v conversation · a approve · o open · esc back"
	default:
		help = "←/→ focus · ↑/↓ move · [ ] file · n/N change · i panel · v conversation · c comment · a approve · x changes · o open · r refresh · esc back"
	}
	return surfaceBar(m.th, m.width,
		lipgloss.NewStyle().Width(m.width).Foreground(m.th.Muted).Padding(0, 1).
			Render(truncate(help, max(10, m.width-2))))
}

// ---- small styling helpers ----

func mutedStyle(t theme.Theme) lipgloss.Style { return lipgloss.NewStyle().Foreground(t.Muted) }
func infoStyle(t theme.Theme) lipgloss.Style  { return lipgloss.NewStyle().Foreground(t.Info) }
func okStyle(t theme.Theme) lipgloss.Style    { return lipgloss.NewStyle().Foreground(t.Success) }
func errStyle(t theme.Theme) lipgloss.Style   { return lipgloss.NewStyle().Foreground(t.Danger) }
func warnStyle(t theme.Theme) lipgloss.Style  { return lipgloss.NewStyle().Foreground(t.Warning) }

func stateBadge(t theme.Theme, state string) string {
	c := t.Success
	switch state {
	case "CLOSED":
		c = t.Danger
	case "MERGED":
		c = t.Primary
	}
	return lipgloss.NewStyle().Foreground(c).Bold(true).Render(state)
}

// draftBadge marks a not-yet-ready PR. Drafts stay listed under OPEN, so the
// badge is what distinguishes them — muted to read as "parked, not active".
func draftBadge(t theme.Theme) string {
	return lipgloss.NewStyle().Foreground(t.Warning).Bold(true).
		Render("✎ DRAFT")
}

func reviewBadge(t theme.Theme, state string) string {
	switch state {
	case "APPROVED":
		return okStyle(t).Render("✓ approved")
	case "CHANGES_REQUESTED":
		return errStyle(t).Render("✗ requested changes")
	case "COMMENTED":
		return mutedStyle(t).Render("• commented")
	default:
		return mutedStyle(t).Render("• " + strings.ToLower(state))
	}
}

func checkGlyph(t theme.Theme, conclusion string) string {
	switch conclusion {
	case "SUCCESS":
		return okStyle(t).Render("●")
	case "FAILURE", "ERROR", "TIMED_OUT", "ACTION_REQUIRED":
		return errStyle(t).Render("●")
	case "", "PENDING", "QUEUED", "IN_PROGRESS", "EXPECTED":
		return warnStyle(t).Render("●")
	default:
		return mutedStyle(t).Render("○")
	}
}

func statusLetter(t theme.Theme, status string) string {
	return lipgloss.NewStyle().Foreground(statusColor(t, status)).Render(statusLetterPlain(status))
}

func statusLetterPlain(status string) string {
	switch status {
	case "added":
		return "A"
	case "removed":
		return "D"
	case "renamed":
		return "R"
	case "modified", "changed":
		return "M"
	default:
		return "·"
	}
}

func statusColor(t theme.Theme, status string) lipgloss.Color {
	switch status {
	case "added":
		return t.Success
	case "removed":
		return t.Danger
	case "renamed":
		return t.Info
	default:
		return t.Warning
	}
}

func shortPath(p string, max int) string {
	if max < 4 {
		max = 4
	}
	if len(p) <= max {
		return p
	}
	return "…" + p[len(p)-max+1:]
}

func wrap(s string, w int) string {
	if w < 8 {
		w = 8
	}
	return lipgloss.NewStyle().Width(w).Render(s)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
