package tui

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/dotnetemmanuel/cairn/internal/gh"
	"github.com/dotnetemmanuel/cairn/internal/theme"
)

// prItem adapts a gh.Item to the bubbles list.Item interface.
type prItem struct{ gh.Item }

func (i prItem) FilterValue() string { return i.Title }

// itemDelegate renders one item as a single colored row of columns:
//
//	<ci> <repo#num>  <review>  <title>                <author>  <updated>
type itemDelegate struct {
	th    theme.Theme
	width int
}

func (d itemDelegate) Height() int                             { return 1 }
func (d itemDelegate) Spacing() int                            { return 0 }
func (d itemDelegate) Update(tea.Msg, *list.Model) tea.Cmd     { return nil }
func (d itemDelegate) Render(w io.Writer, m list.Model, index int, listItem list.Item) {
	it, ok := listItem.(prItem)
	if !ok {
		return
	}
	selected := index == m.Index()

	const (
		refW    = 26
		revW    = 1
		authorW = 14
		updW    = 5
		gaps    = 6 // single spaces between the 6 visible fields
	)
	titleW := d.width - (2 + refW + revW + authorW + updW + gaps)
	if titleW < 8 {
		titleW = 8
	}

	ci := ciGlyph(d.th, it.Checks)
	rev := reviewGlyph(d.th, it.Item)

	refText := shortRepo(it.Repo)
	if it.Number > 0 {
		refText = fmt.Sprintf("%s#%d", refText, it.Number)
	}
	ref := pad(truncate(refText, refW), refW)
	title := pad(truncate(it.Title, titleW), titleW)
	author := pad(truncate(it.Author, authorW), authorW)
	upd := pad(relTime(it.UpdatedAt), updW)

	if selected {
		// On the selection bar, colors would clash — render the whole row in
		// primary-on-surface, keeping only the CI dot colored for at-a-glance
		// status. (The dot uses true color regardless of background.)
		rowStyle := lipgloss.NewStyle().
			Foreground(d.th.Primary).
			Background(d.th.Surface).
			Width(d.width)
		plain := strings.Join([]string{ref, rev, title, author, upd}, " ")
		fmt.Fprint(w, rowStyle.Render(ci+" "+plain))
		return
	}

	refStyled := lipgloss.NewStyle().Foreground(d.th.Info).Render(ref)
	titleStyled := lipgloss.NewStyle().Foreground(d.th.Text).Render(title)
	authorStyled := lipgloss.NewStyle().Foreground(d.th.Muted).Render(author)
	updStyled := lipgloss.NewStyle().Foreground(d.th.Muted).Render(upd)

	row := strings.Join([]string{ci, refStyled, rev, titleStyled, authorStyled, updStyled}, " ")
	fmt.Fprint(w, row)
}

// ciGlyph maps a check rollup state to a colored dot.
func ciGlyph(th theme.Theme, s gh.CheckState) string {
	var c lipgloss.Color
	switch s {
	case gh.CheckSuccess:
		c = th.Success
	case gh.CheckFailure, gh.CheckError:
		c = th.Danger
	case gh.CheckPending, gh.CheckExpected:
		c = th.Warning
	default:
		return lipgloss.NewStyle().Foreground(th.Muted).Render("○")
	}
	return lipgloss.NewStyle().Foreground(c).Render("●")
}

// reviewGlyph maps an item's review situation to a colored mark. A pending
// review request from you (◆, bright) outranks everything else — it's your cue
// to act; a request that's only on others shows a muted hollow ◇.
func reviewGlyph(th theme.Theme, it gh.Item) string {
	switch {
	case it.ReviewReqFromMe:
		return lipgloss.NewStyle().Foreground(th.Focus).Bold(true).Render("◆")
	case it.Review == gh.ReviewApproved:
		return lipgloss.NewStyle().Foreground(th.Success).Render("✓")
	case it.Review == gh.ReviewChangesRequested:
		return lipgloss.NewStyle().Foreground(th.Danger).Render("✗")
	case it.ReviewReqFromOthers:
		return lipgloss.NewStyle().Foreground(th.Muted).Render("◇")
	case it.Review == gh.ReviewRequired:
		return lipgloss.NewStyle().Foreground(th.Warning).Render("•")
	default:
		return lipgloss.NewStyle().Foreground(th.Muted).Render("–")
	}
}

// shortRepo drops the owner when present, keeping the row compact while still
// distinguishing repos within an org section.
func shortRepo(nameWithOwner string) string {
	if i := strings.IndexByte(nameWithOwner, '/'); i >= 0 {
		return nameWithOwner[i+1:]
	}
	return nameWithOwner
}

// relTime renders a compact relative age like "3h" or "5d".
func relTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := clock().Sub(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	default:
		return fmt.Sprintf("%dy", int(d.Hours()/24/365))
	}
}

// clock is overridable in tests; production uses the wall clock.
var clock = time.Now

// truncate shortens s to max runes, adding an ellipsis when cut.
func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max <= 1 {
		return string(r[:max])
	}
	return string(r[:max-1]) + "…"
}

// pad right-pads s with spaces to width w (by rune count).
func pad(s string, w int) string {
	n := w - len([]rune(s))
	if n <= 0 {
		return s
	}
	return s + strings.Repeat(" ", n)
}
