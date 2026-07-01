package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/dotnetemmanuel/cairn/internal/gh"
	"github.com/dotnetemmanuel/cairn/internal/theme"
)

// notifWebURL builds the github.com URL for a notification subject, so non-PR
// types (which have no in-app view) can open in the browser.
func notifWebURL(n gh.Notification) string {
	base := "https://github.com/" + n.Repo
	switch {
	case n.Type == "PullRequest" && n.Number > 0:
		return fmt.Sprintf("%s/pull/%d", base, n.Number)
	case n.Type == "Issue" && n.Number > 0:
		return fmt.Sprintf("%s/issues/%d", base, n.Number)
	default:
		return base
	}
}

// enterHint describes what pressing enter does on the active section's selected
// row, for the footer: fold/unfold a group header, open a PR/issue, or open a
// non-PR notification on GitHub.
func (m Model) enterHint() string {
	if len(m.sections) == 0 {
		return "open"
	}
	// On the inbox with the preview focused, enter opens the full interactive detail.
	if m.sections[m.active].isNotif() && m.notifPrev.focused {
		return "open PR"
	}
	switch it := m.sections[m.active].list.SelectedItem().(type) {
	case sectionHeader:
		if it.collapsible {
			return "fold / unfold"
		}
	case prItem:
		return "open PR"
	case notifItem:
		switch it.Type {
		case "PullRequest":
			return "read" // enter focuses the preview to read/scroll the conversation
		case "Issue":
			return "open issue"
		default:
			return "open on GitHub"
		}
	}
	return "open"
}

// selectedNotif returns the notification under the cursor on the active section,
// when that section is the inbox and the cursor is on a real row.
func (m Model) selectedNotif() (gh.Notification, bool) {
	if len(m.sections) == 0 {
		return gh.Notification{}, false
	}
	s := m.sections[m.active]
	if !s.isNotif() {
		return gh.Notification{}, false
	}
	if n, ok := s.list.SelectedItem().(notifItem); ok {
		return n.Notification, true
	}
	return gh.Notification{}, false
}

// notifPreviewCmd primes the preview pane for the currently-selected notification.
// For a PR it points the pane at that thread and, when its conversation isn't
// cached, kicks off a fetch (marking the pane loading); for an already-cached PR
// it renders the conversation into the scroll viewport. A no-op off a notification
// row, so it's safe to call after every move.
func (m *Model) notifPreviewCmd() tea.Cmd {
	n, ok := m.selectedNotif()
	if !ok {
		return nil
	}
	m.notifPrev.threadID = n.ThreadID

	// Only PRs have a conversation we render in-app; other subjects show a card.
	if n.Type != "PullRequest" || n.Number == 0 {
		m.notifPrev.loading = false
		return nil
	}
	if _, cached := m.notifPrev.cache[n.ThreadID]; cached {
		m.notifPrev.loading = false
		m.loadPreviewVP()
		return nil
	}
	m.notifPrev.loading = true
	owner, repo, _ := gh.SplitRepo(n.Repo)
	return loadNotifConv(n.ThreadID, owner, repo, n.Number)
}

// markReadMsg reports the result of a mark-read PATCH (only surfaced on error;
// the row is moved optimistically up front).
type markReadMsg struct {
	threadID string
	err      error
}

// markSelectedRead marks the selected notification read: it moves the row from
// UNREAD to READ optimistically (instant feedback) and fires the PATCH; an error
// flashes and a sync would correct the state. A no-op off a notification row or
// on an already-read one. (GitHub has no mark-unread, so this is one-way.)
func (m Model) markSelectedRead() (tea.Model, tea.Cmd) {
	n, ok := m.selectedNotif()
	if !ok || !n.Unread {
		return m, nil
	}
	s := &m.sections[m.active]
	idx := s.list.Index()

	kept := s.notifUnread[:0:0]
	for _, u := range s.notifUnread {
		if u.ThreadID == n.ThreadID {
			u.Unread = false
			s.notifRead = append([]gh.Notification{u}, s.notifRead...) // freshly-read on top
			continue
		}
		kept = append(kept, u)
	}
	s.notifUnread = kept
	s.total = len(s.notifUnread) + len(s.notifRead)
	s.rebuildRows(m.sortByRepo)
	// Keep the cursor roughly where it was, then settle onto a real row.
	s.list.Select(idx)
	preferItem(&s.list)

	return m, tea.Batch(markRead(n.ThreadID), m.notifPreviewCmd())
}

