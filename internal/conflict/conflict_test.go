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
