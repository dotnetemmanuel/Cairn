package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/dotnetemmanuel/cairn/internal/gh"
	"github.com/dotnetemmanuel/cairn/internal/townie"
)

// fakeShipAPI is the GitHub half of a whole-stack ship, recording every call in
// order so a test can assert what ran between two merges.
type fakeShipAPI struct {
	log      *[]string
	prs      map[string]int
	branchOf map[int]string
	kids     map[string][]int
	state    map[string][]gh.PRMergeSnapshot // per branch, answered in order; last one repeats
	mergeErr map[string]error
}

func newFakeShipAPI(log *[]string, branches ...string) *fakeShipAPI {
	f := &fakeShipAPI{
		log: log, prs: map[string]int{}, branchOf: map[int]string{},
		kids: map[string][]int{}, state: map[string][]gh.PRMergeSnapshot{},
		mergeErr: map[string]error{},
	}
	for i, b := range branches {
		num := 100 + i
		f.prs[b] = num
		f.branchOf[num] = b
		if i > 0 {
			f.kids[branches[i-1]] = []int{num}
		}
	}
	return f
}

func (f *fakeShipAPI) add(s string) { *f.log = append(*f.log, s) }

func (f *fakeShipAPI) findPROpen(_, _, branch string) (int, error) {
	f.add("find:" + branch)
	return f.prs[branch], nil
}

func (f *fakeShipAPI) mergePR(_, _ string, num int, _ string) error {
	b := f.branchOf[num]
	f.add("merge:" + b)
	return f.mergeErr[b]
}

func (f *fakeShipAPI) prsWithBase(_, _, base string) ([]int, error) {
	return f.kids[base], nil
}

func (f *fakeShipAPI) retargetPR(_, _ string, num int, base string) error {
	f.add("retarget:" + f.branchOf[num] + "->" + base)
	return nil
}

func (f *fakeShipAPI) deleteRemoteBranch(_, _, branch string) error {
	f.add("delete:" + branch)
	return nil
}

func (f *fakeShipAPI) mergeState(_, _ string, num int) (gh.PRMergeSnapshot, error) {
	b := f.branchOf[num]
	f.add("state:" + b)
	answers := f.state[b]
	if len(answers) == 0 {
		return gh.PRMergeSnapshot{
			PRMergeability: gh.PRMergeability{Number: num, Mergeable: "MERGEABLE"},
			HeadOid:        syncedSHA, BaseRef: "main",
		}, nil
	}
	st := answers[0]
	if len(answers) > 1 {
		f.state[b] = answers[1:]
	}
	return st, nil
}

// syncedSHA is what the fake sync leaves the branch pointing at, so a merge state
// carrying any other head is a verdict computed before that push.
const syncedSHA = "after-the-sync"

// syncRunner records each git-town sync in the same log as the API calls, so the
// test sees the true interleaving of merges and syncs.
type syncRunner struct {
	log *[]string
	err error
}

func (r *syncRunner) Run(_, name string, args ...string) (string, error) {
	cmd := strings.Join(append([]string{name}, args...), " ")
	switch {
	case strings.HasPrefix(cmd, "git-town sync"):
		*r.log = append(*r.log, "sync")
		return "", r.err
	case strings.Contains(cmd, "show-ref"):
		return "", errors.New("no such ref") // nothing left over to clean up
	case strings.Contains(cmd, "rev-parse"):
		return syncedSHA + "\n", nil
	}
	return "", nil
}

func drainStream(ch <-chan townie.StreamEvent) (lines []string, err error) {
	for ev := range ch {
		if ev.Done {
			return lines, ev.Err
		}
		lines = append(lines, ev.Line)
	}
	return lines, nil
}

func indexOf(log []string, want string) int {
	for i, s := range log {
		if s == want {
			return i
		}
	}
	return -1
}

// A stacked branch keeps the commits of the branch below it. Once that one is
// squash-merged, the same lines exist twice and GitHub refuses the next merge —
// so every branch must be synced onto the freshly-merged trunk before its turn.
func TestShipStackSyncsBeforeEachMergeAboveTheBottom(t *testing.T) {
	var log []string
	api := newFakeShipAPI(&log, "a", "b", "c")
	ops := townie.Ops{Runner: &syncRunner{log: &log}}

	_, err := drainStream(shipStackStream("o", "r", []string{"a", "b", "c"}, "main", ops, true, api))
	if err != nil {
		t.Fatalf("clean stack must ship whole: %v", err)
	}
	for _, b := range []string{"a", "b", "c"} {
		if indexOf(log, "merge:"+b) < 0 {
			t.Fatalf("branch %s never merged; log %v", b, log)
		}
	}
	for _, pair := range [][2]string{{"a", "b"}, {"b", "c"}} {
		lower, upper := "merge:"+pair[0], "merge:"+pair[1]
		synced := false
		for i := indexOf(log, lower); i < indexOf(log, upper); i++ {
			if log[i] == "sync" {
				synced = true
			}
		}
		if !synced {
			t.Errorf("no sync between %s and %s; log %v", lower, upper, log)
		}
	}
}

