// Package stack reads a repository's git-town branch lineage — from git config,
// the source of truth that git-town writes — and builds an in-memory stack tree
// with drift detection. It is strictly read-only: Cairn never writes lineage,
// and every stack *mutation* is delegated to the git-town binary (Hard Rule 1).
package stack

import (
	"os/exec"
	"sort"
	"strings"
)

// Lineage is the git-town lineage recorded for a repo: the trunk branch plus
// each branch's recorded parent, and the set of perennial branches.
type Lineage struct {
	Trunk     string
	Parents   map[string]string // branch -> recorded parent
	Perennial map[string]bool
}

// Source records where a node's lineage came from: reconstructed from GitHub PR
// base/head chains (remote, works for any repo) or read from local git-town
// config (local, drift-aware).
type Source int

const (
	SourceRemote Source = iota
	SourceLocal
)

// Node is one branch in the stack tree.
type Node struct {
	Name      string
	Parent    string // "" for the trunk
	Depth     int    // 0 = trunk
	Children  []*Node
	IsTrunk   bool
	IsCurrent bool
	Drifted   bool   // recorded parent is not an ancestor — branch needs a restack
	Source    Source // remote (from PRs) or local (from git-town config)

	// PR association (set for remote nodes that have an open PR).
	HasPR    bool
	PRNumber int
	Review   string // gh.ReviewState value, kept as a string to avoid a gh import
	Checks   string // gh.CheckState value
}

// Tree is the built stack for a repo: the trunk root plus a pre-order flatten
// for rendering and selection.
type Tree struct {
	Repo    string // owner/name (remote stacks); "" for the local git-town tree
	Root    *Node
	Order   []*Node // DFS pre-order; trunk first
	Current string
}

// IndexOf returns the position of branch in Order, or -1.
func (t *Tree) IndexOf(branch string) int {
	for i, n := range t.Order {
		if n.Name == branch {
			return i
		}
	}
	return -1
}

// parseLineage turns the output of `git config --get-regexp '^git-town'` into a
// Lineage. Kept pure (no exec) so it is directly table-testable.
func parseLineage(configDump string) Lineage {
	lin := Lineage{Parents: map[string]string{}, Perennial: map[string]bool{}}
	for _, line := range strings.Split(configDump, "\n") {
		key, val, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		val = strings.TrimSpace(val)
		switch {
		case key == "git-town.main-branch":
			lin.Trunk = val
		case key == "git-town.perennial-branches":
			for _, b := range strings.Fields(val) {
				lin.Perennial[b] = true
			}
		case strings.HasPrefix(key, "git-town-branch.") && strings.HasSuffix(key, ".parent"):
			name := strings.TrimSuffix(strings.TrimPrefix(key, "git-town-branch."), ".parent")
			if name != "" && val != "" {
				lin.Parents[name] = val
			}
		}
	}
	return lin
}

// BuildTree assembles the stack tree rooted at the trunk. drift, when non-nil,
// is called for each non-trunk branch to flag lineage drift; injecting it keeps
// the tree-building logic testable without git. Returns nil when there is no
// trunk (i.e. the repo has no git-town configuration).
func BuildTree(lin Lineage, current string, drift func(branch, parent string) bool) *Tree {
	if lin.Trunk == "" {
		return nil
	}
	children := map[string][]string{}
	for b, p := range lin.Parents {
		children[p] = append(children[p], b)
	}
	for p := range children {
		sort.Strings(children[p])
	}

	var order []*Node
	visited := map[string]bool{}
	var build func(name, parent string, depth int) *Node
	build = func(name, parent string, depth int) *Node {
		visited[name] = true // guard against malformed cyclic lineage
		n := &Node{
			Name: name, Parent: parent, Depth: depth,
			IsTrunk: parent == "", IsCurrent: name == current,
			Source: SourceLocal,
		}
		if parent != "" && drift != nil {
			n.Drifted = drift(name, parent)
		}
		order = append(order, n)
		for _, c := range children[name] {
			if !visited[c] {
				n.Children = append(n.Children, build(c, name, depth+1))
			}
		}
		return n
	}
	root := build(lin.Trunk, "", 0)
	return &Tree{Root: root, Order: order, Current: current}
}

