package gh

import "testing"

// withDemo runs fn with demo mode on, restoring the flag after so other tests in
// the package still see the real (off) code paths.
func withDemo(t *testing.T, fn func()) {
	t.Helper()
	prev := demoOn
	EnableDemo()
	defer func() { demoOn = prev }()
	fn()
}

// TestDemoScreensPopulated asserts every intercepted read returns non-empty,
// error-free fixture data once armed, so no screen renders blank in demo mode.
func TestDemoScreensPopulated(t *testing.T) {
	withDemo(t, func() {
		if v, err := FetchViewer(); err != nil || v.Login != "avery" {
			t.Fatalf("FetchViewer = %+v, %v", v, err)
		}
		if orgs, err := FetchOrgs(); err != nil || len(orgs) == 0 {
			t.Fatalf("FetchOrgs = %v, %v", orgs, err)
		}
		for _, f := range []string{"is:open is:pr author:@me", "is:open is:pr review-requested:@me", "org:northwind", "is:closed"} {
			items, _, err := SearchItems(f, 30)
			if err != nil || len(items) == 0 {
				t.Fatalf("SearchItems(%q) = %d items, %v", f, len(items), err)
			}
		}
		if feed, err := FetchNotificationFeed(30); err != nil || len(feed) == 0 {
			t.Fatalf("FetchNotificationFeed = %d, %v", len(feed), err)
		}
	})
}

// TestDemoInlineAnchorExists guards the review pane's 💬 badge: #142 must carry
// at least one inline comment whose Path/Line lands on a real diff line in its
// files, or the badge would have nothing to attach to.
func TestDemoInlineAnchorExists(t *testing.T) {
	withDemo(t, func() {
		detail, err := FetchPRDetail("northwind", "atlas", 142)
		if err != nil || len(detail.ReviewComments) == 0 {
			t.Fatalf("PRDetail(142) has no review comments: %v", err)
		}
		files, err := FetchPRFiles("northwind", "atlas", 142)
		if err != nil {
			t.Fatalf("PRFiles(142): %v", err)
		}
		paths := map[string]bool{}
		for _, f := range files {
			paths[f.Filename] = true
		}
		anchored := false
		for _, rc := range detail.ReviewComments {
			if paths[rc.Path] && rc.AnchorLine() > 0 {
				anchored = true
			}
		}
		if !anchored {
			t.Fatal("no inline comment anchors onto a #142 diff file")
		}
	})
}

// TestDemoStackConsistent asserts the branch->number map matches the OpenPRs
// stack, so stack mode's PR overlay lines up with the tree.
func TestDemoStackConsistent(t *testing.T) {
	withDemo(t, func() {
		nums, _ := OpenPRNumbersByBranch("northwind", "atlas")
		prs, _ := OpenPRs("northwind", "atlas")
		if len(prs) != len(nums) {
			t.Fatalf("OpenPRs(%d) vs numbers-by-branch(%d) size mismatch", len(prs), len(nums))
		}
		for _, p := range prs {
			if nums[p.Head] != p.Number {
				t.Fatalf("branch %q: OpenPRs #%d but map #%d", p.Head, p.Number, nums[p.Head])
			}
		}
	})
}
