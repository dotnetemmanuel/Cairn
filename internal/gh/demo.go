package gh

// Demo mode: a self-contained, read-only fake GitHub for screenshots and a
// no-account first run. When demoOn is set (via EnableDemo, called for the
// `cairn demo` subcommand or when CAIRN_DEMO=1 is in the environment), each
// intercepted read function in this package short-circuits to a demo* variant
// here instead of hitting the network. Nothing in demo mode mutates anything;
// write paths never route through here. All data is fake and consistent across
// screens: the same cast, org, repos, and PR stack everywhere.
//
// The whole feature is this one file plus a one-line guard at the top of each
// intercepted function. Keeping fixtures here keeps the real code paths obvious.

import (
	"strings"
	"time"
)

// demoOn flips every intercepted read function to its fixture variant. Set once
// by EnableDemo; never toggled back within a run.
var demoOn bool

// EnableDemo turns on demo mode for the process. Call before constructing the
// TUI. Idempotent.
func EnableDemo() { demoOn = true }

// DemoActive reports whether demo mode is on, so the TUI can gate write actions
// (approve, merge, propose, …) behind a toast instead of a real API call.
func DemoActive() bool { return demoOn }

// The fixture cast — all fake. northwind is the decades-old sample-database
// company (Northwind Traders), so it reads instantly as demo data.
const (
	demoViewer = "avery"
	demoCIBot  = "northwind-ci"
	demoOrg    = "northwind"
)

// demoAgo is a timestamp d before now. Only the relative label ("3h ago") is
// rendered, so wall-clock drift between captures does not matter.
func demoAgo(d time.Duration) time.Time { return time.Now().Add(-d) }

// --- viewer / orgs -------------------------------------------------------

func demoFetchViewer() (Viewer, error) {
	return Viewer{Login: demoViewer, RateRemaining: 4987, RateLimit: 5000}, nil
}

func demoFetchOrgs() ([]string, error) {
	return []string{demoOrg}, nil
}

// --- the board (SearchItems) --------------------------------------------

// demoStack is the centerpiece: a three-PR stack in northwind/atlas that drives
// the hero sidebar, stack mode, the review pane, and the conversation view.
//
//	main (trunk)
//	└─ search-api    #141 base main         approved        CI green
//	   └─ search-index #142 base search-api   review required CI green   ← review/conversation demo
//	      └─ search-ui  #143 base search-index draft           CI pending
func demoStack() []Item {
	return []Item{
		{IsPR: true, Repo: "northwind/atlas", Number: 141, Title: "Add the search API skeleton", Author: demoViewer, State: "OPEN", Review: ReviewApproved, Checks: CheckSuccess, HeadBranch: "search-api", BaseBranch: "main", UpdatedAt: demoAgo(26 * time.Hour)},
		{IsPR: true, Repo: "northwind/atlas", Number: 142, Title: "Build the inverted index", Author: demoViewer, State: "OPEN", Review: ReviewRequired, Checks: CheckSuccess, HeadBranch: "search-index", BaseBranch: "search-api", UpdatedAt: demoAgo(3 * time.Hour)},
		{IsPR: true, Repo: "northwind/atlas", Number: 143, Title: "Wire the search UI", Author: demoViewer, State: "OPEN", IsDraft: true, Review: ReviewNone, Checks: CheckPending, HeadBranch: "search-ui", BaseBranch: "search-index", UpdatedAt: demoAgo(40 * time.Minute)},
	}
}

// demoMyPRs is the "My PRs" tab: the atlas stack plus one landed-adjacent PR in
// another repo so the section is not single-repo.
func demoMyPRs() []Item {
	return append(demoStack(),
		Item{IsPR: true, Repo: "northwind/ledger", Number: 77, Title: "Cache exchange-rate lookups", Author: demoViewer, State: "OPEN", Review: ReviewApproved, Checks: CheckSuccess, HeadBranch: "rate-cache", BaseBranch: "main", UpdatedAt: demoAgo(2 * 24 * time.Hour)},
	)
}

