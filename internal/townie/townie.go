// Package townie is Cairn's thin delegation layer over the git-town binary (and
// a little git). It writes NO lineage and contains NO rebase/merge-base logic —
// every stack mutation shells out to git-town, the single source of stack
// mechanics (Hard Rule 1). The Command catalog here drives both the keybindings
// and the pedagogical help overlay, so the two never drift apart.
package townie

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// nonInteractiveEnv forces git/git-town to never open an editor or pager, so a
// `continue` that wants to amend a commit message (or a sync that would page)
// proceeds unattended inside the TUI instead of blocking on a terminal we don't
// own.
func nonInteractiveEnv() []string {
	return append(os.Environ(),
		"GIT_EDITOR=true",
		"GIT_SEQUENCE_EDITOR=true",
		"GIT_PAGER=cat",
		"GIT_TERMINAL_PROMPT=0",
	)
}

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
	cmd.Env = nonInteractiveEnv()
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
	case "push":
		// Publish a branch to origin so a PR can be opened against it. Works on a
		// branch that isn't checked out (refs/heads/<name>). Out of Catalog() — it's
		// a step inside propose, not a stack-authoring verb of its own.
		return []string{"git", "push", "-u", "origin", name}
	case "sync":
		return []string{"git-town", "sync", "--stack"}
	case "restack":
		return []string{"git-town", "sync", "--stack", "--no-push"}
	case "amend":
		// Two steps; the first is git, the second restacks descendants. Run()
		// special-cases this verb.
		return []string{"git", "commit", "--amend", "--no-edit"}
	case "continue":
		// Resume a sync/rebase paused on a conflict (after the resolver staged the
		// fixes). Kept out of Catalog() — not a stack-authoring verb. ExecRunner
		// forces a non-interactive editor so it never blocks waiting for one.
		return []string{"git-town", "continue"}
	case "undo":
		// Roll the whole in-progress operation back (the conflict resolver's
		// "abort all"). Also out of Catalog().
		return []string{"git-town", "undo"}
	default:
		return nil
	}
}

// plan returns the ordered command(s) a verb runs. Most verbs are a single
// command; amend (amend then restack) and init (two config writes) are
// multi-step. Shared by Run (mock-testable) and Stream (live output).
func plan(verb, name string) ([][]string, error) {
	switch verb {
	case "amend":
		return [][]string{argv("amend", ""), argv("restack", "")}, nil
	case "init":
		return InitArgv(name), nil
	default:
		a := argv(verb, name)
		if a == nil {
			return nil, &UnknownVerbError{Verb: verb}
		}
		return [][]string{a}, nil
	}
}

// Run executes the given verb (with an optional branch name) and returns the
// combined output. amend is a two-step sequence (amend, then restack); init is a
// two-step sequence that writes git-town's config keys (name = the trunk branch).
func (o Ops) Run(verb, name string) (string, error) {
	cmds, err := plan(verb, name)
	if err != nil {
		return "", err
	}
	return o.runSeq(cmds)
}

// StreamEvent is one unit of streamed output: a Line as it is produced, or the
// terminal completion event (Done, with Err set if a step failed).
type StreamEvent struct {
	Line string
	Done bool
	Err  error
}

// StreamRunner is an optional Runner capability: stream a command's combined
// output line-by-line via emit instead of returning it all at once. ExecRunner
// implements it; a plain Runner (e.g. a test mock) need only implement Run, and
// Stream falls back to running it and emitting the captured output.
type StreamRunner interface {
	Stream(dir, name string, args []string, emit func(line string)) error
}

// Stream runs the verb's command(s) and emits each output line on the returned
// channel as it arrives, then a single {Done:true} event (Err set on failure;
// remaining steps are skipped after an error). It shares the verb→command mapping
// with Run via plan, and routes execution through the Runner (so tests still
// observe the commands). The channel is closed after the Done event.
func (o Ops) Stream(verb, name string) <-chan StreamEvent {
	cmds, err := plan(verb, name)
	if err != nil {
		ch := make(chan StreamEvent, 1)
		ch <- StreamEvent{Done: true, Err: err}
		close(ch)
		return ch
	}
	return o.streamCmds(cmds)
}

// streamCmds runs each command in order, streaming its output line-by-line, then a
// single {Done:true} (Err set on the first failure; remaining steps are skipped).
// Shared by Stream (verb-driven) and the one-off ops like SetParent.
func (o Ops) streamCmds(cmds [][]string) <-chan StreamEvent {
	ch := make(chan StreamEvent, 64)
	go func() {
		defer close(ch)
		sr, live := o.Runner.(StreamRunner)
		for _, a := range cmds {
			var err error
			if live {
				err = sr.Stream(o.Dir, a[0], a[1:], func(line string) {
					ch <- StreamEvent{Line: line}
				})
			} else {
				// Non-streaming Runner (tests): run it, emit the captured output.
				var out string
				out, err = o.Runner.Run(o.Dir, a[0], a[1:]...)
				for _, ln := range splitLines(out) {
					ch <- StreamEvent{Line: ln}
				}
			}
			if err != nil {
				ch <- StreamEvent{Done: true, Err: err}
				return
			}
		}
		ch <- StreamEvent{Done: true}
	}()
	return ch
}

// SetParent records branch's git-town parent in local .git/config — the "track
// this branch" action that adds an untracked branch to the stack tree. Nothing is
// committed, staged, or pushed. Streamed like the other ops so the stack screen
// reuses one execution + output path.
func (o Ops) SetParent(branch, parent string) <-chan StreamEvent {
	return o.streamCmds([][]string{{"git", "config", "git-town-branch." + branch + ".parent", parent}})
}

