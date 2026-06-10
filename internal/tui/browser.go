package tui

import (
	"io"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/cli/go-gh/v2/pkg/browser"
)

// openBrowser opens a URL in the user's browser (honoring $BROWSER), as the
// escape hatch to github.com. Errors are swallowed — it's a convenience.
func openBrowser(url string) tea.Cmd {
	return func() tea.Msg {
		if url == "" {
			return nil
		}
		b := browser.New("", io.Discard, io.Discard)
		_ = b.Browse(url)
		return nil
	}
}
