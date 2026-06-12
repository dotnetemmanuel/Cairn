// Package townie is Cairn's thin delegation layer over the git-town binary (and
// a little git). It writes NO lineage and contains NO rebase/merge-base logic —
// every stack mutation shells out to git-town, the single source of stack
// mechanics (Hard Rule 1). The Command catalog here drives both the keybindings
// and the pedagogical help overlay, so the two never drift apart.
package townie

import (
	"os/exec"
	"strings"
)

// Runner executes an external command in a working directory and returns its
// combined stdout+stderr. It's an interface so the op layer is unit-testable
// without invoking real git-town.
type Runner interface {
	Run(dir, name string, args ...string) (string, error)
}

// ExecRunner is the production Runner: it actually shells out.
type ExecRunner struct{}

func (ExecRunner) Run(dir, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// Ops issues stack operations for one repo directory.
type Ops struct {
	Dir    string
	Runner Runner
}

// New builds Ops for dir using the real exec Runner.
func New(dir string) Ops { return Ops{Dir: dir, Runner: ExecRunner{}} }

// argv returns the exact command a verb runs, so the help overlay can show it
// and Run can execute it from one source of truth.
func argv(verb, name string) []string {
	switch verb {
	case "new":
		return []string{"git-town", "append", name}
	case "insert":
		return []string{"git-town", "prepend", name}
	case "hack":
		return []string{"git-town", "hack", name}
	case "checkout":
		// Not a stack mutation — just moves HEAD. Kept out of Catalog() so it
		// isn't listed as a git-town stack command, but routed through Run so the
		// stack mode reuses one execution path.
		return []string{"git", "checkout", name}
	case "sync":
		return []string{"git-town", "sync", "--stack"}
	case "restack":
		return []string{"git-town", "sync", "--stack", "--no-push"}
	case "amend":
		// Two steps; the first is git, the second restacks descendants. Run()
		// special-cases this verb.
		return []string{"git", "commit", "--amend", "--no-edit"}
	default:
		return nil
	}
}

// Run executes the given verb (with an optional branch name) and returns the
// combined output. amend is a two-step sequence (amend, then restack); init is a
// two-step sequence that writes git-town's config keys (name = the trunk branch).
func (o Ops) Run(verb, name string) (string, error) {
	if verb == "amend" {
		out, err := o.exec(argv("amend", ""))
		if err != nil {
			return out, err
		}
		rest, rerr := o.exec(argv("restack", ""))
		return strings.TrimRight(out, "\n") + "\n" + rest, rerr
	}
	if verb == "init" {
		return o.runSeq(InitArgv(name))
	}
	a := argv(verb, name)
	if a == nil {
		return "", &UnknownVerbError{Verb: verb}
	}
	return o.exec(a)
}

// runSeq runs each command in order, accumulating output, and stops at the first
// error (so a failed first step never runs the second).
func (o Ops) runSeq(cmds [][]string) (string, error) {
	var out []string
	for _, a := range cmds {
		s, err := o.exec(a)
		out = append(out, strings.TrimRight(s, "\n"))
		if err != nil {
			return strings.Join(out, "\n"), err
		}
	}
	return strings.Join(out, "\n"), nil
}

// InitArgv is the command sequence that initializes git-town for a repo: it marks
// the trunk branch and standardizes on rebase syncing (right for stacked PRs).
// git-town 23 has no non-interactive config setter — its own `init` is a
// full-screen wizard — so Cairn writes the exact git-town.* keys git-town reads
// from local .git/config. These touch only local config: nothing is committed,
// staged, or pushed, and no tracked file is created.
func InitArgv(trunk string) [][]string {
	return [][]string{
		{"git", "config", "git-town.main-branch", trunk},
		{"git", "config", "git-town.sync-feature-strategy", "rebase"},
	}
}

func (o Ops) exec(a []string) (string, error) {
	return o.Runner.Run(o.Dir, a[0], a[1:]...)
}

// UnknownVerbError is returned for an unrecognized verb.
type UnknownVerbError struct{ Verb string }

func (e *UnknownVerbError) Error() string { return "unknown stack verb: " + e.Verb }

// Command describes one stack command for the help overlay and key dispatch.
type Command struct {
	Key       string // TUI key that triggers it
	Verb      string // internal verb (matches argv)
	Title     string // short label, e.g. "new"
	Short     string // one-line summary
	Long      string // pedagogical explanation for git-town newcomers
	NeedsName bool   // true if it prompts for a branch name
	Mutates   bool   // true if it changes the repo (needs confirmation)
}

// CommandHint shows the underlying git-town invocation for a command, e.g.
// "git-town append <name>" — handy in the help overlay.
func (c Command) Hint() string {
	a := argv(c.Verb, "<name>")
	if a == nil {
		return ""
	}
	return strings.Join(a, " ")
}

// Catalog is the ordered set of stack commands. It is the single source of truth
// for keybindings, dispatch, and the help overlay.
func Catalog() []Command {
	return []Command{
		{
			Key: "n", Verb: "new", Title: "new", NeedsName: true, Mutates: true,
			Short: "stack a new branch on top of this one",
			Long: "Creates a new feature branch as a CHILD of the current branch — " +
				"the next layer of the stack. When you propose it, its PR will target " +
				"the current branch, not main. Use this when your next change builds on " +
				"the one you're on.",
		},
		{
			Key: "I", Verb: "insert", Title: "insert", NeedsName: true, Mutates: true,
			Short: "wedge a new branch below the current one",
			Long: "Creates a new, EMPTY branch as the PARENT of the current branch, wedging " +
				"a slot UNDERNEATH it. Nothing moves yet — git-town just re-parents the " +
				"current branch (and everything above) onto the new slot. Commit your " +
				"prerequisite there, then sync to flow it up. Use this when you realize a " +
				"lower change is missing.",
		},
		{
			Key: "S", Verb: "sync", Title: "sync", Mutates: true,
			Short: "pull trunk + rebase the whole stack, then push",
			Long: "Fetches the latest trunk (main) and rebases the whole stack back onto " +
				"it — the base onto trunk, each branch above onto its parent — deletes " +
				"local branches whose remote is gone (e.g. after a PR merges), and pushes " +
				"the results. This is how you keep a stack current with main and with each " +
				"other. (Cairn assumes git-town's rebase sync strategy.)",
		},
		{
			Key: "R", Verb: "restack", Title: "restack", Mutates: true,
			Short: "re-rebase the stack without pushing",
			Long: "A full stack sync WITHOUT the push: fetches the latest trunk and " +
				"rebases the stack locally — the base onto trunk, each branch onto its " +
				"parent. Use it to clear a drift (amber ⚠) after you've amended or " +
				"rewritten a lower branch, before you're ready to push.",
		},
		{
			Key: "A", Verb: "amend", Title: "amend", Mutates: true,
			Short: "fold staged changes into this branch, then restack",
			Long: "Folds your currently-staged changes into the current branch's LATEST " +
				"commit (git commit --amend), then restacks the branches above so they sit " +
				"on the updated commit. Use it to revise a change already in the stack.",
		},
	}
}

// Find returns the catalog command for a key, or nil.
func Find(key string) *Command {
	for i := range catalog {
		if catalog[i].Key == key {
			return &catalog[i]
		}
	}
	return nil
}

var catalog = Catalog()