func TestShipStackStopsWhenTheNextBranchStillConflicts(t *testing.T) {
	var log []string
	api := newFakeShipAPI(&log, "a", "b")
	api.state["b"] = []gh.PRMergeSnapshot{{
		PRMergeability: gh.PRMergeability{Number: 101, Mergeable: "CONFLICTING"},
		HeadOid:        syncedSHA, BaseRef: "main",
	}}
	ops := townie.Ops{Runner: &syncRunner{log: &log}}

	_, err := drainStream(shipStackStream("o", "r", []string{"a", "b"}, "main", ops, true, api))
	if err == nil {
		t.Fatal("a conflicting branch must stop the run")
	}
	if indexOf(log, "merge:b") >= 0 {
		t.Errorf("must not attempt a merge known to conflict; log %v", log)
	}
	if !strings.Contains(err.Error(), "b") || !strings.Contains(err.Error(), "conflict") {
		t.Errorf("stop reason must name the branch and the conflict, got %q", err)
	}
}

func TestShipStackStopsWhenTheSyncFails(t *testing.T) {
	var log []string
	api := newFakeShipAPI(&log, "a", "b")
	ops := townie.Ops{Runner: &syncRunner{log: &log, err: errors.New("rebase conflict")}}

	_, err := drainStream(shipStackStream("o", "r", []string{"a", "b"}, "main", ops, true, api))
	if err == nil {
		t.Fatal("a failed sync must stop the run")
	}
	if indexOf(log, "merge:b") >= 0 {
		t.Errorf("must not merge a branch that could not be synced; log %v", log)
	}
	if indexOf(log, "merge:a") < 0 {
		t.Errorf("the branch that did land must still be merged; log %v", log)
	}
}

// Remote mode has no clone to sync, so a conflict there must explain WHY the
// branch above suddenly conflicts instead of surfacing GitHub's raw refusal.
func TestShipStackRemoteExplainsTheSquashedParent(t *testing.T) {
	var log []string
	api := newFakeShipAPI(&log, "a", "b")
	api.state["b"] = []gh.PRMergeSnapshot{{
		PRMergeability: gh.PRMergeability{Number: 101, Mergeable: "CONFLICTING"},
		BaseRef:        "main",
	}}
	ops := townie.Ops{Runner: &syncRunner{log: &log}}

	_, err := drainStream(shipStackStream("o", "r", []string{"a", "b"}, "main", ops, false, api))
	if err == nil {
		t.Fatal("a conflicting branch must stop the run")
	}
	if indexOf(log, "sync") >= 0 {
		t.Errorf("remote mode has no local tree to sync; log %v", log)
	}
	if !strings.Contains(err.Error(), "a") || !strings.Contains(err.Error(), "squash") {
		t.Errorf("must explain that the branch below was squashed, got %q", err)
	}
}

// GitHub keeps serving the verdict it computed for the PREVIOUS head for a moment
// after a push, so "conflicting" right after the sync may describe a branch that
// no longer exists. Trusting it stops a stack that was about to merge fine.
func TestShipStackWaitsOutAStaleConflictVerdict(t *testing.T) {
	wait := shipMergeStateWait
	shipMergeStateWait = 0
	defer func() { shipMergeStateWait = wait }()

	var log []string
	api := newFakeShipAPI(&log, "a", "b")
	api.state["b"] = []gh.PRMergeSnapshot{
		{PRMergeability: gh.PRMergeability{Number: 101, Mergeable: "CONFLICTING"}, HeadOid: "before-the-sync", BaseRef: "main"},
		{PRMergeability: gh.PRMergeability{Number: 101, Mergeable: "MERGEABLE"}, HeadOid: syncedSHA, BaseRef: "main"},
	}
	ops := townie.Ops{Runner: &syncRunner{log: &log}}

	_, err := drainStream(shipStackStream("o", "r", []string{"a", "b"}, "main", ops, true, api))
	if err != nil {
		t.Fatalf("a stale verdict must not stop the run: %v", err)
	}
	if indexOf(log, "merge:b") < 0 {
		t.Errorf("b must merge once GitHub answers for the pushed branch; log %v", log)
	}
}
