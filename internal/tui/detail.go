package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/atotto/clipboard"
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
	stateEditComment // editing your own comment/review (bottom composer)
	stateEditDesc    // editing the PR description (full-screen editor + preview)
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

	page      detailPage
	selected  int // index into files
	focus     focusPane
	showInfo  bool
	showFiles bool // the left files pane (toggle with f to give the diff full width)

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

	// Conversation-page cursor: every navigable block (description, top-level
	// comments, reviews, inline threads) in render order, and the selected one
	// (-1 = none). Lets you n/N to any block to reply to it or edit your own.
	convAnchors []convAnchor
	convCursor  int

	// editAnchor is the block a pending edit targets (set when e opens the editor),
	// so ctrl+s knows which update mutation to fire.
	editAnchor convAnchor

	// statusSeq tags each transient status so a delayed auto-dismiss only clears the
	// message it was scheduled for, never a newer one.
	statusSeq int
}

// convAnchor is one navigable block in the rendered conversation: its starting
// visual row (for scrolling), who wrote it, and the ids needed to act on it.
// replyID > 0 marks a repliable inline thread; mine marks a block you can edit;
// the edit target is editID (REST comment id) or reviewID (a review's node id),
// or neither for the PR description (edited by PR number).
type convAnchor struct {
	row      int
	author   string
	kind     gh.TimelineKind
	mine     bool
	replyID  int    // review-comment REST id, for reply (inline threads only)
	editID   int    // REST id for editing an issue/inline comment
	reviewID string // GraphQL node id for editing a review body
	body     string // current body text, to seed the editor
}

// repliable reports whether r acts on this block: an inline thread threads a
// reply, while a top-level comment or review adds a new conversation comment
// (GitHub has no threaded reply for those).
func (a convAnchor) repliable() bool {
	return a.replyID > 0 || a.kind == gh.KindComment || a.kind == gh.KindReview
}

