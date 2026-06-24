// Package conflict turns a conflicted working tree into a guided, resolvable
// model: it parses the conflict markers git leaves in a file into sides a human
// can choose between, and later applies that choice back to disk. It is pure
// git/text manipulation with no TUI dependencies, so the parsing and resolution
// logic stays directly testable.
package conflict

// Op is the in-progress git operation. It decides which merge stage maps to
// incoming vs yours, because rebase inverts the raw ours/theirs sides relative
// to a merge.
type Op int

const (
	OpNone Op = iota
	OpRebase
	OpMerge
)

// Side labels a half of a conflict for humans. We never use the raw ours/theirs
// names because they flip under rebase; SideIncoming is the change being brought
// in (trunk or parent on rebase) and SideYours is your branch's change.
type Side int

const (
	SideIncoming Side = iota
	SideYours
)

// Span is a slice of a parsed file: either an unconflicted run of Text, or a
// Conflict block (in which case Text is empty and Conflict is non-nil).
type Span struct {
	Text     string
	Conflict *Region
}

// Region is a single <<<<<<< ======= >>>>>>> block split into its sides. Base is
// nil unless diff3/zdiff3 ||||||| markers were present.
type Region struct {
	Incoming []string
	Base     []string
	Yours    []string
}

// Choice records how a Region is resolved.
type Choice int

const (
	ChoiceUnresolved Choice = iota
	ChoiceIncoming
	ChoiceYours
	ChoiceBoth   // incoming then yours
	ChoiceCustom // use Custom text
)

// State is the whole conflicted tree at one moment. Incoming and Yours are the
// branch-name labels shown for each side.
type State struct {
	Op       Op
	Incoming string
	Yours    string
	Files    []string
}
