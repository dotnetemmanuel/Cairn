package tui

import (
	"net/url"
	"strings"
	"testing"
)

func TestBugReportURLCarriesWhatMakesAReportUsable(t *testing.T) {
	got := bugReportURL("v0.1.0", "linux", "amd64", "git: ok\ngh: ok, logged in as octocat")

	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("built an unparseable URL: %v", err)
	}
	if u.Host != "github.com" || !strings.HasPrefix(u.Path, "/dotnetemmanuel/Cairn/issues/new") {
		t.Errorf("must point at this repo's new-issue page, got %s", got)
	}
	q := u.Query()
	if q.Get("template") != "bug_report.yml" {
		t.Errorf("must select the bug form, got %q", q.Get("template"))
	}
	if !strings.Contains(q.Get("version"), "v0.1.0") {
		t.Errorf("version missing, got %q", q.Get("version"))
	}
	if env := q.Get("environment"); !strings.Contains(env, "linux") || !strings.Contains(env, "amd64") {
		t.Errorf("platform missing, got %q", env)
	}
	if !strings.Contains(q.Get("doctor"), "logged in as octocat") {
		t.Errorf("doctor output missing, got %q", q.Get("doctor"))
	}
}

// A whole terminal of doctor output would push the URL past what browsers accept,
// and a silently truncated URL opens a blank form.
func TestBugReportURLStaysShortEnoughToOpen(t *testing.T) {
	got := bugReportURL("v0.1.0", "darwin", "arm64", strings.Repeat("noise ", 2000))
	if len(got) > 2000 {
		t.Errorf("URL is %d chars, too long to rely on", len(got))
	}
	if !strings.Contains(got, "template=bug_report.yml") {
		t.Error("truncation must not cost us the form selection")
	}
}

// The report is built only from what it is handed, so nothing from the
// environment can ride along into a public issue.
func TestBugReportURLAddsNothingOfItsOwn(t *testing.T) {
	u, _ := url.Parse(bugReportURL("v0.1.0", "linux", "amd64", "clean"))
	var keys []string
	for k := range u.Query() {
		keys = append(keys, k)
	}
	if len(keys) != 4 {
		t.Errorf("expected exactly template, version, environment and doctor, got %v", keys)
	}
}
