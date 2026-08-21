package tui

import (
	"strings"

	"github.com/dotnetemmanuel/cairn/internal/gh"
)

// Dev mode shows the repos you test Cairn against (devRepos in config.yml, e.g. a
// sandbox). It is OFF unless you launch `cairn dev`, so a normal board only ever
// carries real work: those repos are excluded from every search, dropped from the
// notification feed, and with them go the closed tails and the stack sidebar.
var (
	devMode  bool
	devRepos []string
)

// EnableDev turns dev mode on for this run. Called from main before the TUI
// starts, mirroring gh.EnableDemo.
func EnableDev() { devMode = true }

// setDevRepos records the repos dev mode governs (owner/name), from the config.
func setDevRepos(repos []string) {
	devRepos = nil
	for _, r := range repos {
		if r = strings.TrimSpace(r); r != "" {
			devRepos = append(devRepos, r)
		}
	}
}

// hideDevRepos adds the -repo: terms that keep the dev repos out of a search.
// Returns the filter untouched in dev mode, or when none are configured.
func hideDevRepos(filter string) string {
	if devMode || len(devRepos) == 0 {
		return filter
	}
	var b strings.Builder
	b.WriteString(filter)
	for _, r := range devRepos {
		b.WriteString(" -repo:")
		b.WriteString(r)
	}
	return b.String()
}

// repoHidden reports whether owner/name is a dev repo being kept off the board.
func repoHidden(repo string) bool {
	if devMode {
		return false
	}
	for _, r := range devRepos {
		if strings.EqualFold(r, repo) {
			return true
		}
	}
	return false
}

// dropHiddenNotifications filters the REST notification feed, which is fetched
// whole rather than searched, so a hidden repo can't ring through it.
func dropHiddenNotifications(feed []gh.Notification) []gh.Notification {
	if devMode || len(devRepos) == 0 {
		return feed
	}
	kept := make([]gh.Notification, 0, len(feed))
	for _, n := range feed {
		if !repoHidden(n.Repo) {
			kept = append(kept, n)
		}
	}
	return kept
}