// demoNeedsReview is the "Needs my review" tab: PRs from teammates waiting on the
// viewer, one clean and one with a failing check + changes requested.
func demoNeedsReview() []Item {
	return []Item{
		{IsPR: true, Repo: "northwind/beacon", Number: 98, Title: "Rotate signing keys before the Q3 audit", Author: "jordan", State: "OPEN", Review: ReviewRequired, Checks: CheckSuccess, HeadBranch: "key-rotation", BaseBranch: "main", ReviewReqFromMe: true, UpdatedAt: demoAgo(5 * time.Hour)},
		{IsPR: true, Repo: "northwind/ledger", Number: 52, Title: "Fix rounding in multi-currency invoices", Author: "kim", State: "OPEN", Review: ReviewChangesRequested, Checks: CheckFailure, HeadBranch: "currency-rounding", BaseBranch: "main", ReviewReqFromMe: true, UpdatedAt: demoAgo(8 * time.Hour)},
	}
}

// demoOrgsMix is the "Orgs" tab: an org-wide spread, freshest first — the stack
// plus teammate work across the three repos.
func demoOrgsMix() []Item {
	return []Item{
		demoStack()[2], // search-ui (draft, freshest)
		{IsPR: true, Repo: "northwind/beacon", Number: 104, Title: "Add structured audit logging", Author: "noor", State: "OPEN", Review: ReviewApproved, Checks: CheckSuccess, HeadBranch: "audit-logs", BaseBranch: "main", UpdatedAt: demoAgo(90 * time.Minute)},
		demoStack()[1],       // search-index
		demoNeedsReview()[0], // beacon key-rotation
		{IsPR: true, Repo: "northwind/atlas", Number: 139, Title: "Tune the ranking weights", Author: "theo", State: "OPEN", Review: ReviewRequired, Checks: CheckPending, HeadBranch: "ranking-weights", BaseBranch: "main", UpdatedAt: demoAgo(6 * time.Hour)},
		demoNeedsReview()[1], // ledger currency-rounding
		demoStack()[0],       // search-api
	}
}

// demoInvolved backs the three involved sub-queries (assigned / mentioned /
// participating), routed by filter marker.
func demoInvolved(filter string) []Item {
	switch {
	case strings.Contains(filter, "assignee:@me"):
		return []Item{
			{IsPR: false, Repo: "northwind/atlas", Number: 120, Title: "Search latency p99 regressed after the index rewrite", Author: "kim", State: "OPEN", UpdatedAt: demoAgo(12 * time.Hour)},
		}
	case strings.Contains(filter, "mentions:@me"):
		return []Item{
			{IsPR: true, Repo: "northwind/ledger", Number: 60, Title: "Document the reconciliation runbook", Author: "noor", State: "OPEN", Review: ReviewRequired, Checks: CheckSuccess, HeadBranch: "recon-runbook", BaseBranch: "main", UpdatedAt: demoAgo(20 * time.Hour)},
		}
	default: // commenter:@me — participating
		return []Item{
			{IsPR: true, Repo: "northwind/beacon", Number: 88, Title: "Retry webhook deliveries with backoff", Author: "theo", State: "OPEN", Review: ReviewApproved, Checks: CheckSuccess, HeadBranch: "webhook-retry", BaseBranch: "main", UpdatedAt: demoAgo(30 * time.Hour)},
		}
	}
}

// demoClosedTail is the muted OPEN/CLOSED divider tail: recently merged/closed
// work under an open list.
func demoClosedTail() []Item {
	return []Item{
		{IsPR: true, Repo: "northwind/atlas", Number: 140, Title: "Extract the query planner into its own package", Author: demoViewer, State: "MERGED", Review: ReviewApproved, Checks: CheckSuccess, HeadBranch: "query-planner", BaseBranch: "main", UpdatedAt: demoAgo(3 * 24 * time.Hour)},
		{IsPR: true, Repo: "northwind/atlas", Number: 131, Title: "Drop the legacy v1 search endpoint", Author: "jordan", State: "MERGED", Review: ReviewApproved, Checks: CheckSuccess, HeadBranch: "drop-v1", BaseBranch: "main", UpdatedAt: demoAgo(5 * 24 * time.Hour)},
		{IsPR: false, Repo: "northwind/ledger", Number: 45, Title: "Spike: columnar storage for the ledger", Author: "noor", State: "CLOSED", UpdatedAt: demoAgo(9 * 24 * time.Hour)},
	}
}

