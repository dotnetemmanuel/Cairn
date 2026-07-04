package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/dotnetemmanuel/cairn/internal/gh"
	"github.com/dotnetemmanuel/cairn/internal/stack"
	"github.com/dotnetemmanuel/cairn/internal/theme"
)

// remoteFixture builds a stack model in remote mode with two orgs' reconstructed
// stacks: acme/web (main → feat-a → feat-b) and beta/api (main → fix-x).
func remoteFixture() stackModel {
	th := theme.New(theme.DefaultPalette())
	s := newStackModelBare(th, "o/local")
	refs := []stack.PRRef{
		{Repo: "acme/web", Number: 1, Head: "feat-a", Base: "main"},
		{Repo: "acme/web", Number: 2, Head: "feat-b", Base: "feat-a"},
		{Repo: "beta/api", Number: 3, Head: "fix-x", Base: "main"},
	}
	s.remoteStacks = stack.BuildRemoteStacks(refs)
	s.remote = true
	return s
}

func TestRemoteChooserGroupsByOrgWithCounts(t *testing.T) {
	s := remoteFixture()
	list := s.remoteChooserList()
	if len(list) != 2 {
		t.Fatalf("want 2 repos, got %d: %+v", len(list), list)
	}
	// Sorted by owner: acme before beta; PR counts exclude the trunk.
	if list[0].repo != "acme/web" || list[0].prs != 2 {
		t.Errorf("first entry = %+v, want acme/web with 2 PRs", list[0])
	}
	if list[1].repo != "beta/api" || list[1].prs != 1 {
		t.Errorf("second entry = %+v, want beta/api with 1 PR", list[1])
	}
}

func TestRemoteShipChainAndBottom(t *testing.T) {
	s := remoteFixture()
	s.remoteRepo = "acme/web"
	trunk, chain := s.remoteShipChain("feat-b")
	if trunk != "main" {
		t.Errorf("trunk = %q, want main", trunk)
	}
	if len(chain) != 2 || chain[0] != "feat-a" || chain[1] != "feat-b" {
		t.Errorf("chain = %v, want [feat-a feat-b] (bottom-up)", chain)
	}
	tr := s.remoteTreeOf("feat-a")
	if !s.remoteIsBottom(tr.NodeByName("feat-a")) {
		t.Error("feat-a (child of trunk) should be the bottom")
	}
	if s.remoteIsBottom(tr.NodeByName("feat-b")) {
		t.Error("feat-b (mid-stack) must not be the bottom")
	}
}

func TestRemoteShipGating(t *testing.T) {
	s := remoteFixture()
	s.remoteRepo = "acme/web"
	s.remoteMerge = map[string]gh.PRMergeability{
		"feat-a": {Number: 1, Mergeable: "MERGEABLE"},
		"feat-b": {Number: 2, Mergeable: "MERGEABLE"},
	}
	tr := s.remoteTreeOf("feat-a")
	// M only on the bottom; G from either branch (it lands the chain from the bottom).
	if !s.remoteShipEnabled(tr.NodeByName("feat-a")) {
		t.Error("M should be enabled on the bottom feat-a")
	}
	if s.remoteShipEnabled(tr.NodeByName("feat-b")) {
		t.Error("M must be disabled on mid-stack feat-b (use G)")
	}
	if !s.remoteShipStackEnabled(tr.NodeByName("feat-b")) {
		t.Error("G should be enabled on feat-b")
	}
	// A draft bottom blocks both M and G, and enables Y on that branch.
	s.remoteMerge["feat-a"] = gh.PRMergeability{Number: 1, Draft: true}
	if s.remoteShipEnabled(tr.NodeByName("feat-a")) {
		t.Error("a draft bottom must block M")
	}
	if s.remoteShipStackEnabled(tr.NodeByName("feat-b")) {
		t.Error("a draft bottom must block G")
	}
	if !s.remoteReadyEnabled(tr.NodeByName("feat-a")) {
		t.Error("Y should be enabled on the draft bottom")
	}
	if s.remoteReadyEnabled(tr.NodeByName("feat-b")) {
		t.Error("Y must be disabled on a non-draft branch")
	}
}

func TestRemoteToggleEntersChooser(t *testing.T) {
	s := fixtureModel() // local
	s2, _ := s.updateBrowsing(tea.KeyMsg{Type: tea.KeyCtrlR})
	if !s2.remote || s2.remoteRepo != "" {
		t.Fatalf("ctrl+r from local should enter the remote chooser, got remote=%v repo=%q", s2.remote, s2.remoteRepo)
	}
	// ctrl+r again toggles back to local.
	s3, _ := s2.updateRemote(tea.KeyMsg{Type: tea.KeyCtrlR})
	if s3.remote {
		t.Error("ctrl+r from remote should return to local")
	}
}