// PRRef is the minimal PR data needed to reconstruct a stack from GitHub: a PR
// links its head branch (the node) to its base branch (the parent).
type PRRef struct {
	Repo   string
	Number int
	Head   string
	Base   string
	Review string
	Checks string
}

// BuildRemoteStacks reconstructs stacks purely from PR base/head chains — no
// local clone needed. It returns one Tree per connected chain within each repo:
// a branch's parent is its PR's base, and a trunk is any base that no PR in the
// set uses as a head (e.g. main). PR metadata is attached to each head node.
func BuildRemoteStacks(prs []PRRef) []*Tree {
	byRepo := map[string][]PRRef{}
	repoOrder := []string{}
	for _, p := range prs {
		if _, seen := byRepo[p.Repo]; !seen {
			repoOrder = append(repoOrder, p.Repo)
		}
		byRepo[p.Repo] = append(byRepo[p.Repo], p)
	}
	var trees []*Tree
	for _, repo := range repoOrder {
		trees = append(trees, buildRepoStacks(byRepo[repo])...)
	}
	return trees
}

func buildRepoStacks(prs []PRRef) []*Tree {
	headSet := map[string]bool{}
	prByHead := map[string]PRRef{}
	children := map[string][]string{} // base -> head branches
	for _, p := range prs {
		headSet[p.Head] = true
		prByHead[p.Head] = p
		children[p.Base] = append(children[p.Base], p.Head)
	}
	for b := range children {
		sort.Strings(children[b])
	}
	// Roots are bases that aren't themselves a PR head — the trunk(s).
	rootSet := map[string]bool{}
	for _, p := range prs {
		if !headSet[p.Base] {
			rootSet[p.Base] = true
		}
	}
	var roots []string
	for r := range rootSet {
		roots = append(roots, r)
	}
	sort.Strings(roots)

	var trees []*Tree
	for _, root := range roots {
		var order []*Node
		visited := map[string]bool{}
		var build func(branch, parent string, depth int) *Node
		build = func(branch, parent string, depth int) *Node {
			visited[branch] = true
			n := &Node{
				Name: branch, Parent: parent, Depth: depth,
				IsTrunk: parent == "", Source: SourceRemote,
			}
			if pr, ok := prByHead[branch]; ok {
				n.HasPR = true
				n.PRNumber = pr.Number
				n.Review = pr.Review
				n.Checks = pr.Checks
			}
			order = append(order, n)
			for _, c := range children[branch] {
				if !visited[c] {
					n.Children = append(n.Children, build(c, branch, depth+1))
				}
			}
			return n
		}
		repo := ""
		if len(prs) > 0 {
			repo = prs[0].Repo
		}
		trees = append(trees, &Tree{Repo: repo, Root: build(root, "", 0), Order: order})
	}
	return trees
}

// FindStack returns the tree whose branches include name, or nil.
func FindStack(trees []*Tree, name string) *Tree {
	for _, t := range trees {
		if t.IndexOf(name) >= 0 {
			return t
		}
	}
	return nil
}

// FindStackInRepo is FindStack scoped to a repo, so identically-named branches
// in different repos don't collide.
func FindStackInRepo(trees []*Tree, repo, name string) *Tree {
	for _, t := range trees {
		if t.Repo == repo && t.IndexOf(name) >= 0 {
			return t
		}
	}
	return nil
}

// NodeByName returns the node for branch name, or nil.
func (t *Tree) NodeByName(name string) *Node {
	if t == nil {
		return nil
	}
	for _, n := range t.Order {
		if n.Name == name {
			return n
		}
	}
	return nil
}

