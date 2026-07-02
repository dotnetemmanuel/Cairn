package gh

import (
	"errors"
	"strings"
	"testing"
)

func TestPRMergeabilityBlockedAndReason(t *testing.T) {
	cases := []struct {
		name       string
		m          PRMergeability
		wantBlock  bool
		wantReason string
	}{
		{"clean mergeable", PRMergeability{Mergeable: "MERGEABLE"}, false, ""},
		{"unknown allowed", PRMergeability{Mergeable: "UNKNOWN"}, false, ""},
		{"conflicting", PRMergeability{Mergeable: "CONFLICTING"}, true, "conflicts with its base"},
		{"draft", PRMergeability{Draft: true, Mergeable: "MERGEABLE"}, true, "still a draft"},
		{"draft wins over conflict", PRMergeability{Draft: true, Mergeable: "CONFLICTING"}, true, "still a draft"},
	}
	for _, tc := range cases {
		if got := tc.m.Blocked(); got != tc.wantBlock {
			t.Errorf("%s: Blocked() = %v, want %v", tc.name, got, tc.wantBlock)
		}
		if got := tc.m.Reason(); got != tc.wantReason {
			t.Errorf("%s: Reason() = %q, want %q", tc.name, got, tc.wantReason)
		}
	}
}

func TestPRMergeabilityFixHintAndCaution(t *testing.T) {
	draft := PRMergeability{Draft: true}
	if h := draft.FixHint(); !strings.Contains(h, "ready for review") {
		t.Errorf("draft FixHint should point at marking ready, got %q", h)
	}
	conflict := PRMergeability{Mergeable: "CONFLICTING"}
	if h := conflict.FixHint(); !strings.Contains(h, "sync") {
		t.Errorf("conflict FixHint should point at sync, got %q", h)
	}
	if (PRMergeability{Mergeable: "MERGEABLE"}).FixHint() != "" {
		t.Error("a mergeable PR has no fix hint")
	}
	// Caution is non-blocking (does not count as Blocked) but still surfaces.
	cr := PRMergeability{Mergeable: "MERGEABLE", ReviewDecision: "CHANGES_REQUESTED"}
	if cr.Blocked() {
		t.Error("a review requirement must not be a hard block")
	}
	if cr.Caution() == "" {
		t.Error("changes-requested should surface a caution")
	}
	if (PRMergeability{ReviewDecision: "APPROVED"}).Caution() != "" {
		t.Error("an approved PR has no caution")
	}
}

func TestFriendlyMergeError(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"HTTP 405: Pull Request is in draft state and cannot be merged", "still a draft"},
		{"HTTP 405: Pull Request is not mergeable", "not mergeable"},
		{"Required status check \"ci\" is expected", "status checks"},
		{"At least 1 approving review is required by reviewers", "approving review"},
		{"Head branch was modified. Review and try the merge again.", "changed under us"},
		{"HTTP 404: Not Found", "wasn't found"},
		{"some unmapped failure", "couldn't merge"},
	}
	for _, tc := range cases {
		got := FriendlyMergeError(errors.New(tc.raw), "feat-x")
		if got == nil || !strings.Contains(got.Error(), tc.want) {
			t.Errorf("FriendlyMergeError(%q) = %v, want substring %q", tc.raw, got, tc.want)
		}
		if !strings.Contains(got.Error(), "feat-x") {
			t.Errorf("FriendlyMergeError should name the branch: %v", got)
		}
	}
	if FriendlyMergeError(nil, "feat-x") != nil {
		t.Error("nil error must pass through as nil")
	}
}

func TestFriendlyCreatePRError(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"422 No commits between main and feat-x", "no commits beyond"},
		{`Validation failed: field 'base' is invalid`, "base feat-base isn't on the remote"},
		{"A pull request already exists for owner:feat-x", "already exists"},
	}
	for _, c := range cases {
		got := friendlyCreatePRError(errors.New(c.raw), "feat-x", "feat-base")
		if !strings.Contains(got.Error(), c.want) {
			t.Errorf("friendlyCreatePRError(%q) = %q, want substring %q", c.raw, got, c.want)
		}
	}
}
