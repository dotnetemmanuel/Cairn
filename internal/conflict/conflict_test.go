package conflict_test

import (
	"strings"
	"testing"

	"github.com/dotnetemmanuel/cairn/internal/conflict"
)

func TestParseFileTwoSidedConflict(t *testing.T) {
	in := "a\nb\n<<<<<<< HEAD\nINC1\nINC2\n=======\nYOUR1\n>>>>>>> feat\nc\n"
	spans, err := conflict.Parse(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(spans) != 3 {
		t.Fatalf("want 3 spans, got %d", len(spans))
	}
	if spans[0].Text != "a\nb\n" {
		t.Errorf("ctx0 = %q", spans[0].Text)
	}
	r := spans[1].Conflict
	if r == nil {
		t.Fatal("span 1 not a conflict")
	}
	if got := strings.Join(r.Incoming, "|"); got != "INC1|INC2" {
		t.Errorf("incoming = %q", got)
	}
	if got := strings.Join(r.Yours, "|"); got != "YOUR1" {
		t.Errorf("yours = %q", got)
	}
	if len(r.Base) != 0 {
		t.Errorf("base should be empty, got %v", r.Base)
	}
	if spans[2].Text != "c\n" {
		t.Errorf("ctx2 = %q", spans[2].Text)
	}
}

func TestParseFileDiff3Base(t *testing.T) {
	in := "<<<<<<< HEAD\nINC\n||||||| base\nORIG\n=======\nYOURS\n>>>>>>> feat\n"
	spans, _ := conflict.Parse(in)
	r := spans[0].Conflict
	if strings.Join(r.Base, "|") != "ORIG" {
		t.Errorf("base = %v", r.Base)
	}
}

func TestParseNoConflict(t *testing.T) {
	in := "line one\nline two\nno markers here\n"
	spans, err := conflict.Parse(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(spans) != 1 {
		t.Fatalf("want 1 span, got %d", len(spans))
	}
	if spans[0].Conflict != nil {
		t.Errorf("span 0 should be context, got conflict")
	}
	if spans[0].Text != in {
		t.Errorf("ctx = %q, want %q", spans[0].Text, in)
	}
}

func TestParseUnterminated(t *testing.T) {
	in := "a\n<<<<<<< HEAD\nINC\n=======\nYOURS\n"
	if _, err := conflict.Parse(in); err == nil {
		t.Fatal("want error for unterminated conflict, got nil")
	}
}

func TestParseEmptyInput(t *testing.T) {
	spans, err := conflict.Parse("")
	if err != nil {
		t.Fatal(err)
	}
	if spans != nil {
		t.Errorf("want nil spans for empty input, got %v", spans)
	}
}

func TestApplyChoices(t *testing.T) {
	in := "a\n<<<<<<< HEAD\nINC\n=======\nYOURS\n>>>>>>> feat\nb\n"
	spans, _ := conflict.Parse(in)
	cases := []struct {
		c      conflict.Choice
		custom string
		want   string
	}{
		{conflict.ChoiceIncoming, "", "a\nINC\nb\n"},
		{conflict.ChoiceYours, "", "a\nYOURS\nb\n"},
		{conflict.ChoiceBoth, "", "a\nINC\nYOURS\nb\n"},
		{conflict.ChoiceCustom, "X\nY", "a\nX\nY\nb\n"},
	}
	for _, tc := range cases {
		out := conflict.Apply(spans, []conflict.Resolution{{Choice: tc.c, Custom: tc.custom}})
		if out != tc.want {
			t.Errorf("choice %v: got %q want %q", tc.c, out, tc.want)
		}
	}
}

// TestApplyUnresolvedRoundTrips uses BARE markers (no branch labels) so that
// ChoiceUnresolved's canonical marker reconstruction reproduces the input
// byte-for-byte; this lets a partially-resolved file be re-parsed.
func TestApplyUnresolvedRoundTrips(t *testing.T) {
	in := "a\n<<<<<<<\nINC\n=======\nYOURS\n>>>>>>>\nb\n"
	spans, _ := conflict.Parse(in)
	if out := conflict.Apply(spans, []conflict.Resolution{{Choice: conflict.ChoiceUnresolved}}); out != in {
		t.Errorf("explicit unresolved: got %q want %q", out, in)
	}
	if out := conflict.Apply(spans, nil); out != in {
		t.Errorf("empty resolutions: got %q want %q", out, in)
	}
}

func TestConflictsCount(t *testing.T) {
	in := "a\n<<<<<<< HEAD\nINC\n=======\nYOURS\n>>>>>>> feat\nb\n<<<<<<< HEAD\nI2\n=======\nY2\n>>>>>>> feat\nc\n"
	spans, _ := conflict.Parse(in)
	if n := conflict.Conflicts(spans); n != 2 {
		t.Errorf("Conflicts = %d, want 2", n)
	}
	plain, _ := conflict.Parse("no conflicts here\n")
	if n := conflict.Conflicts(plain); n != 0 {
		t.Errorf("Conflicts = %d, want 0", n)
	}
}

// TestParseRoundTrip checks that context text retains exact newlines so a later
// Apply can reassemble the file losslessly.
func TestParseRoundTrip(t *testing.T) {
	in := "a\nb\n<<<<<<< HEAD\nINC1\nINC2\n=======\nYOUR1\n>>>>>>> feat\nc\n"
	spans, err := conflict.Parse(in)
	if err != nil {
		t.Fatal(err)
	}
	// Reassemble choosing the incoming side and re-emitting markers' content.
	var b strings.Builder
	for _, s := range spans {
		if s.Conflict == nil {
			b.WriteString(s.Text)
			continue
		}
		for _, l := range s.Conflict.Incoming {
			b.WriteString(l)
			b.WriteString("\n")
		}
	}
	if got, want := b.String(), "a\nb\nINC1\nINC2\nc\n"; got != want {
		t.Errorf("roundtrip = %q, want %q", got, want)
	}
}