func newDetail(th theme.Theme, it gh.Item) detailModel {
	owner, repo, _ := gh.SplitRepo(it.Repo)
	ta := textarea.New()
	ta.Placeholder = "Write a comment (GitHub-flavored Markdown)…"
	ta.ShowLineNumbers = false
	// Theme the composer from the palette (not bubbles' terminal-adaptive default,
	// which renders a black cursor-line band in a light theme on a dark terminal).
	// Must be set on the persistent model — see styleComposer's note on the cached
	// &FocusedStyle pointer.
	styleComposer(&ta, th)
	return detailModel{
		th:        th,
		owner:     owner,
		repo:      repo,
		number:    it.Number,
		url:       it.URL,
		title:     it.Title,
		loading:   true,
		showInfo:  true,
		showFiles: true,
		composer:  ta,
		diffVP:    newVP(),
		infoVP:    newVP(),
		convVP:    newVP(),
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

// statusClearMsg auto-dismisses a transient status line a few seconds after it
// was shown; seq guards against clearing a message that has since been replaced.
type statusClearMsg struct{ seq int }

// statusFlashFor is how long a transient status (a confirmation or a warning)
// lingers before it auto-dismisses without a keypress.
const statusFlashFor = 4 * time.Second

// flashStatus sets a transient status and returns a command that auto-clears it
// after statusFlashFor. Incrementing statusSeq also invalidates any earlier
// pending auto-clear, so overlapping messages don't cut each other short.
func (m *detailModel) flashStatus(s string) tea.Cmd {
	m.status = s
	m.statusSeq++
	seq := m.statusSeq
	return tea.Tick(statusFlashFor, func(time.Time) tea.Msg { return statusClearMsg{seq: seq} })
}

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

// doUpdatePRBody saves an edited PR description off the main loop.
func doUpdatePRBody(owner, repo string, number int, body string) tea.Cmd {
	return func() tea.Msg {
		return actionDoneMsg{verb: "description edit", err: gh.UpdatePRBody(owner, repo, number, body)}
	}
}

// doEditComment saves an edited comment/review, dispatching to the right API by
// the anchor's kind (issue comment, inline review comment, or review body).
func doEditComment(owner, repo string, a convAnchor, body string) tea.Cmd {
	return func() tea.Msg {
		var err error
		switch a.kind {
		case gh.KindComment:
			err = gh.UpdateIssueComment(owner, repo, a.editID, body)
		case gh.KindInline:
			err = gh.UpdateReviewComment(owner, repo, a.editID, body)
		case gh.KindReview:
			err = gh.UpdateReview(a.reviewID, body)
		}
		return actionDoneMsg{verb: "edit", err: err}
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
			return m, m.flashStatus(errStyle(m.th).Render("✗ " + msg.verb + " failed: " + msg.err.Error()))
		}
		clear := m.flashStatus(okStyle(m.th).Render("✓ " + msg.verb + " submitted"))
		// Remember the user changed their involvement so the dashboard syncs every
		// tab on exit (e.g. an Orgs PR you just commented on moves into Involved).
		m.posted = true
		// Reload conversation/checks so the new review/comment shows — but stay
		// on the current file, line, and scroll position.
		return m, tea.Batch(loadPR(m.owner, m.repo, m.number, true), clear)

	case statusClearMsg:
		if msg.seq == m.statusSeq {
			m.status = ""
		}
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

// composing reports whether a text composer is open and should receive raw
// keystrokes (so global shortcuts like ? must not steal them).
func (m detailModel) composing() bool {
	return m.state == stateComment || m.state == stateReject ||
		m.state == stateLineComment || m.state == stateReply ||
		m.state == stateEditComment
}

// editingDesc reports whether the full-screen description editor is open (its own
// layout, so it's kept out of composing()'s bottom-composer path).
func (m detailModel) editingDesc() bool { return m.state == stateEditDesc }

// ownPR reports whether the viewer authored this PR. GitHub forbids approving or
// requesting changes on your own PR, so those actions are gated (and not
// advertised) when this is true.
func (m detailModel) ownPR() bool { return isMine(m.detail.Author) }

// reviewKeys returns the footer fragment for the approve / request-changes review
// actions — empty on your own PR, where GitHub disallows them.
func (m detailModel) reviewKeys() string {
	if m.ownPR() {
		return ""
	}
	return " · a approve · x request-changes"
}

// capturingText reports whether raw keystrokes should reach a text field (either
// the bottom composer or the full-screen description editor), so global shortcuts
// like ? don't steal them.
func (m detailModel) capturingText() bool { return m.composing() || m.editingDesc() }

func (m detailModel) handleKey(msg tea.KeyMsg) (detailModel, tea.Cmd) {
	// The full-screen description editor owns the whole screen and its own keys.
	if m.editingDesc() {
		switch msg.String() {
		case "esc":
			m.state = stateBrowsing
			m.composer.Reset()
			m.composer.Blur()
			m.relayout() // restore the bottom composer's normal size
			return m, nil
		case "ctrl+s":
			body := strings.TrimSpace(m.composer.Value())
			m.composer.Reset()
			m.composer.Blur()
			m.relayout()
			m.state = stateSubmitting
			m.status = mutedStyle(m.th).Render("saving description…")
			return m, doUpdatePRBody(m.owner, m.repo, m.number, body)
		default:
			var cmd tea.Cmd
			m.composer, cmd = m.composer.Update(msg)
			return m, cmd
		}
	}

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
			edit := m.editAnchor
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
			case stateEditComment:
				return m, doEditComment(m.owner, m.repo, edit, body)
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
	case "s":
		// Toggle the files sidebar to give the diff the full width (handy on narrow
		// screens) — same key as the dashboard's sidebar toggle. Move focus off it
		// when hiding so you can't tab into nothing.
		m.showFiles = !m.showFiles
		if !m.showFiles && m.focus == focusFiles {
			m.focus = focusDiff
		}
		m.relayout()
		m.refreshDiff()
		return m, nil
	case "o":
		return m, openBrowser(m.url)
	case "y":
		// Yank the GitHub link for the comment under the cursor (a diff-line inline
		// comment, or the selected conversation thread) to the clipboard — a one-key
		// handoff to paste back into chat. Falls back to the PR link.
		return m.copyLink()
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
	case "S":
		// Suggest a change on the cursor line: a comment pre-filled with a
		// GitHub ```suggestion block seeded from the line's current content.
		// (Moved off s, which now toggles the files sidebar — consistent with the
		// dashboard's s.)
		if m.page == pageDiff && m.focus == focusDiff {
			return m.startLineComment("suggest")
		}
	case "r":
		// Reply to the block in focus: an inline thread threads a reply, a top-level
		// comment/review adds a new conversation comment. Off a repliable block (or on
		// the diff without a thread) r refreshes — matching the dashboard.
		if a, ok := m.selectedAnchor(); ok && m.page == pageConversation && a.repliable() {
			if a.replyID > 0 {
				return m.startConvReply()
			}
			return m.startConvComment(a.author)
		}
		if m.page == pageDiff && m.focus == focusDiff && len(m.lineComments()) > 0 {
			return m.startReply()
		}
		m.loading, m.refreshing = true, true
		return m, loadPR(m.owner, m.repo, m.number, true)
	case "e":
		// Edit the block under the conversation cursor — your own only. The
		// description opens a full-screen editor + preview; comments/reviews reuse the
		// bottom composer.
		if m.page == pageConversation {
			return m.startEdit()
		}
	case "x":
		// GitHub forbids reviewing your own PR, so don't even open the composer —
		// warn instead of letting the submit fail after you have typed a reason.
		if m.ownPR() {
			return m, m.flashStatus(warnStyle(m.th).Render("you can't request changes on your own PR"))
		}
		m.state = stateReject
		m.composer.Reset()
		m.composer.Placeholder = "Reason for requesting changes…"
		m.composer.Focus()
		return m, textarea.Blink
	case "a":
		if m.ownPR() {
			return m, m.flashStatus(warnStyle(m.th).Render("you can't approve your own PR"))
		}
		m.state = stateConfirmApprove
		// A live confirm prompt (not auto-cleared); bump the seq so any pending
		// auto-dismiss can't wipe it out from under the confirmation.
		m.statusSeq++
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
		// Jumping between changes implies you're reading the diff — focus it so the
		// very next ↑/↓ walks diff lines instead of switching files.
		m.focus = focusDiff
		m.gotoHunk(1)
		return m, nil
	case "N":
		if m.page == pageConversation {
			m.moveConvThread(-1)
			return m, nil
		}
		m.focus = focusDiff
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
		case "ctrl+d", "pgdown":
			m.jumpCursor(diffPageStep)
			return m, nil
		case "ctrl+u", "pgup":
			m.jumpCursor(-diffPageStep)
			return m, nil
		case "g", "home":
			m.jumpToEdge(false)
			return m, nil
		case "G", "end":
			m.jumpToEdge(true)
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
	order := []focusPane{focusDiff}
	if m.showFiles {
		order = []focusPane{focusFiles, focusDiff}
	}
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

// commentPermalink builds the GitHub permalink for an inline review comment from
// the PR url and the comment's REST id: <pr-url>#discussion_r<id>, the anchor
// GitHub uses to deep-link a review-thread comment.
func commentPermalink(prURL string, commentID int) string {
	return prURL + "#discussion_r" + strconv.Itoa(commentID)
}

// linkForSelection returns the most specific GitHub link for what's under the
// cursor, plus a label for the status line: the selected conversation thread or
// the diff cursor's inline comment when there is one, otherwise the PR itself.
func (m detailModel) linkForSelection() (url, kind string) {
	if m.url == "" {
		return "", ""
	}
	// Conversation page: the selected (n/N) inline thread.
	if a, ok := m.selectedAnchor(); ok && m.page == pageConversation && a.replyID > 0 {
		return commentPermalink(m.url, a.replyID), "comment"
	}
	// Diff page: an inline comment anchored to the cursor's line (the thread anchor).
	if m.page == pageDiff {
		if cs := m.lineComments(); len(cs) > 0 && cs[0].DatabaseID > 0 {
			return commentPermalink(m.url, cs[0].DatabaseID), "comment"
		}
	}
	return m.url, "PR" // nothing comment-specific selected — yank the PR link
}

// copyLink writes the selected comment's (or PR's) GitHub link to the system
// clipboard and reports the outcome on the status line.
func (m detailModel) copyLink() (detailModel, tea.Cmd) {
	url, kind := m.linkForSelection()
	if url == "" {
		m.status = warnStyle(m.th).Render("nothing to copy yet")
		return m, nil
	}
	if err := clipboard.WriteAll(url); err != nil {
		m.status = errStyle(m.th).Render("copy failed: " + err.Error())
		return m, nil
	}
	m.status = okStyle(m.th).Render("✓ copied " + kind + " link  " + url)
	return m, nil
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

// diffPageStep is how many patch lines ctrl+d/ctrl+u (and pgdn/pgup) jump — a
// chunky step for long, hunk-less diffs (e.g. a whole new file) where line-by-line
// scrolling is tedious.
const diffPageStep = 10

// jumpCursor moves the diff cursor by delta lines and pages the viewport by the
// same visual distance, so content actually scrolls on every press (not only once
// the cursor runs off the edge). ensureCursorVisible then corrects the offset near
// the ends where the paged distance clamps short.
func (m *detailModel) jumpCursor(delta int) {
	if m.diffNLines == 0 {
		return
	}
	old := m.diffCursor
	m.diffCursor = clamp(m.diffCursor+delta, 0, m.diffNLines-1)
	m.diffVP.SetYOffset(m.diffVP.YOffset + m.visualRow(m.diffCursor) - m.visualRow(old))
	m.renderDiffContent()
	m.ensureCursorVisible()
	m.refreshInfo()
}

// jumpToEdge parks the diff cursor on the first (toEnd=false) or last line of the
// file and snaps the viewport to that edge — for long diffs where scrolling all the
// way is painful.
func (m *detailModel) jumpToEdge(toEnd bool) {
	if m.diffNLines == 0 {
		return
	}
	if toEnd {
		m.diffCursor = m.diffNLines - 1
		m.renderDiffContent()
		m.diffVP.GotoBottom()
	} else {
		m.diffCursor = 0
		m.renderDiffContent()
		m.diffVP.GotoTop()
	}
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

// startEdit opens an editor for the block under the conversation cursor — your
// own only. The PR description gets the full-screen editor + live preview; a
// comment or review body reuses the bottom composer, seeded with its current text.
func (m detailModel) startEdit() (detailModel, tea.Cmd) {
	a, ok := m.selectedAnchor()
	if !ok {
		return m, m.flashStatus(mutedStyle(m.th).Render("nothing selected — press n/N to pick a block"))
	}
	if !a.mine {
		return m, m.flashStatus(warnStyle(m.th).Render("you can only edit your own " + editKindLabel(a.kind)))
	}
	m.editAnchor = a
	if a.kind == gh.KindDescription {
		m.state = stateEditDesc
		m.composer.SetValue(a.body)
		m.composer.Focus()
		m.sizeDescEditor()
		return m, textarea.Blink
	}
	m.state = stateEditComment
	m.composer.SetValue(a.body)
	m.composer.Placeholder = "Edit your " + editKindLabel(a.kind) + " (GitHub-flavored Markdown)…"
	m.composer.Focus()
	return m, textarea.Blink
}

// editKindLabel names a block kind for edit prompts and messages.
func editKindLabel(k gh.TimelineKind) string {
	switch k {
	case gh.KindDescription:
		return "description"
	case gh.KindReview:
		return "review"
	default:
		return "comment"
	}
}

// selectedAnchor returns the conversation block under the cursor, if any.
func (m detailModel) selectedAnchor() (convAnchor, bool) {
	if m.convCursor < 0 || m.convCursor >= len(m.convAnchors) {
		return convAnchor{}, false
	}
	return m.convAnchors[m.convCursor], true
}

// startConvComment opens the composer to add a new top-level conversation comment,
// framed as a reply to the selected block's author (GitHub has no threaded reply
// for top-level comments, so this is just a new comment).
func (m detailModel) startConvComment(author string) (detailModel, tea.Cmd) {
	m.state = stateComment
	m.composer.Reset()
	m.composer.Placeholder = "Reply to " + author + " in the conversation (GitHub-flavored Markdown)…"
	m.composer.Focus()
	return m, textarea.Blink
}

// startConvReply opens the composer to reply to the inline thread under the
// cursor (full-conversation view). Only inline threads are repliable.
func (m detailModel) startConvReply() (detailModel, tea.Cmd) {
	t, ok := m.selectedAnchor()
	if !ok || t.replyID == 0 {
		return m, m.flashStatus(mutedStyle(m.th).Render("that block isn't a repliable thread — pick an inline comment with n/N"))
	}
	m.replyTo = t.replyID
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

	// descEditorChromeH is the non-textarea rows in the description editor's left
	// pane: headline, rule, blank, and the field label.
	descEditorChromeH = 4
)

// bottomReserve is how many lines the bottom strip needs: a one-line footer
// while browsing, or the taller composer (textarea + its title) while writing.
func (m detailModel) bottomReserve() int {
	switch m.state {
	case stateComment, stateReject, stateLineComment, stateReply, stateEditComment:
		return composerH + 1
	}
	return detailFooterH
}

func (m *detailModel) paneWidths() (files, diff, info int) {
	// Lay out within the indented body width so header/footer bars stay flush.
	w := bodyWidth(m.width)
	files = 0
	if m.showFiles {
		files = 36
		if files > w/3 {
			files = w / 3
		}
		if files < 16 {
			files = 16
		}
	}
	info = 0
	if m.showInfo && w >= 100 {
		info = 52
		if info > w*2/5 {
			info = w * 2 / 5
		}
	}
	// One separator between each visible pane (diff is always shown).
	panes := 1
	if files > 0 {
		panes++
	}
	if info > 0 {
		panes++
	}
	diff = w - files - info - (panes - 1)
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
	if m.state == stateEditDesc {
		m.sizeDescEditor() // the full-screen editor overrides the bottom-composer size
	}
}

// sizeDescEditor sizes the shared composer to fill the left editor pane of the
// full-screen description editor (the right pane is the live preview).
func (m *detailModel) sizeDescEditor() {
	editorW, _ := composeWidths(bodyWidth(m.width))
	bodyH := m.height - detailHeaderH - 1 // header chrome + one-line footer hint
	h := bodyH - descEditorChromeH        // editor headline + rule + blank + field label
	if h < 3 {
		h = 3
	}
	m.composer.SetWidth(editorW)
	m.composer.SetHeight(h)
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
	// Re-theme the composer and re-point its cached active-style pointer (via
	// Focus/Blur) so a live toggle recolors the textarea too.
	styleComposer(&m.composer, th)
	if m.capturingText() {
		m.composer.Focus()
	} else {
		m.composer.Blur()
	}
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

// refreshConv re-renders the conversation, places the cursor on the first block
// (the description) if it isn't already on one, and scrolls to the top.
func (m *detailModel) refreshConv() {
	_, anchors := m.renderConversation()
	switch {
	case len(anchors) == 0:
		m.convCursor = -1
	case m.convCursor < 0 || m.convCursor >= len(anchors):
		m.convCursor = 0
	}
	m.renderConvContent()
	m.convVP.GotoTop()
}

// renderConvContent rebuilds the conversation viewport content + anchor index for
// the current cursor, leaving scroll position untouched.
func (m *detailModel) renderConvContent() {
	content, anchors := m.renderConversation()
	m.convAnchors = anchors
	m.convVP.SetContent(content)
}

// moveConvThread steps the conversation cursor (dir +1/-1) across navigable blocks,
// re-renders to move the highlight, and scrolls the selected block into view.
func (m *detailModel) moveConvThread(dir int) {
	n := len(m.convAnchors)
	if n == 0 {
		m.status = mutedStyle(m.th).Render("nothing to navigate")
		return
	}
	if m.convCursor < 0 {
		m.convCursor = 0
	} else {
		m.convCursor = (m.convCursor + dir + n) % n
	}
	m.renderConvContent()
	// Only scroll when the selected block's header is outside the viewport —
	// staying put when it's already visible (no jolt on a short hop).
	row := m.convAnchors[m.convCursor].row
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

	// The description editor takes the whole body: editor left, live preview right.
	if m.editingDesc() {
		bodyH := m.height - detailHeaderH - 1
		if bodyH < 3 {
			bodyH = 3
		}
		hint := surfaceBar(m.th, m.width, lipgloss.NewStyle().Foreground(m.th.Muted).Padding(0, 1).
			Render("ctrl+s save · esc cancel · GitHub-flavored Markdown"))
		return lipgloss.JoinVertical(lipgloss.Left,
			m.viewHeader(), indentBody(m.viewDescEditor(bodyH)), hint)
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
func (m detailModel) renderConversation() (string, []convAnchor) {
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
	var anchors []convAnchor
	row := 0
	write := func(s string) { b.WriteString(s); row += strings.Count(s, "\n") }
	// add records a navigable block at the current row and reports whether it is the
	// selected one (so its header gets the cursor bar). The anchor's index is its
	// position in the list, which is what n/N cycles through.
	add := func(a convAnchor) bool {
		a.row = row
		a.mine = isMine(a.author)
		sel := len(anchors) == m.convCursor
		anchors = append(anchors, a)
		return sel
	}
	// inlineAnchor is the anchor for a repliable inline comment (standalone or a
	// review child): repliable and editable via its REST id.
	inlineAnchor := func(e gh.TimelineEntry) convAnchor {
		return convAnchor{author: e.Author, kind: e.Kind, replyID: e.DatabaseID, editID: e.DatabaseID, body: e.Body}
	}
	// writeThread renders an inline thread — its anchor comment then each threaded
	// reply — registering every one as its own navigable anchor (so you can hunk
	// onto and edit your own reply, not just the anchor). prefix indents the whole
	// thread when it sits under a review.
	writeThread := func(e gh.TimelineEntry, width int, prefix string) {
		sel := add(inlineAnchor(e))
		block := m.renderInlineComment(e, width, sel)
		if prefix != "" {
			block = indentBlock(block, prefix)
		}
		write(block + "\n")
		for _, r := range e.Replies {
			selR := add(convAnchor{author: r.Author, kind: gh.KindInline, replyID: r.DatabaseID, editID: r.DatabaseID, body: r.Body})
			rblock := m.renderThreadReply(r.Author, r.Body, r.CreatedAt, width, selR)
			if prefix != "" {
				rblock = indentBlock(rblock, prefix)
			}
			write("\n" + rblock)
		}
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
		// header + cited code + body, plus any threaded replies.
		if e.Kind == gh.KindInline {
			writeThread(e, tw, "")
			continue
		}

		// Description / top-level comment / review summary — each a navigable block.
		var a convAnchor
		switch e.Kind {
		case gh.KindComment:
			a = convAnchor{author: e.Author, kind: e.Kind, editID: e.DatabaseID, body: e.Body}
		case gh.KindReview:
			a = convAnchor{author: e.Author, kind: e.Kind, reviewID: e.ReviewID, body: e.Body}
		default: // KindDescription
			a = convAnchor{author: e.Author, kind: e.Kind, body: e.Body}
		}
		sel := add(a)
		write(m.conversationHeader(e, sel, tw) + "\n")
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
				writeThread(ch, childW, prefix)
			}
		}
	}
	if len(entries) <= 1 && strings.TrimSpace(m.detail.Body) == "" {
		return mutedStyle(m.th).Render("No conversation yet. Press c to comment."), nil
	}
	return b.String(), anchors
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
		// Conversation thread cursor: a full-width primary-on-focusBg bar makes the
		// selected thread unmistakable (a lone ▌ read too faint). FocusBg defaults to
		// Surface but a theme can set it lighter so the bar reads on a low-contrast base.
		plain := fmt.Sprintf("%s @%s on %s:%d · %s", focusGlyph, e.Author, shortRepo(e.Path), e.Line, relTime(e.CreatedAt))
		header = lipgloss.NewStyle().Foreground(m.th.Primary).Background(m.th.FocusBg).
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
	// Threaded replies are rendered (and anchored) by the caller so each is its own
	// navigable block; the anchor here is just its head comment.
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
func (m detailModel) renderThreadReply(author, body string, when time.Time, w int, selected bool) string {
	marker := mutedStyle(m.th).Render("    ╰→ ")
	guide := mutedStyle(m.th).Render("    │")
	cont := "       "
	replyW := w - 7
	if replyW < 8 {
		replyW = 8
	}
	headerLine := indentBlock(infoStyle(m.th).Bold(true).Render("@"+author)+mutedStyle(m.th).Render(" · "+relTime(when)), marker)
	if selected {
		// Same primary-on-focusBg cursor bar as an anchor, so a reply reads as
		// selectable too — with the ╰→ preserved so it still nests visually.
		plain := fmt.Sprintf("    ╰→ %s @%s · %s", focusGlyph, author, relTime(when))
		headerLine = lipgloss.NewStyle().Foreground(m.th.Primary).Background(m.th.FocusBg).
			Bold(true).Width(w).Render(plain)
	}
	rbody := strings.TrimSpace(body)
	if rbody == "" {
		rbody = mutedStyle(m.th).Render("(no message)")
	} else {
		rbody = renderMarkdown(rbody, replyW, m.th)
	}
	return guide + "\n" + headerLine + "\n" + indentBlock(rbody, cont)
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

func (m detailModel) conversationHeader(e gh.TimelineEntry, selected bool, w int) string {
	// The selected block wears a full-width primary-on-focusBg bar (same cursor as
	// an inline thread), with a leading glyph, so n/N selection is unmistakable.
	if selected {
		var what string
		switch e.Kind {
		case gh.KindDescription:
			what = "opened this PR"
		case gh.KindReview:
			what = reviewStateWord(e.State)
		default:
			what = "commented"
		}
		plain := fmt.Sprintf("%s @%s %s · %s", focusGlyph, e.Author, what, relTime(e.CreatedAt))
		return lipgloss.NewStyle().Foreground(m.th.Primary).Background(m.th.FocusBg).
			Bold(true).Width(w).Render(plain)
	}
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

// reviewStateWord is a short plain-text phrase for a review state, for the
// selection bar (which can't carry the colored reviewBadge).
func reviewStateWord(state string) string {
	switch state {
	case "APPROVED":
		return "approved"
	case "CHANGES_REQUESTED":
		return "requested changes"
	case "DISMISSED":
		return "review dismissed"
	default:
		return "reviewed"
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

	diffTitle := "Diff"
	if len(m.files) > 0 {
		diffTitle = "Diff · " + shortPath(m.files[m.selected].Filename, m.diffVP.Width-8)
		if len(m.hunks) > 1 {
			diffTitle += fmt.Sprintf("  hunk %d/%d", m.curHunk+1, len(m.hunks))
		}
	}
	diffPane := m.paneBox(diffTitle, m.diffVP.View(), m.diffVP.Width, bodyH, m.focus == focusDiff)

	var panes []string
	if filesW > 0 {
		panes = append(panes, m.paneBox("Files", m.renderFileList(filesW, bodyH-2), filesW, bodyH, m.focus == focusFiles))
	}
	panes = append(panes, diffPane)
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
			b.WriteString("\n" + m.renderThreadReply(c.Author, c.Body, c.CreatedAt, m.infoVP.Width, false) + "\n")
		}
		b.WriteString("\n")
		b.WriteString(mutedStyle(m.th).Render("r reply · y copy link · c new comment · v full conversation") + "\n")
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
	case stateEditComment:
		title = "Edit your " + editKindLabel(m.editAnchor.kind)
	}
	head := warnStyle(m.th).Render(title) + mutedStyle(m.th).Render("   ctrl+s submit · esc cancel")
	return lipgloss.JoinVertical(lipgloss.Left, head, m.composer.View())
}

// viewDescEditor draws the full-screen description editor: the Markdown editor on
// the left, a live rendered preview on the right — mirroring the propose-PR
// composer, minus the title field (a PR's title is edited elsewhere).
func (m detailModel) viewDescEditor(h int) string {
	w := bodyWidth(m.width)
	editorW, previewW := composeWidths(w)

	headline := lipgloss.NewStyle().Foreground(m.th.Primary).Bold(true).
		Render(fmt.Sprintf("edit description · #%d", m.number))
	rule := lipgloss.NewStyle().Foreground(m.th.Focus).Render(strings.Repeat("─", editorW))
	editor := lipgloss.JoinVertical(lipgloss.Left,
		headline, rule, "",
		fieldLabel(m.th, "Description — Markdown", true),
		m.composer.View(),
	)
	editorPane := lipgloss.NewStyle().Width(editorW).Height(h).Render(editor)

	pvHead := lipgloss.NewStyle().Foreground(m.th.Muted).Render("Preview")
	pvRule := lipgloss.NewStyle().Foreground(m.th.Overlay).Render(strings.Repeat("─", previewW))
	var pvBody string
	if strings.TrimSpace(m.composer.Value()) == "" {
		pvBody = mutedStyle(m.th).Render("(nothing to preview yet — write a\ndescription on the left)")
	} else {
		pvBody = renderMarkdown(m.composer.Value(), previewW-1, m.th)
	}
	preview := lipgloss.JoinVertical(lipgloss.Left, pvHead, pvRule, "", pvBody)
	previewPane := lipgloss.NewStyle().Width(previewW).Height(h).Render(preview)

	return lipgloss.JoinHorizontal(lipgloss.Top, editorPane, stackVBar(m.th, h), previewPane)
}

func (m detailModel) viewFooter() string {
	if m.status != "" {
		return surfaceBar(m.th, m.width,
			lipgloss.NewStyle().Width(m.width).Padding(0, 1).Render(m.status))
	}
	var help string
	switch {
	case m.page == pageConversation:
		// n/N walks every block; the block-specific actions (reply on an inline
		// thread, edit on one of your own blocks) show only when they apply.
		nav := " · n/N block"
		if a, ok := m.selectedAnchor(); ok {
			switch {
			case a.replyID > 0:
				nav += " · r reply · y copy link"
			case a.repliable():
				nav += " · r reply"
			}
			if a.mine {
				nav += " · e edit"
			}
		}
		help = "↑/↓ scroll" + nav + " · c comment" + m.reviewKeys() + " · o open · v/d/esc close"
	case m.focus == focusDiff:
		// On the diff, c/s act on the cursor line; r replies when it has a thread,
		// otherwise r refreshes.
		rk := " · r refresh"
		if len(m.lineComments()) > 0 {
			rk = " · r reply · y copy link"
		}
		help = "↑/↓ line · ^d/^u page · g/G top/end · n/N change · c comment line · S suggest" + rk +
			" · s/i files/panel · ←/→ focus · v conversation" + m.reviewKeys() + " · o open · esc back"
	default:
		help = "←/→ focus · ↑/↓ move · [ ] file · n/N change · s/i files/panel · v conversation · c comment" + m.reviewKeys() + " · o open · r refresh · esc back"
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