// markRead PATCHes a thread read off the main loop.
func markRead(threadID string) tea.Cmd {
	return func() tea.Msg {
		return markReadMsg{threadID: threadID, err: gh.MarkThreadRead(threadID)}
	}
}

// loadNotifConv fetches one PR's full detail (for its conversation) off the main
// loop, tagged with the thread id so the result reaches the right cache slot.
func loadNotifConv(threadID, owner, repo string, number int) tea.Cmd {
	return func() tea.Msg {
		detail, err := gh.FetchPRDetail(owner, repo, number)
		return notifConvMsg{threadID: threadID, detail: detail, err: err}
	}
}

// loadPreviewVP renders the selected thread's cached conversation into the preview
// viewport, scrolled to the latest activity (where a mention/new comment lives).
// The render is keyed by thread + theme + width, so it re-renders automatically
// after a theme toggle or resize (not just a selection change) and is otherwise a
// cheap no-op.
func (m *Model) loadPreviewVP() {
	id := m.notifPrev.threadID
	entry, ok := m.notifPrev.cache[id]
	if !ok {
		return
	}
	key := fmt.Sprintf("%s|%s|%d", id, theme.Fingerprint(m.th), m.notifPrev.vp.Width)
	if m.notifPrev.renderedAs == key {
		return
	}
	if entry.err != nil {
		m.notifPrev.vp.SetContent(errStyle(m.th).Render("couldn't load conversation: " + entry.err.Error()))
	} else {
		m.notifPrev.vp.SetContent(renderNotifConversation(entry.detail, m.notifPrev.vp.Width, m.th))
	}
	m.notifPrev.renderedAs = key
	m.notifPrev.vp.GotoBottom() // newest activity (the trigger) sits at the end
}

// cacheMiss reports whether the given thread's conversation isn't fetched yet, so
// the preview shows a spinner rather than stale content.
func (m Model) cacheMiss(threadID string) bool {
	_, ok := m.notifPrev.cache[threadID]
	return !ok
}

// renderNotifConversation renders a PR's conversation read-only, reusing the
// detail screen's renderer via a throwaway detail model (convCursor=-1 so no
// inline-reply cursor bar shows).
func renderNotifConversation(detail gh.PRDetail, width int, th theme.Theme) string {
	if width < 8 {
		width = 8
	}
	d := detailModel{th: th, detail: detail, convCursor: -1}
	d.convVP.Width = width
	s, _ := d.renderConversation()
	return s
}

// viewNotifications renders the inbox: the list (UNREAD/READ) on the left and the
// preview pane on the right, separated by a vertical bar. listW/bodyH come from
// viewBody; the list rows themselves are already in s.list.
func (m Model) viewNotifications(bodyH, bw int) string {
	s := m.sections[m.active]

	listW, previewW, twoPane := notifPaneDims(bw)
	listH := bodyH - 2 // title + rule
	if listH < 1 {
		listH = 1
	}

	label := s.title
	if pos, n := selectablePos(&s.list); n > 0 {
		label = fmt.Sprintf("%s  ▴ %d/%d ▾", s.title, pos, n)
	}
	// The focused pane wears the accent; the other recedes to muted, so it's clear
	// whether arrows move the list or scroll the preview.
	listAccent := m.th.Focus
	if m.notifPrev.focused {
		listAccent = m.th.Muted
	}
	title := lipgloss.NewStyle().Width(listW).Foreground(listAccent).Bold(true).Render(label)
	rule := lipgloss.NewStyle().Foreground(listAccent).Render(strings.Repeat("─", listW))

	var body string
	switch {
	case s.loading:
		body = fmt.Sprintf("  %s loading notifications…", m.spinner.View())
	case s.err != nil:
		body = errStyle(m.th).Render("  error: " + s.err.Error())
	default:
		body = s.list.View()
	}
	listPane := lipgloss.JoinVertical(lipgloss.Left,
		title, rule, lipgloss.NewStyle().Width(listW).Height(listH).Render(body))

	if !twoPane {
		return listPane
	}

	preview := m.renderNotifPreview(previewW, bodyH)
	return lipgloss.JoinHorizontal(lipgloss.Top,
		listPane, stackVBar(m.th, bodyH), preview)
}

