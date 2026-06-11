package stack

import "testing"

func TestParseLineage(t *testing.T) {
	dump := `git-town.main-branch main
git-town.offline true
git-town.perennial-branches dev staging
git-town-branch.feat-base.parent main
git-town-branch.feat-mid.parent feat-base
git-town-branch.feature/with-slash.parent feat-mid`

	lin := parseLineage(dump)
	if lin.Trunk != "main" {
		t.Errorf("trunk = %q, want main", lin.Trunk)
	}
	if lin.Parents["feat-base"] != "main" || lin.Parents["feat-mid"] != "feat-base" {
		t.Errorf("parents wrong: %+v", lin.Parents)
	}
	if lin.Parents["feature/with-slash"] != "feat-mid" {
		t.Errorf("branch names with slashes should parse: %+v", lin.Parents)
	}
	if !lin.Perennial["dev"] || !lin.Perennial["staging"] {
		t.Errorf("perennial set wrong: %+v", lin.Perennial)
	}
	if _, ok := lin.Parents["offline"]; ok {
		t.Error("non-lineage keys must not leak into Parents")
	}
}

func TestParseLineageEmpty(t *testing.T) {
	if lin := parseLineage(""); lin.Trunk != "" || len(lin.Parents) != 0 {
		t.Errorf("empty config should yield empty lineage, got %+v", lin)
	}
}

func TestBuildTreeShapeAndOrder(t *testing.T) {
	lin := Lineage{
		Trunk: "main",
		Parents: map[string]string{
			"feat-base": "main",
			"feat-mid":  "feat-base",
			"feat-top":  "feat-mid",
			"side":      "main", // a second branch off trunk
		},
	}
	tree := BuildTree(lin, "feat-mid", nil)
	if tree == nil {
		t.Fatal("expected a tree")
	}
	if tree.Root.Name != "main" || !tree.Root.IsTrunk {
		t.Fatalf("root should be trunk main, got %+v", tree.Root)
	}
	// Pre-order DFS with sorted children: main, feat-base, feat-mid, feat-top, side.
	want := []string{"main", "feat-base", "feat-mid", "feat-top", "side"}
	if len(tree.Order) != len(want) {
		t.Fatalf("order length = %d, want %d (%v)", len(tree.Order), len(want), names(tree.Order))
	}
	for i, w := range want {
		if tree.Order[i].Name != w {
			t.Errorf("order[%d] = %s, want %s (full: %v)", i, tree.Order[i].Name, w, names(tree.Order))
		}
	}
	// Depth + current marker.
	if tree.Order[2].Name == "feat-mid" && (!tree.Order[2].IsCurrent || tree.Order[2].Depth != 2) {
		t.Errorf("feat-mid: current=%v depth=%d, want current=true depth=2", tree.Order[2].IsCurrent, tree.Order[2].Depth)
	}
}

func TestBuildTreeFlagsDrift(t *testing.T) {
	lin := Lineage{
		Trunk:   "main",
		Parents: map[string]string{"feat-base": "main", "feat-mid": "feat-base"},
	}
	// Pretend feat-mid has drifted off feat-base.
	drift := func(branch, parent string) bool { return branch == "feat-mid" }
	tree := BuildTree(lin, "", drift)

	byName := map[string]*Node{}
	for _, n := range tree.Order {
		byName[n.Name] = n
	}
	if byName["feat-mid"] == nil || !byName["feat-mid"].Drifted {
		t.Error("feat-mid should be flagged drifted")
	}
	if byName["feat-base"].Drifted {
		t.Error("feat-base should not be drifted")
	}
	if byName["main"].Drifted {
		t.Error("trunk is never drifted (no parent)")
	}
}

func TestBuildTreeNoTrunk(t *testing.T) {
	if tree := BuildTree(Lineage{Parents: map[string]string{"x": "y"}}, "", nil); tree != nil {
		t.Error("no trunk should yield a nil tree")
	}
}

