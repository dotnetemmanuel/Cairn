package tui

import (
	"testing"
	"time"

	"github.com/dotnetemmanuel/cairn/internal/gh"
)

var closedInJuly = time.Date(2026, 7, 1, 11, 7, 51, 0, time.UTC)

func TestDropReusedNamesDropsAPRClosedBeforeTheBranchExisted(t *testing.T) {
	drift := map[string]gh.PRLanding{
		"feat-search": {Number: 23, ClosedAt: closedInJuly},
	}
	made := closedInJuly.Add(48 * time.Hour)
	got := dropReusedNames(drift, func(string) time.Time { return made })
	if _, ok := got["feat-search"]; ok {
		t.Fatal("kept a PR that was closed before the local branch had any commits")
	}
}

// The case the filter must not break: a teammate merged the branch this morning,
// which is exactly the drift warning the feature exists for.
func TestDropReusedNamesKeepsALandingAfterTheBranchExisted(t *testing.T) {
	drift := map[string]gh.PRLanding{
		"feat-search": {Number: 141, Merged: true, ClosedAt: closedInJuly},
	}
	made := closedInJuly.Add(-72 * time.Hour)
	got := dropReusedNames(drift, func(string) time.Time { return made })
	if _, ok := got["feat-search"]; !ok {
		t.Fatal("dropped a real drift: the branch predates the merge")
	}
}

func TestDropReusedNamesKeepsWhatItCannotDate(t *testing.T) {
	cases := map[string]struct {
		landing gh.PRLanding
		first   time.Time
	}{
		"no close date from GitHub": {gh.PRLanding{Number: 7}, closedInJuly.Add(time.Hour)},
		"git cannot date the branch": {gh.PRLanding{Number: 8, ClosedAt: closedInJuly}, time.Time{}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			drift := map[string]gh.PRLanding{"b": c.landing}
			got := dropReusedNames(drift, func(string) time.Time { return c.first })
			if _, ok := got["b"]; !ok {
				t.Fatal("dropped a landing it had no evidence against")
			}
		})
	}
}

// A branch first committed in the same second the PR closed is not proof of
// reuse, so it must survive.
func TestDropReusedNamesKeepsATieOnTheSecond(t *testing.T) {
	drift := map[string]gh.PRLanding{"b": {Number: 9, ClosedAt: closedInJuly}}
	got := dropReusedNames(drift, func(string) time.Time { return closedInJuly })
	if _, ok := got["b"]; !ok {
		t.Fatal("dropped a landing that closed exactly when the branch began")
	}
}

// The drift filter dates branches against the trunk, and a repo that HAS
// git-town never fills s.trunk — it is only prefilled for the init prompt. Read
// the trunk off the built tree or the filter silently does nothing.
func TestTrunkNameComesFromTheTreeNotTheInitPrefill(t *testing.T) {
	s := fixtureModel()
	s.trunk = ""
	if got := s.trunkName(); got != "main" {
		t.Fatalf("trunkName = %q, want %q from the built tree", got, "main")
	}
}

func TestTrunkNameFallsBackToTheDetectedTrunkWithoutATree(t *testing.T) {
	s := fixtureModel()
	s.tree = nil
	s.trunk = "trunkish"
	if got := s.trunkName(); got != "trunkish" {
		t.Fatalf("trunkName = %q, want the detected trunk when there is no tree", got)
	}
}