// demoSearch routes a board filter to the matching fixture set. Order matters:
// closed/merged is checked first (it overlays any tab), then the specific tabs.
func demoSearch(filter string, limit int) ([]Item, int, error) {
	var items []Item
	switch {
	case strings.Contains(filter, "is:closed"), strings.Contains(filter, "is:merged"):
		items = demoClosedTail()
	case strings.Contains(filter, "author:@me"):
		items = demoMyPRs()
	case strings.Contains(filter, "review-requested:@me"):
		items = demoNeedsReview()
	case strings.Contains(filter, "assignee:@me"), strings.Contains(filter, "mentions:@me"), strings.Contains(filter, "commenter:@me"):
		items = demoInvolved(filter)
	case strings.Contains(filter, "org:"):
		items = demoOrgsMix()
	default:
		items = demoOrgsMix()
	}
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items, len(items), nil
}

// --- notifications -------------------------------------------------------

func demoNotificationFeed(limit int) ([]Notification, error) {
	feed := []Notification{
		{ThreadID: "nt1", Reason: "review_requested", Unread: true, Type: "PullRequest", Repo: "northwind/beacon", Number: 98, Title: "Rotate signing keys before the Q3 audit", UpdatedAt: demoAgo(5 * time.Hour)},
		{ThreadID: "nt2", Reason: "mention", Unread: true, Type: "PullRequest", Repo: "northwind/atlas", Number: 142, Title: "Build the inverted index", UpdatedAt: demoAgo(3 * time.Hour)},
		{ThreadID: "nt3", Reason: "comment", Unread: true, Type: "Issue", Repo: "northwind/atlas", Number: 120, Title: "Search latency p99 regressed after the index rewrite", UpdatedAt: demoAgo(4 * time.Hour)},
		{ThreadID: "nt4", Reason: "ci_activity", Unread: false, Type: "CheckSuite", Repo: "northwind/ledger", Number: 52, Title: "CI failed on currency-rounding", UpdatedAt: demoAgo(8 * time.Hour)},
		{ThreadID: "nt5", Reason: "author", Unread: false, Type: "PullRequest", Repo: "northwind/atlas", Number: 141, Title: "Add the search API skeleton", UpdatedAt: demoAgo(26 * time.Hour)},
		{ThreadID: "nt6", Reason: "comment", Unread: false, Type: "PullRequest", Repo: "northwind/ledger", Number: 52, Title: "Fix rounding in multi-currency invoices", UpdatedAt: demoAgo(9 * time.Hour)},
		{ThreadID: "nt7", Reason: "subscribed", Unread: false, Type: "Release", Repo: "northwind/beacon", Number: 0, Title: "beacon v2.4.0", UpdatedAt: demoAgo(2 * 24 * time.Hour)},
	}
	if limit > 0 && len(feed) > limit {
		feed = feed[:limit]
	}
	return feed, nil
}

// demoNotifications maps the feed onto the Item shape for any non-inbox caller.
func demoNotifications(limit int) ([]Item, int, error) {
	feed, _ := demoNotificationFeed(limit)
	items := make([]Item, 0, len(feed))
	for _, n := range feed {
		items = append(items, Item{IsPR: n.Type == "PullRequest", Repo: n.Repo, Number: n.Number, Title: n.Title, State: "OPEN", UpdatedAt: n.UpdatedAt})
	}
	return items, len(items), nil
}

// --- PR detail + files ---------------------------------------------------

