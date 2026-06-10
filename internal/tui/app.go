// Package tui holds the Bubble Tea application. Phase 0 is intentionally
// minimal: a header showing the authenticated login and remaining rate limit,
// with `q` to quit and `r` to refresh.
package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/dotnetemmanuel/cairn/internal/config"
	"github.com/dotnetemmanuel/cairn/internal/gh"
	"github.com/dotnetemmanuel/cairn/internal/theme"
)

// Model is the root Bubble Tea model.
type Model struct {
	cfg config.Config
	th  theme.Theme

	width  int
	height int

	loading bool
	login   string
	rate    int
	limit   int
	err     error
}

// viewerMsg carries the result of an async viewer fetch.
type viewerMsg struct {
	v   gh.Viewer
	err error
}

// New constructs the root model from loaded config.
func New(cfg config.Config) Model {
	return Model{
		cfg:     cfg,
		th:      theme.New(cfg.Theme),
		loading: true,
	}
}

// Init kicks off the initial viewer fetch.
func (m Model) Init() tea.Cmd {
	return fetchViewer
}

func fetchViewer() tea.Msg {
	v, err := gh.FetchViewer()
	return viewerMsg{v: v, err: err}
}

// Update handles messages.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height

	case viewerMsg:
		m.loading = false
		if msg.err != nil {
			m.err = msg.err
		} else {
			m.err = nil
			m.login = msg.v.Login
			m.rate = msg.v.RateRemaining
			m.limit = msg.v.RateLimit
		}

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "r":
			if !m.loading {
				m.loading = true
				return m, fetchViewer
			}
		}
	}
	return m, nil
}

// View renders the header and a minimal body.
func (m Model) View() string {
	title := lipgloss.NewStyle().
		Foreground(m.th.Primary).
		Bold(true).
		Render("⟁ Cairn")

	var status string
	switch {
	case m.loading:
		status = lipgloss.NewStyle().Foreground(m.th.Focus).Render("connecting to GitHub…")
	case m.err != nil:
		status = lipgloss.NewStyle().Foreground(m.th.Danger).
			Render("auth/API error: " + m.err.Error())
	default:
		who := lipgloss.NewStyle().Foreground(m.th.Info).Render(m.login)
		calls := lipgloss.NewStyle().Foreground(m.th.Muted).
			Render(fmt.Sprintf("%d API calls remaining", m.rate))
		status = lipgloss.NewStyle().Foreground(m.th.Text).
			Render("Logged in as ") + who +
			lipgloss.NewStyle().Foreground(m.th.Muted).Render(" · ") + calls
	}

	headerInner := lipgloss.JoinHorizontal(lipgloss.Center, title, "   ", status)

	headerWidth := m.width
	if headerWidth <= 0 {
		headerWidth = lipgloss.Width(headerInner)
	}
	header := lipgloss.NewStyle().
		Width(headerWidth).
		Background(m.th.Surface).
		Foreground(m.th.Text).
		Padding(0, 1).
		Render(headerInner)

	help := lipgloss.NewStyle().
		Foreground(m.th.Muted).
		Padding(1, 1).
		Render("r refresh · q quit")

	return lipgloss.JoinVertical(lipgloss.Left, header, help)
}
