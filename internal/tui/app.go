// Package tui holds the Bubble Tea application. Phase 1 is the read-only
// dashboard: config-driven sections of PRs/issues from GraphQL, cycled with
// tab, navigated with j/k, refreshed with r.
package tui

import (
	"fmt"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/dotnetemmanuel/cairn/internal/config"
	"github.com/dotnetemmanuel/cairn/internal/gh"
	"github.com/dotnetemmanuel/cairn/internal/theme"
)

// searchLimit caps items fetched per section (the total match count is shown
// separately, so capping is visible rather than silent).
const searchLimit = 50

// section is one board tab — either a search filter or the notifications feed.
type section struct {
	title   string
	typ     string
	filter  string
	list    list.Model
	loading bool
	loaded  bool
	err     error
	total   int
}

// appMode selects which screen is active.
type appMode int

const (
	modeDashboard appMode = iota
	modeDetail
)

// Model is the root Bubble Tea model.
type Model struct {
	cfg config.Config
	th  theme.Theme

	width  int
	height int

	mode   appMode
	detail detailModel

	sections []section
	active   int

	spinner spinner.Model

	// header state
	headerLoading bool
	login         string
	rate          int
	limit         int
	headerErr     error
}

// New constructs the root model from loaded config.
func New(cfg config.Config) Model {
	th := theme.New(cfg.Theme)

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(th.Focus)

	m := Model{
		cfg:           cfg,
		th:            th,
		spinner:       sp,
		headerLoading: true,
	}

	delegate := itemDelegate{th: th}
	for _, s := range cfg.Sections {
		l := list.New(nil, delegate, 0, 0)
		l.SetShowTitle(false)
		l.SetShowStatusBar(false)
		l.SetShowHelp(false)
		l.SetShowPagination(true)
		l.SetFilteringEnabled(false)
		m.sections = append(m.sections, section{
			title:   s.Title,
			typ:     s.Type,
			filter:  s.Filter,
			list:    l,
			loading: true,
		})
	}
	return m
}

// ---- messages & commands ----

type viewerMsg struct {
	v   gh.Viewer
	err error
}

type sectionLoadedMsg struct {
	idx   int
	items []gh.Item
	total int
	err   error
}

func fetchViewer() tea.Msg {
	v, err := gh.FetchViewer()
	return viewerMsg{v: v, err: err}
}

func loadSection(idx int, typ, filter string) tea.Cmd {
	return func() tea.Msg {
		var (
			items []gh.Item
			total int
			err   error
		)
		if typ == config.SectionNotifications {
			items, total, err = gh.FetchNotifications(searchLimit)
		} else {
			items, total, err = gh.SearchItems(filter, searchLimit)
		}
		return sectionLoadedMsg{idx: idx, items: items, total: total, err: err}
	}
}

// Init fetches the viewer and loads every section concurrently (progressive
// render — each tab fills in as its query returns).
func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{fetchViewer, m.spinner.Tick}
	for i, s := range m.sections {
		cmds = append(cmds, loadSection(i, s.typ, s.filter))
	}
	return tea.Batch(cmds...)
}

// Update handles messages.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Global messages handled regardless of mode.
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.resizeLists()
		if m.mode == modeDetail {
			var cmd tea.Cmd
			m.detail, cmd = m.detail.Update(msg) // keep the detail screen sized
			return m, cmd
		}
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case detailExitMsg:
		m.mode = modeDashboard
		return m, nil
	}

	// In detail mode, everything else routes to the detail screen.
	if m.mode == modeDetail {
		var cmd tea.Cmd
		m.detail, cmd = m.detail.Update(msg)
		return m, cmd
	}

	switch msg := msg.(type) {
	case viewerMsg:
		m.headerLoading = false
		if msg.err != nil {
			m.headerErr = msg.err
		} else {
			m.headerErr = nil
			m.login = msg.v.Login
			m.rate = msg.v.RateRemaining
			m.limit = msg.v.RateLimit
		}
		return m, nil

	case sectionLoadedMsg:
		if msg.idx < 0 || msg.idx >= len(m.sections) {
			return m, nil
		}
		s := &m.sections[msg.idx]
		s.loading = false
		s.loaded = true
		s.err = msg.err
		s.total = msg.total
		if msg.err == nil {
			items := make([]list.Item, len(msg.items))
			for i, it := range msg.items {
				items[i] = prItem{it}
			}
			s.list.SetItems(items)
		}
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "enter":
			return m.openSelected()
		case "tab", "l", "right":
			m.active = (m.active + 1) % len(m.sections)
			return m, nil
		case "shift+tab", "h", "left":
			m.active = (m.active - 1 + len(m.sections)) % len(m.sections)
			return m, nil
		case "r":
			cmds := []tea.Cmd{fetchViewer}
			s := &m.sections[m.active]
			s.loading = true
			s.err = nil
			cmds = append(cmds, loadSection(m.active, s.typ, s.filter))
			m.headerLoading = true
			return m, tea.Batch(cmds...)
		}
		// Forward navigation to the active section's list.
		if len(m.sections) > 0 {
			var cmd tea.Cmd
			m.sections[m.active].list, cmd = m.sections[m.active].list.Update(msg)
			return m, cmd
		}
	}
	return m, nil
}

