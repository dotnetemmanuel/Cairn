package tui

import (
	"fmt"
	"strings"

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
	stateReject // request-changes reason
	stateConfirmApprove
	stateSubmitting
)

// detailModel is the PR review screen.
type detailModel struct {
	th    theme.Theme
	owner string
	repo  string
	number int
	url   string
	title string

	width, height int

	loading bool
	err     error
	detail  gh.PRDetail
	files   []gh.FileDiff

	page     detailPage
	selected int // index into files
	focus    focusPane
	showInfo bool

	diffVP viewport.Model
	infoVP viewport.Model
	convVP viewport.Model
	hunks  []int // hunk start line offsets for the selected file

	state    detailState
	composer textarea.Model
	status   string // transient feedback line
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
}

type actionDoneMsg struct {
	verb string
	err  error
}

type detailExitMsg struct{}

func loadPR(owner, repo string, number int) tea.Cmd {
	return func() tea.Msg {
		detail, derr := gh.FetchPRDetail(owner, repo, number)
		files, ferr := gh.FetchPRFiles(owner, repo, number)
		err := derr
		if err == nil {
			err = ferr
		}
		return prLoadedMsg{detail: detail, files: files, err: err}
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

func (m detailModel) Init() tea.Cmd {
	return loadPR(m.owner, m.repo, m.number)
}

// ---- update ----

func (m detailModel) Update(msg tea.Msg) (detailModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.relayout()
		return m, nil

	case prLoadedMsg:
		m.loading = false
		m.err = msg.err
		m.detail = msg.detail
		m.files = msg.files
		if msg.err == nil {
			m.selected = 0
			m.relayout()
			m.refreshDiff()
			m.refreshInfo()
			m.refreshConv()
		}
		return m, nil

	case actionDoneMsg:
		m.state = stateBrowsing
		if msg.err != nil {
			m.status = errStyle(m.th).Render("✗ " + msg.verb + " failed: " + msg.err.Error())
			return m, nil
		}
		m.status = okStyle(m.th).Render("✓ " + msg.verb + " submitted")
		// Reload conversation/checks so the new review/comment shows.
		return m, loadPR(m.owner, m.repo, m.number)

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m detailModel) handleKey(msg tea.KeyMsg) (detailModel, tea.Cmd) {
	// Composer states capture most keys.
	if m.state == stateComment || m.state == stateReject {
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
			if st == stateComment {
				return m, doComment(m.owner, m.repo, m.number, body)
			}
			return m, doReview(m.owner, m.repo, m.number, "REQUEST_CHANGES", body)
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

	// Browsing.
	switch msg.String() {
	case "esc":
		// From the conversation page, esc returns to the diff; from the diff
		// page it exits back to the dashboard.
		if m.page == pageConversation {
			m.page = pageDiff
			return m, nil
		}
		return m, func() tea.Msg { return detailExitMsg{} }
	case "v":
		m.page = pageConversation
		return m, nil
	case "d":
		m.page = pageDiff
		return m, nil
	case "tab":
		m.focus = m.nextFocus(1)
		return m, nil
	case "shift+tab":
		m.focus = m.nextFocus(-1)
		return m, nil
	case "i":
		m.showInfo = !m.showInfo
		m.relayout()
		m.refreshDiff()
		return m, nil
	case "o":
		return m, openBrowser(m.url)
	case "c":
		m.state = stateComment
		m.composer.Reset()
		m.composer.Placeholder = "Write a comment (GitHub-flavored Markdown)…"
		m.composer.Focus()
		return m, textarea.Blink
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
		m.gotoHunk(1)
		return m, nil
	case "N":
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
		var cmd tea.Cmd
		m.diffVP, cmd = m.diffVP.Update(msg)
		return m, cmd
	case focusInfo:
		var cmd tea.Cmd
		m.infoVP, cmd = m.infoVP.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *detailModel) nextFocus(dir int) focusPane {
	order := []focusPane{focusFiles, focusDiff}
	if m.showInfo {
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
	cur := m.diffVP.YOffset
	target := cur
	if dir > 0 {
		for _, h := range m.hunks {
			if h > cur {
				target = h
				break
			}
		}
	} else {
		for i := len(m.hunks) - 1; i >= 0; i-- {
			if m.hunks[i] < cur {
				target = m.hunks[i]
				break
			}
		}
	}
	m.diffVP.SetYOffset(target)
}

// ---- layout ----

const (
	detailHeaderH = 2
	detailFooterH = 1
)

func (m *detailModel) paneWidths() (files, diff, info int) {
	w := m.width
	files = 30
	if files > w/3 {
		files = w / 3
	}
	if files < 16 {
		files = 16
	}
	info = 0
	if m.showInfo && w >= 100 {
		info = 42
		if info > w/3 {
			info = w / 3
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
	vpH := bodyH - 1 // one line for the pane title
	if vpH < 1 {
		vpH = 1
	}
	m.diffVP.Width = diffW
	m.diffVP.Height = vpH
	if infoW > 0 {
		m.infoVP.Width = infoW
		m.infoVP.Height = vpH
	}
	// The conversation page is full width.
	m.convVP.Width = m.width
	m.convVP.Height = bodyH - 1
	m.composer.SetWidth(m.width - 4)
	m.composer.SetHeight(5)
}

func (m *detailModel) refreshDiff() {
	if len(m.files) == 0 {
		m.diffVP.SetContent(mutedStyle(m.th).Render("  (no files changed)"))
		m.hunks = nil
		return
	}
	f := m.files[m.selected]
	m.diffVP.SetContent(renderDiff(m.th, f, m.diffVP.Width))
	m.hunks = hunkLineIndexes(f.Patch)
	m.diffVP.GotoTop()
}

func (m *detailModel) refreshInfo() {
	m.infoVP.SetContent(m.renderInfo())
}

func (m *detailModel) refreshConv() {
	m.convVP.SetContent(m.renderConversation())
	m.convVP.GotoTop()
}

// ---- view ----

func (m detailModel) View() string {
	if m.width == 0 {
		return "loading…"
	}
	if m.loading {
		return fmt.Sprintf("\n  loading PR #%d…", m.number)
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

	if m.state == stateComment || m.state == stateReject {
		return lipgloss.JoinVertical(lipgloss.Left, header, body, m.viewComposer())
	}
	return lipgloss.JoinVertical(lipgloss.Left, header, body, m.viewFooter())
}

func (m detailModel) viewConversation() string {
	bodyH := m.height - detailHeaderH - detailFooterH
	if bodyH < 3 {
		bodyH = 3
	}
	n := len(m.detail.Comments) + len(m.detail.Reviews) + len(m.detail.ReviewComments)
	title := fmt.Sprintf("Conversation · %d items", n)
	return m.paneBox(title, m.convVP.View(), m.width, bodyH, true)
}

// renderConversation builds the full-width, chronological thread.
func (m detailModel) renderConversation() string {
	w := m.convVP.Width
	if w < 8 {
		w = 8
	}
	entries := m.detail.Timeline()
	var b strings.Builder
	for i, e := range entries {
		if i > 0 {
			b.WriteString(mutedStyle(m.th).Render(strings.Repeat("─", w)) + "\n")
		}
		b.WriteString(m.conversationHeader(e) + "\n")
		body := strings.TrimSpace(e.Body)
		if body == "" {
			body = mutedStyle(m.th).Render("(no message)")
		} else {
			body = wrap(body, w)
		}
		b.WriteString(body + "\n")
	}
	if len(entries) <= 1 && strings.TrimSpace(m.detail.Body) == "" {
		return mutedStyle(m.th).Render("No conversation yet. Press c to comment.")
	}
	return b.String()
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
	line1 := lipgloss.NewStyle().Width(m.width).Background(m.th.Surface).Padding(0, 1).
		Render(fmt.Sprintf("%s %s  %s", num, title, state))

	refs := fmt.Sprintf("%s ← %s", m.detail.BaseRef, m.detail.HeadRef)
	stats := lipgloss.NewStyle().Foreground(m.th.Success).Render(fmt.Sprintf("+%d", m.detail.Additions)) +
		" " + lipgloss.NewStyle().Foreground(m.th.Danger).Render(fmt.Sprintf("-%d", m.detail.Deletions)) +
		mutedStyle(m.th).Render(fmt.Sprintf("  %d files · @%s", m.detail.ChangedFiles, m.detail.Author))
	line2 := lipgloss.NewStyle().Width(m.width).Padding(0, 1).
		Render(infoStyle(m.th).Render(refs) + "  " + stats)

	return lipgloss.JoinVertical(lipgloss.Left, line1, line2)
}

func (m detailModel) viewBody() string {
	bodyH := m.height - detailHeaderH - detailFooterH
	if bodyH < 3 {
		bodyH = 3
	}
	filesW, _, infoW := m.paneWidths()

	filePane := m.paneBox("Files", m.renderFileList(filesW, bodyH-1), filesW, bodyH, m.focus == focusFiles)
	diffTitle := "Diff"
	if len(m.files) > 0 {
		diffTitle = "Diff · " + shortPath(m.files[m.selected].Filename, m.diffVP.Width-8)
	}
	diffPane := m.paneBox(diffTitle, m.diffVP.View(), m.diffVP.Width, bodyH, m.focus == focusDiff)

	panes := []string{filePane, diffPane}
	if infoW > 0 {
		infoPane := m.paneBox("Conversation · Checks", m.infoVP.View(), infoW, bodyH, m.focus == focusInfo)
		panes = append(panes, infoPane)
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, joinWithGap(panes)...)
}

// paneBox renders a titled, fixed-size pane; the title bar is focus-colored
// when active.
func (m detailModel) paneBox(title, content string, w, h int, focused bool) string {
	titleStyle := lipgloss.NewStyle().Width(w).Foreground(m.th.Muted)
	if focused {
		titleStyle = titleStyle.Foreground(m.th.Focus).Bold(true).Underline(true)
	}
	body := lipgloss.NewStyle().Width(w).Height(h - 1).MaxHeight(h - 1).Render(content)
	return lipgloss.JoinVertical(lipgloss.Left, titleStyle.Render(title), body)
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
		letter := statusLetter(m.th, f.Status)
		name := truncate(shortRepo(f.Filename), w-10)
		counts := lipgloss.NewStyle().Foreground(m.th.Success).Render(fmt.Sprintf("+%d", f.Additions))
		line := fmt.Sprintf("%s %s %s", letter, pad(name, w-10), counts)
		if i == m.selected {
			line = lipgloss.NewStyle().Width(w).Foreground(m.th.Primary).Background(m.th.Surface).
				Render(fmt.Sprintf("%s %s", statusLetterPlain(f.Status), truncate(shortRepo(f.Filename), w-4)))
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func (m detailModel) renderInfo() string {
	var b strings.Builder
	h := func(s string) string { return infoStyle(m.th).Bold(true).Render(s) }

	if strings.TrimSpace(m.detail.Body) != "" {
		b.WriteString(h("Description") + "\n")
		b.WriteString(wrap(m.detail.Body, m.infoVP.Width) + "\n\n")
	}

	b.WriteString(h(fmt.Sprintf("Checks (%d)", len(m.detail.Checks))) + "\n")
	if len(m.detail.Checks) == 0 {
		b.WriteString(mutedStyle(m.th).Render("  none") + "\n")
	}
	for _, c := range m.detail.Checks {
		b.WriteString(checkGlyph(m.th, c.Conclusion) + " " + c.Name + "\n")
	}
	b.WriteString("\n")

	b.WriteString(h(fmt.Sprintf("Reviews (%d)", len(m.detail.Reviews))) + "\n")
	for _, r := range m.detail.Reviews {
		b.WriteString(reviewBadge(m.th, r.State) + " @" + r.Author + "\n")
		if strings.TrimSpace(r.Body) != "" {
			b.WriteString(mutedStyle(m.th).Render(wrap(r.Body, m.infoVP.Width)) + "\n")
		}
	}
	b.WriteString("\n")

	b.WriteString(h(fmt.Sprintf("Comments (%d)", len(m.detail.Comments))) + "\n")
	for _, c := range m.detail.Comments {
		b.WriteString(infoStyle(m.th).Render("@"+c.Author) + " " + mutedStyle(m.th).Render(relTime(c.CreatedAt)) + "\n")
		b.WriteString(wrap(c.Body, m.infoVP.Width) + "\n\n")
	}
	return b.String()
}

func (m detailModel) viewComposer() string {
	title := "Comment"
	if m.state == stateReject {
		title = "Request changes — reason"
	}
	head := warnStyle(m.th).Render(title) + mutedStyle(m.th).Render("   ctrl+s submit · esc cancel")
	return lipgloss.JoinVertical(lipgloss.Left, head, m.composer.View())
}

func (m detailModel) viewFooter() string {
	if m.status != "" {
		return lipgloss.NewStyle().Width(m.width).Padding(0, 1).Render(m.status)
	}
	var help string
	if m.page == pageConversation {
		help = "↑/↓ scroll · c reply · a approve · x request-changes · o open · d diff · esc back"
	} else {
		help = "tab focus · ↑/↓ move · [ ] file · n/N hunk · v conversation · c comment · a approve · x changes · o open · esc back"
	}
	return lipgloss.NewStyle().Width(m.width).Foreground(m.th.Muted).Padding(0, 1).
		Render(truncate(help, max(10, m.width-2)))
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

func reviewBadge(t theme.Theme, state string) string {
	switch state {
	case "APPROVED":
		return okStyle(t).Render("✓ approved")
	case "CHANGES_REQUESTED":
		return errStyle(t).Render("✗ changes")
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

// joinWithGap interleaves single-space gap columns between panes.
func joinWithGap(panes []string) []string {
	if len(panes) <= 1 {
		return panes
	}
	out := make([]string, 0, len(panes)*2-1)
	for i, p := range panes {
		if i > 0 {
			out = append(out, " ")
		}
		out = append(out, p)
	}
	return out
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
