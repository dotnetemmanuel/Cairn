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

// sectionHeader is a non-selectable divider row that labels the group of items
// that follows it (e.g. "OPEN", "CLOSED"). Navigation skips over it; see
// ensureSelectable / selectAdjacent in app.go.
type sectionHeader struct{ label string }

func (sectionHeader) FilterValue() string { return "" }

// isClosed reports whether an item is no longer open (CLOSED or MERGED).
func isClosed(it gh.Item) bool {
	return it.State != "" && !strings.EqualFold(it.State, "OPEN")
}

// itemDelegate renders one item as a single colored row of columns:
//
//	<ci> <repo#num>  <review>  <title>                <author>  <updated>
type itemDelegate struct {
	th    theme.Theme
	width int
}

// focusGlyph is the single cursor marker for the focused/selected item, used the
// same way across every view (PR rows, files, diff lines, stack tree,
// conversation, conflict rail) so focus reads consistently. Semantic markers
// (current branch, review diamonds, resolved state) are intentionally distinct.
const focusGlyph = "❯"

// Column widths for a PR row, shared by the row renderer and the column header
// so they stay aligned. A leading focus cell (glyph + space) precedes the CI dot.
const (
	focusW     = 2 // focusGlyph + its trailing space
	colRefW    = 26
	colRevW    = 1
	colAuthorW = 14
	colUpdW    = 5
	colGaps    = 6 // single spaces between the 6 visible fields
)

// titleColW is the flexible title column for a list of the given total width.
func titleColW(total int) int {
	w := total - (focusW + 2 + colRefW + colRevW + colAuthorW + colUpdW + colGaps)
	if w < 8 {
		w = 8
	}
	return w
}

// columnHeader renders the muted, aligned column-label row shown above a PR
// section's list. authorLabel lets a section relabel the author column (e.g.
// "Opened by" for the review queue).
func columnHeader(th theme.Theme, total int, authorLabel string) string {
	cells := []string{
		" ", // focus-cursor column
		" ", // CI dot
		pad("PR", colRefW),
		pad("", colRevW),
		pad("Title", titleColW(total)),
		pad(truncate(authorLabel, colAuthorW), colAuthorW),
		pad("Upd", colUpdW),
	}
	return lipgloss.NewStyle().Foreground(th.Muted).Bold(true).
		Render(strings.Join(cells, " "))
}

func (d itemDelegate) Height() int                         { return 1 }
func (d itemDelegate) Spacing() int                        { return 0 }
func (d itemDelegate) Update(tea.Msg, *list.Model) tea.Cmd { return nil }
func (d itemDelegate) Render(w io.Writer, m list.Model, index int, listItem list.Item) {
	if h, ok := listItem.(sectionHeader); ok {
		if h.label == "" {
			// A blank spacer row (e.g. breathing room above the CLOSED divider).
			return
		}
		style := lipgloss.NewStyle().Foreground(d.th.Muted).Bold(true)
		text := "─ " + h.label + " "
		fill := d.width - lipgloss.Width(text)
		if fill < 0 {
			fill = 0
		}
		fmt.Fprint(w, style.Render(text+strings.Repeat("─", fill)))
		return
	}
	it, ok := listItem.(prItem)
	if !ok {
		return
	}
	selected := index == m.Index()

	// Leading focus cursor: the unified ❯ when this row is selected, a blank cell
	// otherwise so every row keeps the same left margin.
	focusCell := " "
	if selected {
		focusCell = focusGlyph
	}

	titleW := titleColW(d.width)

	ci := ciGlyph(d.th, it.Checks)
	rev := reviewGlyph(d.th, it.Item)

	refText := shortRepo(it.Repo)
	if it.Number > 0 {
		refText = fmt.Sprintf("%s#%d", refText, it.Number)
	}
	ref := pad(truncate(refText, colRefW), colRefW)
	// Draft PRs stay listed under OPEN but carry a muted "draft" tag so the eye
	// can tell ready-for-review work from parked work. The tag lives in the
	// title cell (ASCII prefix) so column alignment is preserved.
	draftTag := ""
	if it.IsPR && it.IsDraft && !isClosed(it.Item) {
		draftTag = "DRAFT "
	}
	title := pad(truncate(draftTag+it.Title, titleW), titleW)
	author := pad(truncate(it.Author, colAuthorW), colAuthorW)
	upd := pad(relTime(it.UpdatedAt), colUpdW)

	if selected {
		// On the selection bar, colors would clash — render the whole row in
		// primary-on-surface, keeping only the CI dot colored for at-a-glance
		// status. (The dot uses true color regardless of background.)
		rowStyle := lipgloss.NewStyle().
			Foreground(d.th.Primary).
			Background(d.th.Surface).
			Width(d.width)
		// The draft tag keeps its peach color on the bar (same nesting trick as
		// the CI dot): style it explicitly over the surface so it survives the
		// uniform row style instead of turning primary like the rest.
		titleCell := title
		if draftTag != "" {
			tag := lipgloss.NewStyle().Foreground(d.th.Warning).Background(d.th.Surface).
				Bold(true).Render(title[:len(draftTag)])
			titleCell = tag + title[len(draftTag):]
		}
		plain := strings.Join([]string{ref, rev, titleCell, author, upd}, " ")
		fmt.Fprint(w, rowStyle.Render(focusCell+" "+ci+" "+plain))
		return
	}

	// Closed/merged rows recede: the whole row is muted so the eye skips past
	// them, and the leading dot carries the only color — pink for merged, gray
	// for plain-closed — so the two are distinguishable at a glance.
	if isClosed(it.Item) {
		muted := lipgloss.NewStyle().Foreground(d.th.Muted)
		row := strings.Join([]string{
			focusCell, stateDot(d.th, it.State),
			muted.Render(ref), muted.Render("–"),
			muted.Render(title), muted.Render(author), muted.Render(upd),
		}, " ")
		fmt.Fprint(w, row)
		return
	}

	refStyled := lipgloss.NewStyle().Foreground(d.th.Info).Render(ref)
	titleStyled := styleTitle(d.th, title, draftTag)
	authorStyled := lipgloss.NewStyle().Foreground(d.th.Muted).Render(author)
	updStyled := lipgloss.NewStyle().Foreground(d.th.Muted).Render(upd)

	row := strings.Join([]string{focusCell, ci, refStyled, rev, titleStyled, authorStyled, updStyled}, " ")
	fmt.Fprint(w, row)
}

// styleTitle colors the unfocused-row title cell. The title recedes to muted —
// like the author/time columns — so the bright selected row stands out; any
// leading draft tag keeps its peach color to read as a flag. draftTag is the
// exact ASCII prefix baked into the padded title (empty when not a draft).
func styleTitle(th theme.Theme, padded, draftTag string) string {
	textStyle := lipgloss.NewStyle().Foreground(th.Muted)
	if draftTag == "" || len(padded) < len(draftTag) {
		return textStyle.Render(padded)
	}
	// Peach (the "not ready / parked" cue) so the tag stands apart from the
	// muted title/author/time columns instead of blending into them.
	tag := lipgloss.NewStyle().Foreground(th.Warning).Bold(true).Render(padded[:len(draftTag)])
	return tag + textStyle.Render(padded[len(draftTag):])
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

// stateDot marks a non-open row by lifecycle, replacing the CI dot (whose
// status is moot once a PR is closed): a merged PR gets a bright dot, a
// plain-closed one stays muted.
func stateDot(th theme.Theme, state string) string {
	c := th.Muted
	if strings.EqualFold(state, "MERGED") {
		c = th.Primary
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
