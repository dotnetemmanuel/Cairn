package tui

import (
	"strings"
	"testing"

	"github.com/dotnetemmanuel/cairn/internal/gh"
	"github.com/dotnetemmanuel/cairn/internal/theme"
)

func withDevRepos(t *testing.T, on bool, repos ...string) {
	t.Helper()
	prevMode, prevRepos := devMode, devRepos
	devMode, devRepos = on, repos
	t.Cleanup(func() { devMode, devRepos = prevMode, prevRepos })
}

// A sandbox you test Cairn against is not real work: its PRs stay out of every
// list until you launch with dev mode, which is the whole point of the flag.
func TestSandboxIsFilteredOutUnlessDevMode(t *testing.T) {
	withDevRepos(t, false, "me/sandbox")

	got := hideDevRepos("is:open is:pr author:@me")
	if !strings.Contains(got, "-repo:me/sandbox") {
		t.Errorf("search must exclude the sandbox, got %q", got)
	}
	if !strings.HasPrefix(got, "is:open is:pr author:@me") {
		t.Errorf("the original filter must survive, got %q", got)
	}
	if !repoHidden("me/sandbox") || repoHidden("me/real-work") {
		t.Error("only the listed repos are hidden")
	}

	withDevRepos(t, true, "me/sandbox")
	if got := hideDevRepos("is:open is:pr author:@me"); got != "is:open is:pr author:@me" {
		t.Errorf("dev mode shows everything, got %q", got)
	}
	if repoHidden("me/sandbox") {
		t.Error("dev mode must show the sandbox")
	}
}

func TestNothingIsHiddenWhenNoDevReposAreConfigured(t *testing.T) {
	withDevRepos(t, false)
	if got := hideDevRepos("is:open"); got != "is:open" {
		t.Errorf("no configured repos means no exclusion, got %q", got)
	}
	if repoHidden("me/anything") {
		t.Error("nothing is hidden without configured dev repos")
	}
}

// Notifications come from the REST feed, not from search, so they need their own
// pass or the sandbox keeps ringing.
func TestNotificationsFromHiddenReposAreDropped(t *testing.T) {
	withDevRepos(t, false, "me/sandbox")
	feed := []gh.Notification{
		{Repo: "me/sandbox", Title: "sandbox noise"},
		{Repo: "me/real-work", Title: "actual work"},
	}
	kept := dropHiddenNotifications(feed)
	if len(kept) != 1 || kept[0].Repo != "me/real-work" {
		t.Errorf("kept %v, want only the real repo", kept)
	}

	withDevRepos(t, true, "me/sandbox")
	if len(dropHiddenNotifications(feed)) != 2 {
		t.Error("dev mode keeps the sandbox notifications")
	}
}

// Running with the sandbox visible has to be obvious, or a board full of test
// PRs reads as a bug.
func TestHeaderSaysWhenDevModeIsOn(t *testing.T) {
	withDevRepos(t, true, "me/sandbox")
	th := theme.New(theme.DefaultPalette())
	if !strings.Contains(renderBrandHeader(th, "octocat", 4992, false, nil, 100, ""), "dev mode") {
		t.Error("the header must show that dev mode is on")
	}
	withDevRepos(t, false, "me/sandbox")
	if strings.Contains(renderBrandHeader(th, "octocat", 4992, false, nil, 100, ""), "dev mode") {
		t.Error("no dev marker in the normal board")
	}
}