// Focused returns the lineage relevant to branch: its ancestor chain from the
// trunk down to it, then everything stacked on top of it (its descendants) in
// DFS order. Unrelated branches that merely share the trunk are excluded — so
// selecting a lone PR shows just "trunk → that PR", not every sibling.
func (t *Tree) Focused(branch string) []*Node {
	target := t.NodeByName(branch)
	if target == nil {
		return nil
	}
	// Walk up to the trunk, then reverse → trunk..target.
	var chain []*Node
	for n := target; n != nil; n = t.NodeByName(n.Parent) {
		chain = append(chain, n)
		if n.Parent == "" {
			break
		}
	}
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	out := append([]*Node{}, chain...)
	var dfs func(n *Node)
	dfs = func(n *Node) {
		for _, c := range n.Children {
			out = append(out, c)
			dfs(c)
		}
	}
	dfs(target)
	return out
}

// Load reads the lineage for the repo at dir (cwd if empty) and builds the tree
// with real drift detection. Returns (nil, nil) when the repo has no git-town
// stack configured.
func Load(dir string) (*Tree, error) {
	dump, err := gitConfigDump(dir)
	if err != nil {
		return nil, err
	}
	lin := parseLineage(dump)
	if lin.Trunk == "" {
		return nil, nil
	}
	cur := currentBranch(dir)
	drift := func(branch, parent string) bool { return driftedFromParent(dir, branch, parent) }
	return BuildTree(lin, cur, drift), nil
}

// gitConfigDump returns all git-town config entries ("key value" per line). A
// missing match (git exit code 1) is not an error — it just means no config.
func gitConfigDump(dir string) (string, error) {
	cmd := exec.Command("git", "config", "--get-regexp", `^git-town`)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return "", nil // no matching keys
		}
		return "", err
	}
	return string(out), nil
}

func currentBranch(dir string) string {
	cmd := exec.Command("git", "branch", "--show-current")
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// DetectTrunk makes a best-effort guess at the repo's trunk branch, for
// pre-filling the git-town init prompt. Order: the remote's default branch
// (origin/HEAD), then a local main, then master, then the current branch. Returns
// "" only when none of those exist. The user can still override the guess.
func DetectTrunk(dir string) string {
	if b := trunkFromSymbolicRef(gitOutput(dir, "symbolic-ref", "--short", "refs/remotes/origin/HEAD")); b != "" {
		return b
	}
	for _, cand := range []string{"main", "master"} {
		if localBranchExists(dir, cand) {
			return cand
		}
	}
	return currentBranch(dir)
}

// LatestCommitSubject returns the first line of branch's most recent commit
// message (dir is cwd if empty), for pre-filling a proposed PR's title. Returns
// "" when the branch has no commits or git can't be reached.
func LatestCommitSubject(dir, branch string) string {
	return gitOutput(dir, "log", "-1", "--format=%s", branch)
}

// trunkFromSymbolicRef turns "origin/main" (the short form of origin/HEAD) into
// "main". Pure, so the stripping is table-testable.
func trunkFromSymbolicRef(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	if i := strings.IndexByte(ref, '/'); i >= 0 {
		return ref[i+1:]
	}
	return ref
}

func localBranchExists(dir, branch string) bool {
	cmd := exec.Command("git", "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	if dir != "" {
		cmd.Dir = dir
	}
	return cmd.Run() == nil
}

// gitOutput runs a git command and returns trimmed stdout, or "" on error.
func gitOutput(dir string, args ...string) string {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// driftedFromParent reports whether branch has drifted off its recorded parent:
// true when the parent's tip is NOT an ancestor of the branch (so the branch is
// no longer stacked on top of it and needs a restack). Uses git plumbing only —
// no rebase logic (Hard Rule 1).
func driftedFromParent(dir, branch, parent string) bool {
	cmd := exec.Command("git", "merge-base", "--is-ancestor", parent, branch)
	if dir != "" {
		cmd.Dir = dir
	}
	// Exit 0 = parent is an ancestor (properly stacked). Non-zero = drifted.
	return cmd.Run() != nil
}