// RemoveMergedLocal cleans up local branches that were just shipped (their PR
// merged, their remote deleted) but that git-town's sync left behind because they
// were the CHECKED-OUT branch — git can't delete HEAD. It hops to the trunk so
// none is current, then force-deletes each leftover and clears its git-town parent
// config (so it doesn't linger as a phantom tree node). It touches ONLY branches
// still present locally (sync already removed the non-current ones), so on a
// partial ship — where the branch you're on wasn't merged — it does nothing and
// does NOT move you off it. Streamed; best-effort per branch.
func (o Ops) RemoveMergedLocal(trunk string, merged []string) <-chan StreamEvent {
	ch := make(chan StreamEvent, 16)
	go func() {
		defer close(ch)
		var leftover []string
		for _, b := range merged {
			if _, err := o.Runner.Run(o.Dir, "git", "show-ref", "--verify", "--quiet", "refs/heads/"+b); err == nil {
				leftover = append(leftover, b)
			}
		}
		if len(leftover) == 0 {
			ch <- StreamEvent{Done: true} // nothing stranded — don't even switch branch
			return
		}
		if _, err := o.Runner.Run(o.Dir, "git", "checkout", trunk); err != nil {
			ch <- StreamEvent{Done: true, Err: fmt.Errorf("could not switch to %s to clean up: %w", trunk, err)}
			return
		}
		for _, b := range leftover {
			if _, err := o.Runner.Run(o.Dir, "git", "branch", "-D", b); err != nil {
				ch <- StreamEvent{Line: "  (could not delete local " + b + ": " + err.Error() + ")"}
				continue
			}
			o.Runner.Run(o.Dir, "git", "config", "--unset", "git-town-branch."+b+".parent")
			ch <- StreamEvent{Line: fmt.Sprintf("Removed %s locally (it was merged; git-town can't delete the branch you were on).", b)}
		}
		ch <- StreamEvent{Done: true}
	}()
	return ch
}

// Stream is ExecRunner's live implementation: pipe the process's combined output
// and emit each line as it is produced.
func (ExecRunner) Stream(dir, name string, args []string, emit func(line string)) error {
	cmd := exec.Command(name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = nonInteractiveEnv()
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw
	if err := cmd.Start(); err != nil {
		return err
	}
	wait := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		pw.Close() // unblock the scanner with EOF once the process exits
		wait <- err
	}()
	sc := bufio.NewScanner(pr)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		emit(sc.Text())
	}
	return <-wait
}

// splitLines splits trimmed output into lines (nil for empty).
func splitLines(s string) []string {
	if s = strings.TrimRight(s, "\n"); s == "" {
		return nil
	}
	return strings.Split(s, "\n")
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
	if c.Verb == "ship" {
		// ship isn't a single git-town call: Cairn merges the PR via gh, then syncs.
		return "gh: merge PR (squash)  →  git-town sync --stack"
	}
	if c.Verb == "shipstack" {
		// The whole-stack ship loops ship's remote steps bottom-up, then syncs once.
		return "for each branch bottom-up: gh merge PR (squash) → retarget children → delete  ·  then git-town sync"
	}
	if c.Verb == "propose" {
		// propose isn't a single git-town call: Cairn pushes the branch, then opens
		// the PR via gh with the base read from the local lineage.
		return "git push -u origin <branch>  →  gh: create PR (base ← parent)"
	}
	if c.Verb == "ready" {
		// ready isn't a git-town call: Cairn marks the PR ready via the GitHub API.
		return "gh: mark PR ready for review"
	}
	if c.Verb == "reconcile" {
		// reconcile isn't a git-town verb: a stack sync, then local cleanup of any
		// merged branch still checked out.
		return "git-town sync --stack  →  drop merged branches locally"
	}
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
			Key: "p", Verb: "propose", Title: "propose", Mutates: true,
			Short: "open a pull request for a branch",
			Long: "Opens a GitHub pull request for the selected branch. Its base is set " +
				"automatically to whatever the branch is stacked on — the branch below it " +
				"in the stack, or the trunk (main) for the bottom branch — read from the " +
				"local lineage, so a stacked PR targets the right place, not main. Cairn " +
				"pushes the branch first, then you title it and write the description in " +
				"Markdown (with a live preview).",
		},
		{
			Key: "Y", Verb: "ready", Title: "mark ready", Mutates: true,
			Short: "take a draft PR out of draft (ready for review)",
			Long: "Marks the selected branch's pull request READY for review — takes it " +
				"out of draft on GitHub. A draft PR can't be merged, so this is how the " +
				"author clears that block without leaving Cairn: it can notify reviewers " +
				"and start required checks. Acts on the branch under the tree cursor, so " +
				"you can ready a draft mid-stack without checking it out first.",
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
		{
			Key: "M", Verb: "ship", Title: "merge", Mutates: true,
			Short: "merge this branch's PR, then re-stack",
			Long: "Merges this branch's pull request into the trunk on GitHub (squash), " +
				"deletes the branch, then syncs so the branches above re-parent onto the " +
				"trunk. Only the BOTTOM of the stack can be merged: a stacked PR targets " +
				"the branch below it, so lower branches must land first.",
		},
		{
			Key: "G", Verb: "shipstack", Title: "merge whole stack", Mutates: true,
			Short: "merge the whole stack, bottom to top, in order",
			Long: "Merges the WHOLE stack in one go, wherever you are in it: starting at " +
				"the bottom (the branch that targets trunk) and working up to the top, it " +
				"squash-merges each pull request into the trunk, retargets the branch above " +
				"onto the trunk, deletes the merged branch, and finally syncs once. This is " +
				"the most destructive action in Cairn — it lands several PRs and cannot be " +
				"undone. If a PR along the way can't merge, the ones below it still land and " +
				"everything above is left untouched.",
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
