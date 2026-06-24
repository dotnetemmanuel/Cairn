// Package conflict turns a conflicted working tree into a guided, resolvable
// model: it parses the conflict markers git leaves in a file into sides a human
// can choose between, and later applies that choice back to disk. It is pure
// git/text manipulation with no TUI dependencies, so the parsing and resolution
// logic stays directly testable.
package conflict

import (
	"fmt"
	"strings"
)

// Parse splits a conflicted file's contents into ordered spans: runs of
// unconflicted Context text interleaved with Conflict Regions. Context spans
// keep their exact trailing newlines so that concatenating every context span
// with a chosen side per region round-trips the file losslessly.
//
// Parse is SIDE-AGNOSTIC about ours/theirs semantics. It fills Region fields
// purely by marker position: the first section (after `<<<<<<<`, before
// `|||||||`/`=======`) goes into Incoming, the base section (between `|||||||`
// and `=======`, present only with diff3/zdiff3 conflictStyle) goes into Base,
// and the second section (after `=======`, before `>>>>>>>`) goes into Yours. A
// later task maps the human branch-name labels per rebase/merge; Parse never
// interprets which raw side is which.
//
// An all-text file yields a single Context span. Empty input yields nil spans
// and no error. A conflict that opens with `<<<<<<<` but never closes with
// `>>>>>>>` returns an error.
func Parse(content string) ([]Span, error) {
	if content == "" {
		return nil, nil
	}

	var spans []Span
	var text strings.Builder // accumulated context text (with newlines)
	var region *Region
	// section points at the slice the current region is collecting into.
	var section *[]string

	flushText := func() {
		if text.Len() > 0 {
			spans = append(spans, Span{Text: text.String()})
			text.Reset()
		}
	}

	// SplitAfter keeps the trailing "\n" on each line, preserving exact text. A
	// final line without a newline keeps no newline; a trailing newline yields a
	// final empty element we skip.
	lines := strings.SplitAfter(content, "\n")
	for i, raw := range lines {
		if i == len(lines)-1 && raw == "" {
			break
		}
		line := strings.TrimSuffix(raw, "\n")
		switch {
		case strings.HasPrefix(line, "<<<<<<<"):
			flushText()
			region = &Region{}
			section = &region.Incoming
		case region != nil && strings.HasPrefix(line, "|||||||"):
			section = &region.Base
		case region != nil && strings.HasPrefix(line, "======="):
			section = &region.Yours
		case region != nil && strings.HasPrefix(line, ">>>>>>>"):
			spans = append(spans, Span{Conflict: region})
			region = nil
			section = nil
		case region != nil:
			*section = append(*section, line)
		default:
			text.WriteString(raw)
		}
	}

	if region != nil {
		return nil, fmt.Errorf("conflict: unterminated conflict region (missing >>>>>>> marker)")
	}
	flushText()
	return spans, nil
}

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

// Resolution records how one Region is resolved: a Choice, plus the Custom
// replacement text used only when Choice is ChoiceCustom.
type Resolution struct {
	Choice Choice
	Custom string
}

// Conflicts counts the conflict spans (Regions) in spans.
func Conflicts(spans []Span) int {
	n := 0
	for _, s := range spans {
		if s.Conflict != nil {
			n++
		}
	}
	return n
}

// Apply reassembles the final file content from parsed spans and a per-Region
// resolution list. Context spans are emitted verbatim; the Nth conflict span is
// resolved by res[N]. A missing resolution (N out of range) is treated as
// ChoiceUnresolved, which re-emits canonical (bare, unlabeled) conflict markers
// so a partially-resolved file round-trips and can be re-parsed.
func Apply(spans []Span, res []Resolution) string {
	var b strings.Builder
	conflictIdx := 0
	emit := func(lines []string) {
		for _, l := range lines {
			b.WriteString(l)
			b.WriteString("\n")
		}
	}
	for _, s := range spans {
		if s.Conflict == nil {
			b.WriteString(s.Text)
			continue
		}
		r := s.Conflict
		choice := ChoiceUnresolved
		var custom string
		if conflictIdx < len(res) {
			choice = res[conflictIdx].Choice
			custom = res[conflictIdx].Custom
		}
		conflictIdx++
		switch choice {
		case ChoiceIncoming:
			emit(r.Incoming)
		case ChoiceYours:
			emit(r.Yours)
		case ChoiceBoth:
			emit(r.Incoming)
			emit(r.Yours)
		case ChoiceCustom:
			b.WriteString(strings.TrimSuffix(custom, "\n"))
			b.WriteString("\n")
		default: // ChoiceUnresolved: reproduce canonical bare markers.
			b.WriteString("<<<<<<<\n")
			emit(r.Incoming)
			if r.Base != nil {
				b.WriteString("|||||||\n")
				emit(r.Base)
			}
			b.WriteString("=======\n")
			emit(r.Yours)
			b.WriteString(">>>>>>>\n")
		}
	}
	return b.String()
}

// State is the whole conflicted tree at one moment. Incoming and Yours are the
// branch-name labels shown for each side.
type State struct {
	Op       Op
	Incoming string
	Yours    string
	Files    []string
}