// demoFetchPRDetail returns the rich fixture for #142 (comments, reviews, inline
// threads, checks, commits, events) and a lighter, board-consistent detail for
// any other number, so opening any demo PR still renders.
func demoFetchPRDetail(owner, repo string, number int) (PRDetail, error) {
	if number == 142 {
		return demoDetail142(), nil
	}
	return demoLightDetail(number), nil
}

func demoDetail142() PRDetail {
	return PRDetail{
		Number: 142, Title: "Build the inverted index", Author: demoViewer, State: "OPEN",
		URL:     "https://github.com/northwind/atlas/pull/142",
		BaseRef: "search-api", HeadRef: "search-index", HeadSHA: "c7d8e9f",
		Additions: 48, Deletions: 4, ChangedFiles: 3,
		CreatedAt: demoAgo(2 * 24 * time.Hour),
		Body: "Adds the in-memory inverted index that the ranking work builds on.\n\n" +
			"- `Index.Add` tokenizes a doc and appends to each token's posting list\n" +
			"- `Index.Lookup` returns a token's posting list\n" +
			"- benchmarks for `Add`/`Lookup` on a 100k-doc corpus\n\n" +
			"Stacked on #141 (the search API skeleton). #143 wires this into the UI.",
		Comments: []Comment{
			{Author: "jordan", Body: "Nice, this unblocks the ranking work. Can we get a benchmark on Add for a 100k-doc corpus?", DatabaseID: 8001, CreatedAt: demoAgo(6 * time.Hour)},
			{Author: demoViewer, Body: "Benchmark added in the latest push: about 120ns/op for Add, allocation-free after warmup.", DatabaseID: 8002, CreatedAt: demoAgo(3 * time.Hour)},
		},
		Reviews: []Review{
			{ID: "REV_kim", Author: "kim", State: "CHANGES_REQUESTED", Body: "One correctness issue on the posting list, otherwise this is close.", CreatedAt: demoAgo(5 * time.Hour)},
			{ID: "REV_jordan", Author: "jordan", State: "COMMENTED", Body: "Left a couple of nits.", CreatedAt: demoAgo(4*time.Hour + 30*time.Minute)},
		},
		ReviewComments: []ReviewComment{
			{Author: "kim", Path: "internal/search/index.go", Line: 16, OriginalLine: 16, Side: "RIGHT",
				DiffHunk: "@@ -0,0 +1,24 @@\n+// Add indexes doc under each of its tokens.\n+func (ix *Index) Add(doc int, tokens []string) {\n+\tfor _, t := range tokens {\n+\t\tix.postings[t] = append(ix.postings[t], doc)",
				Body:     "A token that appears twice in the same doc adds two identical postings, so Lookup double-counts it. Dedup per doc before appending.",
				ThreadID: 1, ReviewID: "REV_kim", DatabaseID: 9001, CreatedAt: demoAgo(5 * time.Hour)},
			{Author: demoViewer, Path: "internal/search/index.go", Line: 16, OriginalLine: 16, Side: "RIGHT",
				Body:     "Good catch. Fixed with a per-doc seen-set before the append.",
				ThreadID: 1, DatabaseID: 9002, CreatedAt: demoAgo(3*time.Hour + 20*time.Minute)},
			{Author: "jordan", Path: "internal/search/tokenizer.go", Line: 8, OriginalLine: 8, Side: "RIGHT",
				DiffHunk: "@@ -5,6 +5,9 @@ func Tokenize(s string) []string {\n+\t// split on whitespace; callers pre-normalize\n+\tfields := strings.Fields(s)",
				Body:     "nit: lowercase before splitting so Search and search collide on one posting list.",
				ThreadID: 2, ReviewID: "REV_jordan", DatabaseID: 9003, CreatedAt: demoAgo(4*time.Hour + 30*time.Minute)},
		},
		Checks: []Check{
			{Name: "build", Status: "COMPLETED", Conclusion: "SUCCESS"},
			{Name: "test", Status: "COMPLETED", Conclusion: "SUCCESS"},
			{Name: "lint", Status: "COMPLETED", Conclusion: "SUCCESS"},
		},
		Commits: []Commit{
			{SHA: "a1b2c3d", Message: "Add the inverted index core", Author: demoViewer, CreatedAt: demoAgo(2 * 24 * time.Hour)},
			{SHA: "e4f5a6b", Message: "Add Add/Lookup benchmarks", Author: demoViewer, CreatedAt: demoAgo(28 * time.Hour)},
			{SHA: "c7d8e9f", Message: "Dedup postings per doc", Author: demoViewer, CreatedAt: demoAgo(3 * time.Hour)},
		},
		Events: []Event{
			{Type: "REVIEW_REQUESTED", Actor: demoViewer, Subject: "kim", CreatedAt: demoAgo(6 * time.Hour)},
			{Type: "REVIEW_REQUESTED", Actor: demoViewer, Subject: "jordan", CreatedAt: demoAgo(6 * time.Hour)},
		},
	}
}

