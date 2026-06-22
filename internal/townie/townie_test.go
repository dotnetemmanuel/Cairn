package townie

import (
	"errors"
	"strings"
	"testing"
)

// recordRunner records the commands it's asked to run and returns canned output.
type recordRunner struct {
	calls [][]string
	out   string
	err   error
}

func (r *recordRunner) Run(dir, name string, args ...string) (string, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	return r.out, r.err
}

// drain collects every event from a stream channel.
func drain(ch <-chan StreamEvent) []StreamEvent {
	var out []StreamEvent
	for ev := range ch {
		out = append(out, ev)
	}
	return out
}

func TestStreamFallbackEmitsLinesThenDone(t *testing.T) {
	rr := &recordRunner{out: "line one\nline two\n"}
	ops := Ops{Dir: "/repo", Runner: rr}
	evs := drain(ops.Stream("sync", ""))

	// Last event is the terminal Done; the rest are output lines.
	if len(evs) < 1 || !evs[len(evs)-1].Done {
		t.Fatalf("expected a trailing Done event, got %+v", evs)
	}
	var lines []string
	for _, e := range evs[:len(evs)-1] {
		lines = append(lines, e.Line)
	}
	if strings.Join(lines, "|") != "line one|line two" {
		t.Errorf("streamed lines = %v, want [line one, line two]", lines)
	}
	if len(rr.calls) != 1 || strings.Join(rr.calls[0], " ") != "git-town sync --stack" {
		t.Errorf("stream should still delegate via Runner, got %v", rr.calls)
	}
}

func TestStreamStopsOnError(t *testing.T) {
	// amend is two steps; a failing first step must not run the second, and the
	// Done event must carry the error.
	rr := &recordRunner{out: "boom", err: errors.New("amend failed")}
	ops := Ops{Dir: "/repo", Runner: rr}
	evs := drain(ops.Stream("amend", ""))
	last := evs[len(evs)-1]
	if !last.Done || last.Err == nil {
		t.Errorf("want Done event with error, got %+v", last)
	}
	if len(rr.calls) != 1 {
		t.Errorf("second step must be skipped after error, ran %d", len(rr.calls))
	}
}

func TestRunDelegatesToGitTown(t *testing.T) {
	cases := []struct {
		verb, name string
		want       []string
	}{
		{"new", "feat-x", []string{"git-town", "append", "feat-x"}},
		{"insert", "feat-y", []string{"git-town", "prepend", "feat-y"}},
		{"sync", "", []string{"git-town", "sync", "--stack"}},
		{"restack", "", []string{"git-town", "sync", "--stack", "--no-push"}},
	}
	for _, c := range cases {
		rr := &recordRunner{out: "ok"}
		ops := Ops{Dir: "/repo", Runner: rr}
		if _, err := ops.Run(c.verb, c.name); err != nil {
			t.Fatalf("%s: unexpected err %v", c.verb, err)
		}
		if len(rr.calls) != 1 || strings.Join(rr.calls[0], " ") != strings.Join(c.want, " ") {
			t.Errorf("%s ran %v, want %v", c.verb, rr.calls, c.want)
		}
	}
}

func TestAmendIsTwoSteps(t *testing.T) {
	rr := &recordRunner{out: "done"}
	ops := Ops{Dir: "/repo", Runner: rr}
	if _, err := ops.Run("amend", ""); err != nil {
		t.Fatalf("amend err: %v", err)
	}
	if len(rr.calls) != 2 {
		t.Fatalf("amend should run 2 commands, ran %d: %v", len(rr.calls), rr.calls)
	}
	if strings.Join(rr.calls[0], " ") != "git commit --amend --no-edit" {
		t.Errorf("step 1 = %v, want git commit --amend --no-edit", rr.calls[0])
	}
	if strings.Join(rr.calls[1], " ") != "git-town sync --stack --no-push" {
		t.Errorf("step 2 = %v, want git-town sync --stack --no-push", rr.calls[1])
	}
}

func TestInitRunsTwoConfigCommands(t *testing.T) {
	rr := &recordRunner{out: "ok"}
	ops := Ops{Dir: "/repo", Runner: rr}
	if _, err := ops.Run("init", "main"); err != nil {
		t.Fatalf("init err: %v", err)
	}
	if len(rr.calls) != 2 {
		t.Fatalf("init should run 2 commands, ran %d: %v", len(rr.calls), rr.calls)
	}
	if got := strings.Join(rr.calls[0], " "); got != "git config git-town.main-branch main" {
		t.Errorf("step 1 = %q", got)
	}
	if got := strings.Join(rr.calls[1], " "); got != "git config git-town.sync-feature-strategy rebase" {
		t.Errorf("step 2 = %q", got)
	}
}

func TestInitStopsIfFirstStepFails(t *testing.T) {
	rr := &recordRunner{err: errors.New("exit 1")}
	ops := Ops{Dir: "/repo", Runner: rr}
	if _, err := ops.Run("init", "main"); err == nil {
		t.Fatal("expected error to propagate")
	}
	if len(rr.calls) != 1 {
		t.Errorf("second config must not run after the first fails; calls = %v", rr.calls)
	}
}

func TestAmendStopsIfFirstStepFails(t *testing.T) {
	rr := &recordRunner{out: "nothing to amend", err: errors.New("exit 1")}
	ops := Ops{Dir: "/repo", Runner: rr}
	if _, err := ops.Run("amend", ""); err == nil {
		t.Fatal("expected error to propagate")
	}
	if len(rr.calls) != 1 {
		t.Errorf("restack must not run after amend fails; calls = %v", rr.calls)
	}
}

func TestUnknownVerb(t *testing.T) {
	ops := Ops{Dir: "/repo", Runner: &recordRunner{}}
	_, err := ops.Run("frobnicate", "")
	var ue *UnknownVerbError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnknownVerbError, got %v", err)
	}
}

func TestCatalogDrivesHelpAndDispatch(t *testing.T) {
	// Every catalog command must map to a real argv (its Hint is non-empty),
	// have a key, and carry pedagogical text.
	for _, c := range Catalog() {
		if c.Key == "" || c.Title == "" {
			t.Errorf("command %+v missing key/title", c)
		}
		if c.Hint() == "" {
			t.Errorf("command %q has no underlying command (bad verb?)", c.Title)
		}
		if len(c.Long) < 40 {
			t.Errorf("command %q needs a real explanation, got %q", c.Title, c.Long)
		}
	}
	if Find("n") == nil || Find("n").Verb != "new" {
		t.Error("Find(n) should resolve to new")
	}
	if Find("zzz") != nil {
		t.Error("Find on unknown key should be nil")
	}
}

func TestCheckoutArgvNotInCatalog(t *testing.T) {
	// checkout runs via Run but must NOT appear as a stack command in Catalog().
	for _, c := range Catalog() {
		if c.Verb == "checkout" {
			t.Fatal("checkout must stay out of Catalog() (it is not a stack mutation)")
		}
	}
	rr := &recordRunner{out: "Switched to branch 'feat-mid'"}
	ops := Ops{Dir: "/repo", Runner: rr}
	if _, err := ops.Run("checkout", "feat-mid"); err != nil {
		t.Fatalf("checkout run errored: %v", err)
	}
	if len(rr.calls) != 1 || strings.Join(rr.calls[0], " ") != "git checkout feat-mid" {
		t.Errorf("checkout ran %v, want git checkout feat-mid", rr.calls)
	}
}
