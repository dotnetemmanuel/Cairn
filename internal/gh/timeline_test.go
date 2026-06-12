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

func TestTimelineNestsInlineCommentsUnderReview(t *testing.T) {
	base := time.Date(2026, 2, 1, 9, 0, 0, 0, time.UTC)
	d := PRDetail{
		Author: "author", Body: "desc", CreatedAt: base,
		Reviews: []Review{
			{ID: "REV1", Author: "daniel", State: "COMMENTED", Body: "A few follow-ups", CreatedAt: base.Add(time.Hour)},
		},
		ReviewComments: []ReviewComment{
			{Author: "daniel", Body: "nit one", Path: "a.cs", Line: 26, DiffHunk: "@@ -0,0 +1 @@\n+code", ReviewID: "REV1", CreatedAt: base.Add(61 * time.Minute)},
			{Author: "daniel", Body: "nit two", Path: "b.ts", Line: 58, DiffHunk: "@@\n+x", ReviewID: "REV1", CreatedAt: base.Add(62 * time.Minute)},
			{Author: "stranger", Body: "orphan", Path: "c.go", Line: 3, ReviewID: "", CreatedAt: base.Add(3 * time.Hour)},
		},
	}

	tl := d.Timeline()
	// description + Daniel's review + the orphan inline = 3 top-level entries.
	if len(tl) != 3 {
		t.Fatalf("want 3 top-level entries, got %d", len(tl))
	}
	rev := tl[1]
	if rev.Kind != KindReview || len(rev.Children) != 2 {
		t.Fatalf("review should carry its 2 inline children; kind=%d children=%d", rev.Kind, len(rev.Children))
	}
	if rev.Children[0].DiffHunk == "" {
		t.Error("nested inline comment must keep its diff hunk for the citation")
	}
	if rev.Children[0].CreatedAt.After(rev.Children[1].CreatedAt) {
		t.Error("children should be time-ordered")
	}
	if tl[2].Kind != KindInline || tl[2].Author != "stranger" {
		t.Errorf("inline with no review id should stay top-level, got %+v", tl[2])
	}
}