func TestRemoteMOpensShipConfirmAndRuns(t *testing.T) {
	s := remoteFixture()
	s.remoteRepo = "acme/web"
	s.remoteMerge = map[string]gh.PRMergeability{
		"feat-a": {Number: 1, Mergeable: "MERGEABLE"},
		"feat-b": {Number: 2, Mergeable: "MERGEABLE"},
	}
	for i, n := range s.remoteNodes() {
		if n.Name == "feat-a" {
			s.remoteCursor = i
		}
	}
	s2, _ := s.updateRemoteTree(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("M")})
	if s2.phase != stackConfirming || s2.pending.Verb != "ship" || s2.opName != "feat-a" {
		t.Fatalf("M should open the remote ship confirm on feat-a, got phase=%d verb=%q op=%q", s2.phase, s2.pending.Verb, s2.opName)
	}
	s3, cmd := s2.updateConfirming(tea.KeyMsg{Type: tea.KeyEnter})
	if s3.phase != stackRunning || cmd == nil {
		t.Errorf("enter should run the remote ship: phase=%d cmd=%v", s3.phase, cmd != nil)
	}
}

func TestRemoteRepoMsgReplacesReposTrees(t *testing.T) {
	s := remoteFixture()
	s.remoteRepo = "acme/web"
	// Simulate a ship of feat-a: only feat-b remains, now based on main.
	refs := []stack.PRRef{{Repo: "acme/web", Number: 2, Head: "feat-b", Base: "main"}}
	s2, _ := s.Update(remoteRepoMsg{
		repo:  "acme/web",
		trees: stack.BuildRemoteStacks(refs),
		merge: map[string]gh.PRMergeability{"feat-b": {Number: 2, Mergeable: "MERGEABLE"}},
	})
	for _, n := range s2.remoteNodes() {
		if n.Name == "feat-a" {
			t.Error("feat-a should be gone from the tree after the post-ship refresh")
		}
	}
	if s2.remoteTreeOf("feat-b") == nil {
		t.Error("feat-b should still be present after the refresh")
	}
	// The other org's repo is untouched by an acme/web refresh.
	prevRepo := s2.remoteRepo
	s2.remoteRepo = "beta/api"
	if s2.remoteTreeOf("fix-x") == nil {
		t.Error("beta/api trees must survive an acme/web refresh")
	}
	s2.remoteRepo = prevRepo
}

func TestRemoteRendersDoNotPanic(t *testing.T) {
	s := remoteFixture()
	s.width, s.height = 120, 40
	// Chooser.
	if out := s.renderRemoteChooser(100); !containsAll(out, "acme", "beta", "choose") {
		t.Errorf("chooser missing orgs/intro:\n%s", out)
	}
	// Chosen repo: tree + actions + confirms.
	s.remoteRepo = "acme/web"
	s.remoteMerge = map[string]gh.PRMergeability{
		"feat-a": {Number: 1, Mergeable: "MERGEABLE"},
		"feat-b": {Number: 2, Mergeable: "MERGEABLE"},
	}
	for i, n := range s.remoteNodes() {
		if n.Name == "feat-a" {
			s.remoteCursor = i
		}
	}
	_ = s.renderRemoteTree(40)
	_ = s.renderRemoteActions(60)
	s.opName = "feat-a"
	if out := s.renderRemoteShipConfirm(80); !containsAll(out, "feat-a", "retarget") {
		t.Errorf("ship confirm missing content:\n%s", out)
	}
	_, chain := s.remoteShipChain("feat-b")
	s.opName, s.affected = "feat-b", chain
	if out := s.renderRemoteShipStackConfirm(80); !containsAll(out, "feat-a", "feat-b") {
		t.Errorf("ship-stack confirm missing branches:\n%s", out)
	}
	// Full remote body render (chooser + chosen).
	_ = s.renderRemote(120, 30)
	s.remoteRepo = ""
	_ = s.renderRemote(120, 30)
}

// containsAll reports whether every substring appears in s, case-insensitively.
func containsAll(s string, subs ...string) bool {
	low := strings.ToLower(s)
	for _, sub := range subs {
		if !strings.Contains(low, strings.ToLower(sub)) {
			return false
		}
	}
	return true
}
