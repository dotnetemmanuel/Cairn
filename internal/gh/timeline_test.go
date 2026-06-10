package gh

import (
	"testing"
	"time"
)

func TestTimelineOrdersDescriptionFirstThenByTime(t *testing.T) {
	base := time.Date(2026, 1, 23, 9, 0, 0, 0, time.UTC)
	d := PRDetail{
		Author:    "author",
		Body:      "the description",
		CreatedAt: base,
		Comments: []Comment{
			{Author: "boss", Body: "looks good", CreatedAt: base.Add(2 * time.Hour)},
			{Author: "me", Body: "early note", CreatedAt: base.Add(1 * time.Hour)},
		},
		Reviews: []Review{
			{Author: "ci", State: "CHANGES_REQUESTED", Body: "fix this", CreatedAt: base.Add(90 * time.Minute)},
			{Author: "noise", State: "COMMENTED", Body: "", CreatedAt: base.Add(30 * time.Minute)}, // dropped
		},
		ReviewComments: []ReviewComment{
			{Author: "boss", Body: "inline nit", Path: "a.go", Line: 5, CreatedAt: base.Add(3 * time.Hour)},
		},
	}

	tl := d.Timeline()

	if tl[0].Kind != KindDescription || tl[0].Body != "the description" {
		t.Fatalf("expected description first, got %+v", tl[0])
	}
	// Remaining entries strictly increasing in time; the empty COMMENTED review dropped.
	var prev time.Time
	for i, e := range tl[1:] {
		if e.CreatedAt.Before(prev) {
			t.Errorf("entry %d out of order: %v before %v", i, e.CreatedAt, prev)
		}
		prev = e.CreatedAt
		if e.Kind == KindReview && e.State == "COMMENTED" && e.Body == "" {
			t.Errorf("empty COMMENTED review should have been dropped")
		}
	}
	if got := len(tl); got != 5 { // description + 2 comments + 1 review + 1 inline
		t.Fatalf("expected 5 timeline entries, got %d", got)
	}
}
