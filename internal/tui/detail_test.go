package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/dotnetemmanuel/cairn/internal/gh"
	"github.com/dotnetemmanuel/cairn/internal/theme"
)

func driveDetail(m detailModel, msgs ...tea.Msg) detailModel {
	for _, msg := range msgs {
		m, _ = m.Update(msg)
	}
	return m
}

func loadedDetail(t *testing.T) detailModel {
	t.Helper()
	th := theme.New(theme.DefaultPalette())
	m := newDetail(th, gh.Item{IsPR: true, Repo: "Grantigo/grantigo", Number: 327,
		Title: "Add a DrinkModal", URL: "https://github.com/Grantigo/grantigo/pull/327"})

	detail := gh.PRDetail{
		Number: 327, Title: "Add a DrinkModal", State: "OPEN",
		BaseRef: "main", HeadRef: "funfeat/emmanuel",
		Author: "dotnetemmanuel", Additions: 246, Deletions: 10, ChangedFiles: 2,
		Body:     "Adds a drink modal.",
		Reviews:  []gh.Review{{Author: "github-actions", State: "CHANGES_REQUESTED", CreatedAt: time.Now()}},
		Comments: []gh.Comment{{Author: "octocat", Body: "looks tasty", CreatedAt: time.Now()}},
		Checks:   []gh.Check{{Name: "review", Conclusion: "SUCCESS"}, {Name: "snyk", Conclusion: "SUCCESS"}},
	}
	files := []gh.FileDiff{
		{Filename: "src/DrinkModal.svelte", Status: "added", Additions: 183,
			Patch: "@@ -0,0 +1,2 @@\n+<script lang=\"ts\">\n+export let isOpen = false;"},
		{Filename: "README.md", Status: "modified", Additions: 5, Deletions: 1,
			Patch: "@@ -1,1 +1,1 @@\n-old line\n+new line"},
	}
	return driveDetail(m,
		tea.WindowSizeMsg{Width: 140, Height: 40},
		prLoadedMsg{detail: detail, files: files},
	)
}

func TestDetailRendersDiffConversationChecks(t *testing.T) {
	m := loadedDetail(t)
	view := m.View("")

	wants := []string{
		"#327", "Add a DrinkModal", "OPEN",
		"main", "funfeat/emmanuel", "+246", "-10",
		"DrinkModal.svelte", // file list
		"isOpen",            // diff content of selected file
		"Checks (2)", "review", "snyk",
		"changes",     // review badge for CHANGES_REQUESTED
		"looks tasty", // comment body
	}
	for _, w := range wants {
		if !strings.Contains(view, w) {
			t.Errorf("detail view missing %q", w)
		}
	}
}