// demoLightDetail builds a minimal but consistent detail from the board item
// matching number, so any non-#142 PR still opens with real-looking metadata.
func demoLightDetail(number int) PRDetail {
	it := demoItemByNumber(number)
	d := PRDetail{
		Number: number, Title: it.Title, Author: it.Author, State: it.State,
		URL:     "https://github.com/" + it.Repo + "/pull/" + itoa(number),
		BaseRef: it.BaseBranch, HeadRef: it.HeadBranch, HeadSHA: "0f0f0f0",
		IsDraft: it.IsDraft, Additions: 32, Deletions: 6, ChangedFiles: 2,
		CreatedAt: demoAgo(30 * time.Hour),
		Body:      "Demo pull request. Open #142 for the full review + conversation fixture.",
		Checks: []Check{
			{Name: "build", Status: "COMPLETED", Conclusion: checkConclusion(it.Checks)},
			{Name: "test", Status: "COMPLETED", Conclusion: checkConclusion(it.Checks)},
		},
		Commits: []Commit{
			{SHA: "0f0f0f0", Message: it.Title, Author: it.Author, CreatedAt: demoAgo(30 * time.Hour)},
		},
	}
	if d.Title == "" {
		d.Title = "Demo pull request"
	}
	return d
}

func checkConclusion(c CheckState) string {
	switch c {
	case CheckFailure, CheckError:
		return "FAILURE"
	case CheckPending, CheckExpected:
		return "IN_PROGRESS"
	default:
		return "SUCCESS"
	}
}

// demoItemByNumber finds a board item by PR/issue number across every fixture
// set, so light detail and files can reuse its title/branches.
func demoItemByNumber(number int) Item {
	all := append(demoMyPRs(), demoNeedsReview()...)
	all = append(all, demoOrgsMix()...)
	all = append(all, demoClosedTail()...)
	all = append(all, demoInvolved("assignee:@me")...)
	all = append(all, demoInvolved("mentions:@me")...)
	all = append(all, demoInvolved("commenter:@me")...)
	for _, it := range all {
		if it.Number == number {
			return it
		}
	}
	return Item{Number: number, Title: "Demo pull request", Author: demoViewer, State: "OPEN"}
}

