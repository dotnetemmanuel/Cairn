package gh

import (
	"errors"
	"strings"
	"testing"
)

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