// notifPreviewHeaderH is the number of lines the preview's metadata header takes
// (title, coordinates, when, rule) before the conversation body, plus its blank
// separator line.
const notifPreviewHeaderH = 5

// notifPaneDims splits the inbox body width into the (fixed) list pane and the
// preview pane, reporting whether there's room for two panes. Shared by the view
// and resizeLists so the preview viewport is sized to match what's drawn.
func notifPaneDims(bw int) (listW, previewW int, twoPane bool) {
	listW = notifListW
	if listW > bw-30 {
		listW = bw / 2
	}
	previewW = bw - listW - 1
	return listW, previewW, previewW >= 24
}

// renderNotifPreview draws the right pane: a metadata header (what / why / when)
// over the PR's read-only conversation, for the selected thread. Non-PR subjects
// (which have no in-app conversation) show a hint to open them on GitHub.
func (m Model) renderNotifPreview(w, h int) string {
	box := lipgloss.NewStyle().Width(w).Height(h)
	n, ok := m.selectedNotif()
	if !ok {
		return box.Render(mutedStyle(m.th).Render("  no notification selected"))
	}

	muted := mutedStyle(m.th)
	var b strings.Builder

	// Header: type glyph + title, then the "coordinates" line (where / why), the
	// timestamp, and a rule. When the pane is focused its title wears the accent and
	// a scroll cue, so it's obvious arrows now scroll here.
	glyph := lipgloss.NewStyle().Foreground(notifColor(m.th, n.Type)).Render(notifGlyph(n.Type))
	titleColor := m.th.Text
	cue := ""
	if m.notifPrev.focused {
		titleColor = m.th.Focus
		cue = lipgloss.NewStyle().Foreground(m.th.Focus).Render("  ↕")
	}
	titleStyle := lipgloss.NewStyle().Foreground(titleColor).Bold(true)
	b.WriteString(glyph + " " + titleStyle.Render(truncate(n.Title, w-6)) + cue + "\n")

	ref := n.Repo
	if n.Number > 0 {
		ref = fmt.Sprintf("%s#%d", n.Repo, n.Number)
	}
	reasonChip := lipgloss.NewStyle().Foreground(reasonColor(m.th, n.Reason)).
		Render(reasonGlyph(n.Reason) + " " + reasonLabel(n.Reason))
	b.WriteString(lipgloss.NewStyle().Foreground(m.th.Info).Render(ref) +
		muted.Render("  ·  ") + reasonChip + muted.Render("  ·  "+humanType(n.Type)) + "\n")
	b.WriteString(muted.Render(fmt.Sprintf("updated %s · %s",
		relTime(n.UpdatedAt), n.UpdatedAt.Format("2006-01-02 15:04"))) + "\n")
	b.WriteString(muted.Render(strings.Repeat("─", w)) + "\n")

	// Body: the read-only conversation in its scroll viewport, or a hint/spinner.
	switch {
	case n.Type != "PullRequest" || n.Number == 0:
		b.WriteString("\n  " + muted.Render("press ") +
			lipgloss.NewStyle().Foreground(m.th.Focus).Render("enter") +
			muted.Render(" to open this "+humanType(n.Type)+" on GitHub"))
	case m.notifPrev.loading && m.cacheMiss(n.ThreadID):
		b.WriteString("\n  " + m.spinner.View() + muted.Render(" loading conversation…"))
	default:
		b.WriteString(m.notifPrev.vp.View())
	}

	return box.Render(b.String())
}

// humanType turns a subject type into a friendly label.
func humanType(t string) string {
	switch t {
	case "PullRequest":
		return "pull request"
	case "CheckSuite":
		return "checks"
	default:
		return strings.ToLower(t)
	}
}
