package tui

import (
	"net/url"
	"runtime"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/dotnetemmanuel/cairn/internal/doctor"
)

// buildVersion is the running binary's version, handed over by main (which owns
// the linker-stamped value) so a bug report says which build it came from.
var buildVersion = "dev"

// SetVersion records the running binary's version for bug reports.
func SetVersion(v string) {
	if v != "" {
		buildVersion = v
	}
}

// doctorLimit keeps the prefilled report inside the URL length browsers accept.
// The doctor summary is the only field that can grow.
const doctorLimit = 1200

// bugReportURL builds a prefilled issue against Cairn's bug form. It is a pure
// function of what it is handed, so nothing from the environment can ride along
// into a public issue, and the doctor text it carries is the same text the user
// sees on screen (which never prints a token).
func bugReportURL(version, goos, goarch, doctorOut string) string {
	if len(doctorOut) > doctorLimit {
		doctorOut = doctorOut[:doctorLimit] + "\n… truncated, run cairn doctor for the rest"
	}
	q := url.Values{}
	q.Set("template", "bug_report.yml")
	q.Set("version", "cairn "+version)
	q.Set("environment", goos+"/"+goarch)
	q.Set("doctor", doctorOut)
	return "https://github.com/dotnetemmanuel/Cairn/issues/new?" + q.Encode()
}

// openBugReport opens a prefilled bug report in the browser: version, platform
// and the doctor summary are already in the form, so a stranger's first report
// is one you can act on.
func openBugReport() tea.Cmd {
	return openBrowser(bugReportURL(buildVersion, runtime.GOOS, runtime.GOARCH, doctor.Diagnose().String()))
}