func TestBuildTreeCyclicGuard(t *testing.T) {
	// Malformed lineage with a cycle must not hang or panic.
	lin := Lineage{Trunk: "main", Parents: map[string]string{
		"a": "main", "b": "a", "a2": "b",
	}}
	// inject a fake cycle: make "main"'s child point back via a manual children loop
	lin.Parents["loop"] = "loop"
	tree := BuildTree(lin, "", nil)
	if tree == nil {
		t.Fatal("expected a tree")
	}
}

func names(ns []*Node) []string {
	out := make([]string, len(ns))
	for i, n := range ns {
		out[i] = n.Name
	}
	return out
}

func TestBuildRemoteStacksFromPRChain(t *testing.T) {
	// A 3-PR stack in one repo, plus a lone PR in another.
	prs := []PRRef{
		{Repo: "o/r", Number: 14, Head: "feat-top", Base: "feat-mid"},
		{Repo: "o/r", Number: 13, Head: "feat-mid", Base: "feat-base"},
		{Repo: "o/r", Number: 12, Head: "feat-base", Base: "main"},
		{Repo: "o/other", Number: 7, Head: "fix", Base: "main"},
	}
	trees := BuildRemoteStacks(prs)

	st := FindStack(trees, "feat-mid")
	if st == nil {
		t.Fatal("expected to find the stack containing feat-mid")
	}
	want := []string{"main", "feat-base", "feat-mid", "feat-top"}
	if len(st.Order) != len(want) {
		t.Fatalf("stack order = %v, want %v", names(st.Order), want)
	}
	for i, w := range want {
		if st.Order[i].Name != w {
			t.Errorf("order[%d] = %s, want %s", i, st.Order[i].Name, w)
		}
	}
	// Trunk has no PR; head branches carry their PR number + remote source.
	if st.Order[0].HasPR || !st.Order[0].IsTrunk {
		t.Error("main should be a PR-less trunk")
	}
	mid := st.Order[2]
	if !mid.HasPR || mid.PRNumber != 13 || mid.Source != SourceRemote {
		t.Errorf("feat-mid node wrong: %+v", mid)
	}
	// The other repo's lone PR is its own stack.
	if other := FindStack(trees, "fix"); other == nil || len(other.Order) != 2 {
		t.Errorf("expected o/other lone-PR stack of depth 2, got %v", other)
	}
}

func TestBuildRemoteStacksIgnoresUnrelatedRepoLinks(t *testing.T) {
	// Same branch names in different repos must not cross-link.
	prs := []PRRef{
		{Repo: "a/x", Number: 1, Head: "feat", Base: "main"},
		{Repo: "b/y", Number: 2, Head: "feat", Base: "main"},
	}
	trees := BuildRemoteStacks(prs)
	if len(trees) != 2 {
		t.Fatalf("expected 2 separate trees, got %d", len(trees))
	}
}

func TestFocusedShowsOnlyRelevantLineage(t *testing.T) {
	// One repo, all branches off main: a real stack (base→mid→top) plus two
	// lone PRs. Selecting a lone PR must not drag in the others.
	prs := []PRRef{
		{Repo: "o/r", Number: 6, Head: "feat-base", Base: "main"},
		{Repo: "o/r", Number: 7, Head: "feat-mid", Base: "feat-base"},
		{Repo: "o/r", Number: 8, Head: "feat-top", Base: "feat-mid"},
		{Repo: "o/r", Number: 3, Head: "lone-a", Base: "main"},
		{Repo: "o/r", Number: 4, Head: "lone-b", Base: "main"},
	}
	tree := BuildRemoteStacks(prs)[0]

	// A lone PR → just trunk → itself.
	if got := names(tree.Focused("lone-a")); len(got) != 2 || got[0] != "main" || got[1] != "lone-a" {
		t.Errorf("lone-a focused = %v, want [main lone-a]", got)
	}
	// A mid-stack branch → full chain + its descendants, no lone siblings.
	want := []string{"main", "feat-base", "feat-mid", "feat-top"}
	got := names(tree.Focused("feat-mid"))
	if len(got) != len(want) {
		t.Fatalf("feat-mid focused = %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("focused[%d] = %s, want %s", i, got[i], w)
		}
	}
}