// demoFetchPRFiles returns real unified-diff patches. #142's files include the
// path/line that its inline ReviewComments anchor to, so the diff shows the 💬
// badge. Other PRs get a small generic diff so their review pane is not empty.
func demoFetchPRFiles(owner, repo string, number int) ([]FileDiff, error) {
	if number == 142 {
		return []FileDiff{
			{Filename: "internal/search/index.go", Status: "added", Additions: 24, Deletions: 0,
				Patch: "@@ -0,0 +1,24 @@\n" +
					"+package search\n" +
					"+\n" +
					"+// Index is an in-memory inverted index mapping tokens to posting lists.\n" +
					"+type Index struct {\n" +
					"+\tpostings map[string][]int\n" +
					"+}\n" +
					"+\n" +
					"+// NewIndex returns an empty index ready for Add.\n" +
					"+func NewIndex() *Index {\n" +
					"+\treturn &Index{postings: map[string][]int{}}\n" +
					"+}\n" +
					"+\n" +
					"+// Add indexes doc under each of its tokens.\n" +
					"+func (ix *Index) Add(doc int, tokens []string) {\n" +
					"+\tfor _, t := range tokens {\n" +
					"+\t\tix.postings[t] = append(ix.postings[t], doc)\n" +
					"+\t}\n" +
					"+}\n" +
					"+\n" +
					"+// Lookup returns the posting list for a token (nil if absent).\n" +
					"+func (ix *Index) Lookup(token string) []int {\n" +
					"+\treturn ix.postings[token]\n" +
					"+}\n"},
			{Filename: "internal/search/tokenizer.go", Status: "modified", Additions: 3, Deletions: 1,
				Patch: "@@ -5,6 +5,9 @@ func Tokenize(s string) []string {\n" +
					" \tif s == \"\" {\n" +
					" \t\treturn nil\n" +
					" \t}\n" +
					"-\treturn strings.Split(s, \" \")\n" +
					"+\t// split on whitespace; callers pre-normalize\n" +
					"+\tfields := strings.Fields(s)\n" +
					"+\treturn fields\n"},
			{Filename: "internal/search/index_test.go", Status: "added", Additions: 21, Deletions: 0,
				Patch: "@@ -0,0 +1,21 @@\n" +
					"+package search\n" +
					"+\n" +
					"+import \"testing\"\n" +
					"+\n" +
					"+func TestAddLookup(t *testing.T) {\n" +
					"+\tix := NewIndex()\n" +
					"+\tix.Add(1, []string{\"alpha\", \"beta\"})\n" +
					"+\tix.Add(2, []string{\"beta\"})\n" +
					"+\tif got := ix.Lookup(\"beta\"); len(got) != 2 {\n" +
					"+\t\tt.Fatalf(\"beta: want 2 postings, got %d\", len(got))\n" +
					"+\t}\n" +
					"+}\n"},
		}, nil
	}
	return []FileDiff{
		{Filename: "README.md", Status: "modified", Additions: 2, Deletions: 1,
			Patch: "@@ -1,3 +1,4 @@\n" +
				" # northwind\n" +
				"-Internal tooling.\n" +
				"+Internal tooling for the northwind platform.\n" +
				"+See docs/ for the runbooks.\n"},
	}, nil
}

// --- stack maps (branch-keyed; owner/repo ignored so the overlay lines up with
// any throwaway repo the seeder builds) ----------------------------------

func demoOpenPRs(owner, repo string) ([]OpenPR, error) {
	return []OpenPR{
		{Number: 141, Head: "search-api", Base: "main", Draft: false, Mergeable: "MERGEABLE", ReviewDecision: "APPROVED"},
		{Number: 142, Head: "search-index", Base: "search-api", Draft: false, Mergeable: "MERGEABLE", ReviewDecision: "REVIEW_REQUIRED"},
		{Number: 143, Head: "search-ui", Base: "search-index", Draft: true, Mergeable: "UNKNOWN", ReviewDecision: ""},
	}, nil
}

func demoOpenPRsByBranch(owner, repo string) (map[string]PRMergeability, error) {
	prs, _ := demoOpenPRs(owner, repo)
	out := make(map[string]PRMergeability, len(prs))
	for _, p := range prs {
		out[p.Head] = p.Mergeability()
	}
	return out, nil
}

func demoOpenPRNumbersByBranch(owner, repo string) (map[string]int, error) {
	return map[string]int{"search-api": 141, "search-index": 142, "search-ui": 143}, nil
}

// demoLandedPRsByBranch reports nothing landed, for the clean stack shot.
func demoLandedPRsByBranch(owner, repo string, branches []string) (map[string]PRLanding, error) {
	return map[string]PRLanding{}, nil
}

func demoPRsWithBase(owner, repo, base string) ([]int, error) {
	switch base {
	case "main":
		return []int{141}, nil
	case "search-api":
		return []int{142}, nil
	case "search-index":
		return []int{143}, nil
	default:
		return nil, nil
	}
}

// itoa is a tiny local int-to-string to avoid pulling strconv into fixtures.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