func TestApproveConfirmFlow(t *testing.T) {
	m := loadedDetail(t)

	// 'a' arms the confirm prompt without submitting.
	m = driveDetail(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if m.state != stateConfirmApprove {
		t.Fatalf("expected stateConfirmApprove, got %d", m.state)
	}
	if !strings.Contains(m.View(""), "Approve PR #327") {
		t.Errorf("expected approve confirmation in footer")
	}

	// A non-y key cancels.
	cancelled := driveDetail(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	if cancelled.state != stateBrowsing {
		t.Fatalf("expected cancel back to browsing, got %d", cancelled.state)
	}

	// 'y' confirms — transitions to submitting and returns a command.
	confirmed, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if confirmed.state != stateSubmitting {
		t.Fatalf("expected stateSubmitting after confirm, got %d", confirmed.state)
	}
	if cmd == nil {
		t.Fatalf("expected a submit command after confirm")
	}
}

func TestCommentComposerOpens(t *testing.T) {
	m := loadedDetail(t)
	m = driveDetail(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	if m.state != stateComment {
		t.Fatalf("expected stateComment, got %d", m.state)
	}
	if !m.composer.Focused() {
		t.Errorf("expected composer focused")
	}
	// esc cancels back to browsing.
	m = driveDetail(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.state != stateBrowsing {
		t.Fatalf("expected browsing after esc, got %d", m.state)
	}
}

func TestEscEmitsExit(t *testing.T) {
	m := loadedDetail(t)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Fatal("expected a command on esc")
	}
	if _, ok := cmd().(detailExitMsg); !ok {
		t.Fatalf("expected detailExitMsg from esc command")
	}
}

func TestRenderDiffMarksAddsAndDels(t *testing.T) {
	th := theme.New(theme.DefaultPalette())
	out, _ := renderDiff(th, gh.FileDiff{
		Filename: "x.go",
		Patch:    "@@ -1,2 +1,2 @@\n-removed\n+added\n unchanged",
	}, 80, 0, -1, nil)
	for _, want := range []string{"removed", "added", "unchanged"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered diff missing %q", want)
		}
	}
}

func multiHunkDetail(t *testing.T) detailModel {
	t.Helper()
	th := theme.New(theme.DefaultPalette())
	m := newDetail(th, gh.Item{IsPR: true, Repo: "o/r", Number: 1})
	files := []gh.FileDiff{{
		Filename: "big.py", Status: "modified", Additions: 3, Deletions: 3,
		Patch: "@@ -1,1 +1,1 @@\n-a\n+a2\n@@ -10,1 +10,1 @@\n-b\n+b2\n@@ -20,1 +20,1 @@\n-c\n+c2",
	}}
	return driveDetail(m,
		tea.WindowSizeMsg{Width: 140, Height: 40},
		prLoadedMsg{detail: gh.PRDetail{Number: 1, State: "OPEN"}, files: files},
	)
}

func TestHunkNavigationAdvancesAndCycles(t *testing.T) {
	m := multiHunkDetail(t)
	if len(m.hunks) != 3 {
		t.Fatalf("expected 3 hunks, got %d", len(m.hunks))
	}
	if m.curHunk != 0 {
		t.Fatalf("expected to start on hunk 0, got %d", m.curHunk)
	}
	n := func() { m = driveDetail(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")}) }
	N := func() { m = driveDetail(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("N")}) }

	// 'n' advances; the title surfaces the position.
	n()
	if m.curHunk != 1 {
		t.Fatalf("expected hunk 1 after n, got %d", m.curHunk)
	}
	if !strings.Contains(m.View(""), "hunk 2/3") {
		t.Errorf("expected 'hunk 2/3' in view")
	}

	// 'n' past the last hunk wraps to the first.
	n() // → 2
	n() // → 0 (wrap)
	if m.curHunk != 0 {
		t.Fatalf("expected n past last to wrap to 0, got %d", m.curHunk)
	}

	// 'N' before the first wraps to the last.
	N()
	if m.curHunk != 2 {
		t.Fatalf("expected N on first to wrap to last (2), got %d", m.curHunk)
	}
}

func TestStatusLineClearsOnNextKey(t *testing.T) {
	m := multiHunkDetail(t)
	// Simulate a lingering error from a failed action.
	m.status = "✗ approve failed: boom"
	if !strings.Contains(m.View(""), "approve failed") {
		t.Fatalf("expected error to show before dismissal")
	}
	// Any browsing keystroke dismisses it.
	m = driveDetail(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	if m.status != "" {
		t.Errorf("expected status cleared after keystroke, got %q", m.status)
	}
}

func TestPatchLineMetaTracksSidesAndLines(t *testing.T) {
	meta := patchLineMeta("@@ -1,2 +1,3 @@\n ctx\n-removed\n+added1\n+added2")
	// idx0 = header (not commentable), then ctx, -removed, +added1, +added2.
	if meta[0].side != "" {
		t.Errorf("hunk header should not be commentable, got side %q", meta[0].side)
	}
	cases := []struct {
		idx  int
		side string
		line int
		code string
	}{
		{1, "RIGHT", 1, "ctx"},
		{2, "LEFT", 2, "removed"},
		{3, "RIGHT", 2, "added1"},
		{4, "RIGHT", 3, "added2"},
	}
	for _, c := range cases {
		got := meta[c.idx]
		if got.side != c.side || got.line != c.line || got.code != c.code {
			t.Errorf("meta[%d] = %+v, want side=%s line=%d code=%q", c.idx, got, c.side, c.line, c.code)
		}
	}
}

// inlineDetail builds a loaded detail at the given width with one multi-line
// file and an optional inline comment on big.py RIGHT:2.
func inlineDetail(t *testing.T, width int, withComment bool) detailModel {
	t.Helper()
	th := theme.New(theme.DefaultPalette())
	m := newDetail(th, gh.Item{IsPR: true, Repo: "o/r", Number: 7})
	detail := gh.PRDetail{Number: 7, State: "OPEN", HeadSHA: "deadbeef"}
	if withComment {
		detail.ReviewComments = []gh.ReviewComment{
			{Author: "octocat", Body: "tweak this", Path: "big.py", Line: 2, Side: "RIGHT", DatabaseID: 999, CreatedAt: time.Now()},
		}
	}
	files := []gh.FileDiff{{
		Filename: "big.py", Status: "modified", Additions: 2, Deletions: 1,
		Patch: "@@ -1,2 +1,3 @@\n ctx\n-removed\n+added1\n+added2",
	}}
	return driveDetail(m,
		tea.WindowSizeMsg{Width: width, Height: 40},
		prLoadedMsg{detail: detail, files: files},
	)
}

func TestLineCommentAnchorsToCursor(t *testing.T) {
	m := inlineDetail(t, 140, false)
	m.focus = focusDiff
	// cursor starts on the context line (idx 1); step down twice → idx 3.
	m = driveDetail(m,
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")},
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")},
	)
	m = driveDetail(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	if m.state != stateLineComment {
		t.Fatalf("expected stateLineComment, got %d", m.state)
	}
	if m.anchorPath != "big.py" || m.anchorSide != "RIGHT" || m.anchorLine != 2 {
		t.Fatalf("anchor = %s %s:%d, want big.py RIGHT:2", m.anchorPath, m.anchorSide, m.anchorLine)
	}
}

// An outdated thread comes back with Line null (0) and only OriginalLine set;
// the badge and line thread must still anchor via OriginalLine instead of
// vanishing from the diff.
func TestOutdatedCommentStillBadges(t *testing.T) {
	th := theme.New(theme.DefaultPalette())
	m := newDetail(th, gh.Item{IsPR: true, Repo: "o/r", Number: 7})
	detail := gh.PRDetail{Number: 7, State: "OPEN", HeadSHA: "deadbeef",
		ReviewComments: []gh.ReviewComment{
			{Author: "octocat", Body: "tweak this", Path: "big.py",
				Line: 0, OriginalLine: 2, Side: "RIGHT", DatabaseID: 999, CreatedAt: time.Now()},
		}}
	files := []gh.FileDiff{{
		Filename: "big.py", Status: "modified", Additions: 2, Deletions: 1,
		Patch: "@@ -1,2 +1,3 @@\n ctx\n-removed\n+added1\n+added2",
	}}
	m = driveDetail(m,
		tea.WindowSizeMsg{Width: 140, Height: 40},
		prLoadedMsg{detail: detail, files: files},
	)
	if cc := m.commentCounts(); cc[3] != 1 {
		t.Errorf("outdated comment should badge rendered line 3 via OriginalLine, got %v", cc)
	}
	m.focus = focusDiff
	m = driveDetail(m,
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")},
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")},
	)
	if lc := m.lineComments(); len(lc) != 1 {
		t.Fatalf("expected 1 line comment at cursor via OriginalLine, got %d", len(lc))
	}
}

func TestSuggestPrefillsBlock(t *testing.T) {
	m := inlineDetail(t, 140, false)
	m.focus = focusDiff
	m = driveDetail(m,
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")},
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")},
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")},
	)
	if m.state != stateLineComment {
		t.Fatalf("expected stateLineComment after s, got %d", m.state)
	}
	val := m.composer.Value()
	if !strings.Contains(val, "```suggestion") || !strings.Contains(val, "added1") {
		t.Errorf("suggestion composer = %q, want a suggestion block seeded with the line", val)
	}
}

func TestContextualPaneShowsLineThread(t *testing.T) {
	m := inlineDetail(t, 140, true)
	m.focus = focusDiff
	// Move onto RIGHT:2 (the commented line).
	m = driveDetail(m,
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")},
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")},
	)
	if lc := m.lineComments(); len(lc) != 1 {
		t.Fatalf("expected 1 line comment at cursor, got %d", len(lc))
	}
	if cc := m.commentCounts(); cc[3] != 1 {
		t.Errorf("expected a comment badge on rendered line 3, got %v", cc)
	}
	view := m.View("")
	for _, w := range []string{"Line thread", "tweak this", "@octocat"} {
		if !strings.Contains(view, w) {
			t.Errorf("contextual pane missing %q", w)
		}
	}
}

func TestReplyTargetsLineThread(t *testing.T) {
	m := inlineDetail(t, 140, true)
	m.focus = focusDiff
	// Move onto RIGHT:2 (the commented line), then reply.
	m = driveDetail(m,
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")},
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")},
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")},
	)
	if m.state != stateReply {
		t.Fatalf("expected stateReply after r on a commented line, got %d", m.state)
	}
	if m.replyTo != 999 {
		t.Errorf("replyTo = %d, want 999 (the thread comment's databaseId)", m.replyTo)
	}
	if m.replyAuthor != "octocat" {
		t.Errorf("replyAuthor = %q, want octocat", m.replyAuthor)
	}
	// The composer titles the reply target.
	if v := m.viewComposer(); !strings.Contains(v, "Reply to octocat") {
		t.Errorf("composer should title the reply; got %q", v)
	}
}

func TestConversationThreadReply(t *testing.T) {
	m := inlineDetail(t, 140, true) // one inline thread, DatabaseID 999
	// Open the full conversation; the thread index should be built.
	m = driveDetail(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("v")})
	if m.page != pageConversation {
		t.Fatalf("expected conversation page")
	}
	if len(m.convThreads) != 1 || m.convThreads[0].id != 999 {
		t.Fatalf("expected 1 conv thread with id 999, got %+v", m.convThreads)
	}
	// n keeps the cursor on the (only) thread; r replies to it.
	m = driveDetail(m,
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")},
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")},
	)
	if m.state != stateReply {
		t.Fatalf("expected stateReply from conversation, got %d", m.state)
	}
	if m.replyTo != 999 {
		t.Errorf("replyTo = %d, want 999", m.replyTo)
	}
	// The footer advertises the thread nav.
	if f := m.viewFooter(); m.state == stateReply {
		_ = f // composer is open now; footer check happens before reply below
	}
}

func TestConversationThreadNavFooter(t *testing.T) {
	m := inlineDetail(t, 140, true)
	m = driveDetail(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("v")})
	if !strings.Contains(m.viewFooter(), "n/N thread") {
		t.Errorf("conversation footer should advertise thread nav; got %q", m.viewFooter())
	}
}

func TestReplyNoopWithoutThread(t *testing.T) {
	m := inlineDetail(t, 140, false) // no comments anywhere
	m.focus = focusDiff
	m = driveDetail(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if m.state == stateReply {
		t.Error("r on a line without a thread must not open the reply composer")
	}
}

func TestTabSkipsHiddenInfoPane(t *testing.T) {
	m := inlineDetail(t, 90, false) // < 100 cols → info pane hidden
	if m.infoVisible() {
		t.Fatal("info pane should be hidden under 100 cols")
	}
	// Cycle focus a few times; it must never land on the hidden info pane.
	for i := 0; i < 4; i++ {
		m.focus = m.nextFocus(1)
		if m.focus == focusInfo {
			t.Fatalf("tab landed on hidden info pane after %d steps", i+1)
		}
	}
}

func TestReloadKeepsViewAfterAction(t *testing.T) {
	th := theme.New(theme.DefaultPalette())
	m := newDetail(th, gh.Item{IsPR: true, Repo: "o/r", Number: 9})
	files := []gh.FileDiff{
		{Filename: "README.md", Status: "modified", Patch: "@@ -1,1 +1,1 @@\n-old\n+new"},
		{Filename: "big.py", Status: "modified", Patch: "@@ -1,2 +1,3 @@\n ctx\n-removed\n+added1\n+added2"},
	}
	detail := gh.PRDetail{Number: 9, State: "OPEN"}
	m = driveDetail(m,
		tea.WindowSizeMsg{Width: 140, Height: 40},
		prLoadedMsg{detail: detail, files: files},
	)
	// Navigate onto the second file and move the cursor down.
	m.selected = 1
	m.refreshDiff()
	m.focus = focusDiff
	m = driveDetail(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	gotSel, gotCursor := m.selected, m.diffCursor

	// A post-action reload (keep:true) must NOT snap back to file 0.
	m, _ = m.Update(prLoadedMsg{detail: detail, files: files, keep: true})
	if m.selected != gotSel {
		t.Errorf("reload changed file from %d to %d", gotSel, m.selected)
	}
	if m.diffCursor != gotCursor {
		t.Errorf("reload changed cursor from %d to %d", gotCursor, m.diffCursor)
	}

	// An initial load (keep:false) resets to the first file.
	m, _ = m.Update(prLoadedMsg{detail: detail, files: files, keep: false})
	if m.selected != 0 {
		t.Errorf("initial load should reset to file 0, got %d", m.selected)
	}
}

func TestConversationPageShowsComments(t *testing.T) {
	m := loadedDetail(t)
	m = driveDetail(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("v")})
	if m.page != pageConversation {
		t.Fatalf("expected conversation page")
	}
	view := m.View("")
	for _, w := range []string{"Conversation", "@octocat", "looks tasty", "@github-actions", "c comment"} {
		if !strings.Contains(view, w) {
			t.Errorf("conversation view missing %q", w)
		}
	}
	// esc returns to diff page, not exit.
	m, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.page != pageDiff {
		t.Fatalf("expected esc to return to diff page")
	}
	if cmd != nil {
		if _, ok := cmd().(detailExitMsg); ok {
			t.Fatalf("esc from conversation should not exit to dashboard")
		}
	}
}

func TestConversationThreadsReplyUnderAnchor(t *testing.T) {
	th := theme.New(theme.DefaultPalette())
	m := newDetail(th, gh.Item{IsPR: true, Repo: "o/r", Number: 9})
	base := time.Now().Add(-3 * time.Hour)
	detail := gh.PRDetail{
		Number: 9, State: "OPEN", HeadSHA: "deadbeef",
		Reviews: []gh.Review{
			{ID: "REV1", Author: "daniel", State: "CHANGES_REQUESTED", Body: "see comments", CreatedAt: base},
		},
		ReviewComments: []gh.ReviewComment{
			{ThreadID: 1, Author: "daniel", Body: "rename this var", Path: "a.go", Line: 5,
				DiffHunk: "@@ -1 +1 @@\n+x := 1", ReviewID: "REV1", CreatedAt: base.Add(time.Minute)},
			{ThreadID: 1, Author: "emmanuel", Body: "renamed in fixup", Path: "a.go", Line: 5,
				CreatedAt: base.Add(time.Hour)},
		},
	}
	files := []gh.FileDiff{{Filename: "a.go", Status: "modified", Additions: 1, Patch: "@@ -1 +1 @@\n+x := 1"}}
	m = driveDetail(m,
		tea.WindowSizeMsg{Width: 140, Height: 40},
		prLoadedMsg{detail: detail, files: files},
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("v")}, // open full conversation
	)
	view := m.View("")
	// The anchor, its citation, and the reply all show; the reply carries the ╰→ guide.
	for _, w := range []string{"@daniel", "rename this var", "@emmanuel", "renamed in fixup", "╰→"} {
		if !strings.Contains(view, w) {
			t.Errorf("threaded conversation missing %q", w)
		}
	}
	// The reply must appear AFTER the anchor body (threaded beneath, not before).
	if strings.Index(view, "renamed in fixup") < strings.Index(view, "rename this var") {
		t.Error("reply should render beneath the anchor, not above it")
	}
}

func TestRenderDiffWrapsLongLinesAndMapsRows(t *testing.T) {
	th := theme.New(theme.DefaultPalette())
	longAdd := "+" + strings.Repeat("x", 200) // one very long added line
	f := gh.FileDiff{Filename: "x.txt", Patch: "@@ -1,1 +1,2 @@\n ctx\n" + longAdd}
	content, rowAt := renderDiff(th, f, 40, 0, -1, nil)

	// 3 patch lines (header, ctx, long add); the long add must span many rows.
	if len(rowAt) != 3 {
		t.Fatalf("expected rowAt for 3 patch lines, got %d", len(rowAt))
	}
	rows := strings.Count(content, "\n") + 1
	if rows <= 3 {
		t.Fatalf("long line should wrap to extra rows; got only %d visual rows", rows)
	}
	// rowAt is monotonic and the last line starts well past its patch index.
	if !(rowAt[0] == 0 && rowAt[1] == 1 && rowAt[2] >= 2) {
		t.Errorf("rowAt mapping wrong: %v", rowAt)
	}
	// No visual row exceeds the pane width (no terminal-level wrapping).
	for _, line := range strings.Split(content, "\n") {
		if lipgloss.Width(line) > 40 {
			t.Errorf("row exceeds width 40 (=%d): %q", lipgloss.Width(line), line)
		}
	}
}

func TestCopyLinkResolvesCommentPermalink(t *testing.T) {
	const prURL = "https://github.com/o/r/pull/7"
	const want = "https://github.com/o/r/pull/7#discussion_r999"

	// Conversation page: the selected inline thread → its discussion permalink.
	m := inlineDetail(t, 140, true) // one inline thread, DatabaseID 999
	m.url = prURL
	m = driveDetail(m,
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("v")},
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")},
	)
	if url, kind := m.linkForSelection(); url != want || kind != "comment" {
		t.Errorf("conversation: linkForSelection = %q/%q, want %q/comment", url, kind, want)
	}

	// Diff page: the inline comment on the cursor line → the same permalink.
	d := inlineDetail(t, 140, true)
	d.url = prURL
	d.focus = focusDiff
	d = driveDetail(d,
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")},
		tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")},
	)
	if url, kind := d.linkForSelection(); url != want || kind != "comment" {
		t.Errorf("diff: linkForSelection = %q/%q, want %q/comment", url, kind, want)
	}

	// Nothing comment-specific selected → fall back to the PR link.
	f := inlineDetail(t, 140, false)
	f.url = prURL
	if url, kind := f.linkForSelection(); url != prURL || kind != "PR" {
		t.Errorf("fallback: linkForSelection = %q/%q, want %q/PR", url, kind, prURL)
	}
}