// openSelected opens the highlighted dashboard row in the detail screen, if it
// is a pull request. Non-PR rows (issues, number-less notifications) are
// ignored for now — the review pane is PR-only.
func (m Model) openSelected() (tea.Model, tea.Cmd) {
	if len(m.sections) == 0 {
		return m, nil
	}
	sel := m.sections[m.active].list.SelectedItem()
	it, ok := sel.(prItem)
	if !ok || !it.IsPR || it.Number == 0 {
		return m, nil
	}
	m.detail = newDetail(m.th, it.Item)
	m.detail, _ = m.detail.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
	m.mode = modeDetail
	return m, m.detail.Init()
}

// chrome heights, used for both layout and list sizing.
const (
	headerH = 1
	tabsH   = 1
	footerH = 1
)

func (m *Model) resizeLists() {
	bodyH := m.height - headerH - tabsH - footerH
	if bodyH < 1 {
		bodyH = 1
	}
	delegate := itemDelegate{th: m.th, width: m.width}
	for i := range m.sections {
		m.sections[i].list.SetDelegate(delegate)
		m.sections[i].list.SetSize(m.width, bodyH)
	}
}

// View renders header + tab bar + active section + footer.
func (m Model) View() string {
	if m.width == 0 {
		return "starting…"
	}
	if m.mode == modeDetail {
		return m.detail.View()
	}
	return lipgloss.JoinVertical(lipgloss.Left,
		m.viewHeader(),
		m.viewTabs(),
		m.viewBody(),
		m.viewFooter(),
	)
}

func (m Model) viewHeader() string {
	title := lipgloss.NewStyle().Foreground(m.th.Primary).Bold(true).Render("⟁ Cairn")

	var status string
	switch {
	case m.headerLoading:
		status = lipgloss.NewStyle().Foreground(m.th.Focus).Render("connecting…")
	case m.headerErr != nil:
		status = lipgloss.NewStyle().Foreground(m.th.Danger).Render(m.headerErr.Error())
	default:
		who := lipgloss.NewStyle().Foreground(m.th.Info).Render(m.login)
		calls := lipgloss.NewStyle().Foreground(m.th.Muted).
			Render(fmt.Sprintf("%d API calls remaining", m.rate))
		status = lipgloss.NewStyle().Foreground(m.th.Text).Render("Logged in as ") + who +
			lipgloss.NewStyle().Foreground(m.th.Muted).Render(" · ") + calls
	}

	inner := lipgloss.JoinHorizontal(lipgloss.Center, title, "   ", status)
	return lipgloss.NewStyle().
		Width(m.width).
		Background(m.th.Surface).
		Foreground(m.th.Text).
		Padding(0, 1).
		Render(inner)
}

func (m Model) viewTabs() string {
	var tabs []string
	for i, s := range m.sections {
		label := s.title
		if s.loaded && s.err == nil {
			label = fmt.Sprintf("%s (%d)", s.title, s.total)
		}
		style := lipgloss.NewStyle().Padding(0, 2).Foreground(m.th.Muted)
		if i == m.active {
			style = style.Foreground(m.th.Focus).Bold(true).
				Underline(true)
		}
		tabs = append(tabs, style.Render(label))
	}
	bar := lipgloss.JoinHorizontal(lipgloss.Bottom, tabs...)
	return lipgloss.NewStyle().Width(m.width).Render(bar)
}

func (m Model) viewBody() string {
	bodyH := m.height - headerH - tabsH - footerH
	if bodyH < 1 {
		bodyH = 1
	}
	if len(m.sections) == 0 {
		return lipgloss.NewStyle().Width(m.width).Height(bodyH).
			Foreground(m.th.Muted).Render("  no sections configured")
	}

	s := m.sections[m.active]
	box := lipgloss.NewStyle().Width(m.width).Height(bodyH)
	switch {
	case s.loading:
		return box.Render(fmt.Sprintf("  %s loading %s…", m.spinner.View(), s.title))
	case s.err != nil:
		return box.Render(lipgloss.NewStyle().Foreground(m.th.Danger).
			Render("  error: " + s.err.Error()))
	case len(s.list.Items()) == 0:
		return box.Render(lipgloss.NewStyle().Foreground(m.th.Muted).
			Render("  nothing here"))
	default:
		return s.list.View()
	}
}

func (m Model) viewFooter() string {
	help := "↑/↓ or j/k move · ←/→ or tab section · enter open · r refresh · q quit"
	return lipgloss.NewStyle().Width(m.width).Foreground(m.th.Muted).
		Padding(0, 1).Render(help)
}
