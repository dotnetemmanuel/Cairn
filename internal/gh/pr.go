package gh

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"
)

// FileDiff is one changed file in a PR, with its unified-diff hunks.
type FileDiff struct {
	Filename         string
	PreviousFilename string
	Status           string // added, modified, removed, renamed, changed
	Additions        int
	Deletions        int
	Patch            string // unified diff hunks; empty for binary/too-large files
}

// Comment is an issue-level comment on the PR conversation.
type Comment struct {
	Author     string
	Body       string
	DatabaseID int // REST comment id, for editing your own comment
	CreatedAt  time.Time
}

// Review is a submitted PR review.
type Review struct {
	ID        string // GraphQL node id, so inline comments can group under it
	Author    string
	State     string // APPROVED, CHANGES_REQUESTED, COMMENTED, DISMISSED
	Body      string
	CreatedAt time.Time
}

// Check is one entry in the CI rollup (a check run or a legacy status context).
type Check struct {
	Name       string
	Status     string // for check runs: QUEUED, IN_PROGRESS, COMPLETED
	Conclusion string // SUCCESS, FAILURE, NEUTRAL, CANCELLED, SKIPPED, TIMED_OUT, ACTION_REQUIRED; or status-context state
	URL        string
}

// ReviewComment is an inline comment left on a specific code line.
type ReviewComment struct {
	Author       string
	Body         string
	Path         string
	Line         int
	OriginalLine int    // line at the comment's original commit (matches DiffHunk)
	Side         string // RIGHT (new) or LEFT (old); GitHub's diffSide
	DiffHunk     string // the cited code context GitHub anchors the comment to
	ReviewID     string // the review this inline comment belongs to ("" if none)
	ThreadID     int    // review-thread group (1-based); comments sharing it are one thread, first = anchor. 0 = ungrouped.
	DatabaseID   int    // REST comment id; reply to a thread via any member's id
	CreatedAt    time.Time
}

// AnchorLine is the diff line a comment attaches to. GitHub leaves Line null
// (0 here) for an outdated thread — one it can no longer map onto the head diff —
// keeping only OriginalLine, the position in the commit the comment was made
// against. Prefer the live line, fall back to the original so the comment still
// anchors (and its 💬 badge still shows) instead of vanishing.
func (c ReviewComment) AnchorLine() int {
	if c.Line > 0 {
		return c.Line
	}
	return c.OriginalLine
}

// ReviewRequest is a pending (not-yet-submitted) review request on a PR.
type ReviewRequest struct {
	Name   string // user login or team slug
	IsTeam bool
	IsYou  bool
}

// PRDetail is everything the review pane needs about a single PR.
type PRDetail struct {
	Number         int
	Title          string
	Body           string
	Author         string
	CreatedAt      time.Time
	State          string
	URL            string
	BaseRef        string
	HeadRef        string
	HeadSHA        string
	Additions      int
	Deletions      int
	ChangedFiles   int
	IsDraft        bool
	Comments       []Comment
	Reviews        []Review
	ReviewComments []ReviewComment
	ReviewRequests []ReviewRequest
	Checks         []Check
	Commits        []Commit
	Events         []Event
}

// TimelineKind classifies a conversation entry.
type TimelineKind int

const (
	KindDescription TimelineKind = iota
	KindComment
	KindReview
	KindInline
	KindCommit // a pushed commit
	KindEvent  // a lifecycle event (ready-for-review, review requested, merged, …)
)

// TimelineEntry is one item in the unified, chronological conversation. A
// KindReview entry may carry Children: the thread anchors left as part of that
// review, rendered indented beneath it. A KindInline entry is a review-thread
// anchor: it carries DiffHunk (the cited code context) and may carry Replies —
// the later comments in its thread, rendered indented beneath the anchor.
type TimelineEntry struct {
	Kind       TimelineKind
	Author     string
	Body       string
	State      string          // review state, for KindReview
	Path       string          // for KindInline
	Line       int             // for KindInline
	Side       string          // for KindInline: RIGHT (new) or LEFT (old)
	DiffHunk   string          // for KindInline: the cited code context
	DatabaseID int             // KindInline/KindComment: REST comment id (reply/edit target)
	ReviewID   string          // for KindReview: GraphQL review node id, to edit its body
	Children   []TimelineEntry // for KindReview: its thread anchors
	Replies    []TimelineEntry // for KindInline: the thread's replies, beneath the anchor
	SHA        string          // for KindCommit: the commit sha
	Event      string          // for KindEvent: the event type
	Subject    string          // for KindEvent: e.g. the requested reviewer
	CreatedAt  time.Time
}

// Commit is one commit pushed to the PR, shown inline in the conversation
// timeline in chronological order.
type Commit struct {
	SHA       string
	Message   string // headline (first line of the commit message)
	Author    string
	CreatedAt time.Time
}

// Event is a non-comment PR lifecycle event (ready-for-review, review requested,
// merged, closed, reopened, converted to draft) shown in the timeline.
type Event struct {
	Type      string // READY_FOR_REVIEW | REVIEW_REQUESTED | MERGED | CLOSED | REOPENED | CONVERT_TO_DRAFT
	Actor     string
	Subject   string // for REVIEW_REQUESTED: the requested reviewer
	CreatedAt time.Time
}

// Timeline merges the PR description, conversation comments, review summaries,
// and inline code comments into a single list ordered by creation time. The
// description always leads. Inline comments that belong to a review are nested as
// Children of that review's entry (so a reviewer's batch of suggestions/comments
// renders indented under their one review), rather than scattered chronologically.
func (d PRDetail) Timeline() []TimelineEntry {
	var top []*TimelineEntry
	byReview := map[string]*TimelineEntry{} // review id -> its entry, for nesting

	for _, r := range d.Reviews {
		e := &TimelineEntry{Kind: KindReview, Author: r.Author, Body: r.Body, State: r.State, ReviewID: r.ID, CreatedAt: r.CreatedAt}
		if r.ID != "" {
			byReview[r.ID] = e
		}
		top = append(top, e)
	}
	for _, c := range d.Comments {
		top = append(top, &TimelineEntry{Kind: KindComment, Author: c.Author, Body: c.Body, DatabaseID: c.DatabaseID, CreatedAt: c.CreatedAt})
	}
	for _, c := range d.Commits {
		top = append(top, &TimelineEntry{Kind: KindCommit, Author: c.Author, Body: c.Message, SHA: c.SHA, CreatedAt: c.CreatedAt})
	}
	for _, ev := range d.Events {
		top = append(top, &TimelineEntry{Kind: KindEvent, Author: ev.Actor, Event: ev.Type, Subject: ev.Subject, CreatedAt: ev.CreatedAt})
	}
	// Group inline comments into their review threads. A thread's FIRST comment is
	// the anchor (it carries the citation); later comments are Replies rendered
	// beneath it. The whole thread nests under the review the ANCHOR belongs to —
	// so a reply submitted as its own event threads under the suggestion instead
	// of scattering. Comments with ThreadID 0 are ungrouped (each its own anchor).
	type anchorRef struct {
		e        *TimelineEntry
		reviewID string
	}
	var anchors []anchorRef
	threadAnchor := map[int]*TimelineEntry{} // 1-based thread id -> its anchor entry
	for _, rc := range d.ReviewComments {
		// The diff hunk (and so the citation gutter) is anchored at the comment's
		// original line; show that in the header too, so they agree even when the
		// PR moved on. ReviewComment.Line stays the current line for diff badges.
		line := rc.Line
		if rc.OriginalLine > 0 {
			line = rc.OriginalLine
		}
		entry := TimelineEntry{
			Kind: KindInline, Author: rc.Author, Body: rc.Body,
			Path: rc.Path, Line: line, Side: rc.Side, DiffHunk: rc.DiffHunk,
			DatabaseID: rc.DatabaseID, CreatedAt: rc.CreatedAt,
		}
		// A reply (a later comment in an already-seen thread) folds under its anchor.
		if rc.ThreadID != 0 {
			if a := threadAnchor[rc.ThreadID]; a != nil {
				a.Replies = append(a.Replies, entry)
				continue
			}
		}
		a := entry
		if rc.ThreadID != 0 {
			threadAnchor[rc.ThreadID] = &a
		}
		anchors = append(anchors, anchorRef{e: &a, reviewID: rc.ReviewID})
	}
	// Place each completed thread (anchor + its replies) under its review, or at
	// the top level when the anchor belongs to no surfaced review.
	for _, ar := range anchors {
		if parent := byReview[ar.reviewID]; ar.reviewID != "" && parent != nil {
			parent.Children = append(parent.Children, *ar.e)
		} else {
			top = append(top, ar.e)
		}
	}

	// Drop bare COMMENTED reviews that carry neither a body nor any inline
	// comments (their substance lived elsewhere); keep ones with nested comments.
	kept := top[:0]
	for _, e := range top {
		if e.Kind == KindReview && e.State == "COMMENTED" &&
			strings.TrimSpace(e.Body) == "" && len(e.Children) == 0 {
			continue
		}
		kept = append(kept, e)
	}
	top = kept

	sort.SliceStable(top, func(i, j int) bool { return top[i].CreatedAt.Before(top[j].CreatedAt) })
	// Order a review's thread anchors, and each anchor's replies, by time. Anchors
	// are already first-in-thread by construction; this orders sibling anchors and
	// the reply chain beneath each one.
	sortReplies := func(e *TimelineEntry) {
		sort.SliceStable(e.Replies, func(i, j int) bool {
			return e.Replies[i].CreatedAt.Before(e.Replies[j].CreatedAt)
		})
	}
	for _, e := range top {
		sort.SliceStable(e.Children, func(i, j int) bool {
			return e.Children[i].CreatedAt.Before(e.Children[j].CreatedAt)
		})
		for i := range e.Children {
			sortReplies(&e.Children[i])
		}
		sortReplies(e)
	}

	out := make([]TimelineEntry, 0, len(top)+1)
	out = append(out, TimelineEntry{Kind: KindDescription, Author: d.Author, Body: d.Body, CreatedAt: d.CreatedAt})
	for _, e := range top {
		out = append(out, *e)
	}
	return out
}

const prDetailQuery = `
query($owner:String!,$repo:String!,$number:Int!){
  viewer{login}
  repository(owner:$owner,name:$repo){
    pullRequest(number:$number){
      number title body state url createdAt isDraft
      additions deletions changedFiles
      baseRefName headRefName headRefOid
      author{login}
      reviewRequests(first:20){nodes{requestedReviewer{__typename ... on User{login} ... on Team{slug}}}}
      comments(first:50){nodes{databaseId author{login} body createdAt}}
      reviews(first:50){nodes{id author{login} state body createdAt}}
      reviewThreads(first:50){nodes{path line originalLine diffSide comments(first:20){nodes{databaseId author{login} body createdAt diffHunk pullRequestReview{id}}}}}
      commits(first:100){nodes{commit{oid messageHeadline committedDate author{user{login} name}}}}
      statusCommit: commits(last:1){nodes{commit{statusCheckRollup{contexts(first:100){nodes{
        __typename
        ... on CheckRun{name status conclusion detailsUrl}
        ... on StatusContext{context state targetUrl}
      }}}}}}
      timelineItems(first:100,itemTypes:[READY_FOR_REVIEW_EVENT,REVIEW_REQUESTED_EVENT,MERGED_EVENT,CLOSED_EVENT,REOPENED_EVENT,CONVERT_TO_DRAFT_EVENT]){nodes{
        __typename
        ... on ReadyForReviewEvent{createdAt actor{login}}
        ... on ReviewRequestedEvent{createdAt actor{login} requestedReviewer{__typename ... on User{login} ... on Team{slug}}}
        ... on MergedEvent{createdAt actor{login}}
        ... on ClosedEvent{createdAt actor{login}}
        ... on ReopenedEvent{createdAt actor{login}}
        ... on ConvertToDraftEvent{createdAt actor{login}}
      }}
    }
  }
}`

// FetchPRDetail loads conversation + checks for a PR via GraphQL (Hard Rule 3).
func FetchPRDetail(owner, repo string, number int) (PRDetail, error) {
	client, err := graphQLClient()
	if err != nil {
		return PRDetail{}, err
	}

	var resp struct {
		Viewer     struct{ Login string }
		Repository struct {
			PullRequest struct {
				Number         int
				Title          string
				Body           string
				State          string
				URL            string
				CreatedAt      time.Time
				IsDraft        bool
				Additions      int
				Deletions      int
				ChangedFiles   int
				BaseRefName    string
				HeadRefName    string
				HeadRefOid     string
				Author         struct{ Login string }
				ReviewRequests struct {
					Nodes []struct {
						RequestedReviewer struct {
							Typename string `json:"__typename"`
							Login    string
							Slug     string
						}
					}
				}
				Comments struct {
					Nodes []struct {
						DatabaseID int `json:"databaseId"`
						Author     struct{ Login string }
						Body       string
						CreatedAt  time.Time
					}
				}
				Reviews struct {
					Nodes []struct {
						ID        string
						Author    struct{ Login string }
						State     string
						Body      string
						CreatedAt time.Time
					}
				}
				ReviewThreads struct {
					Nodes []struct {
						Path         string
						Line         int
						OriginalLine int
						DiffSide     string
						Comments     struct {
							Nodes []struct {
								DatabaseID        int `json:"databaseId"`
								Author            struct{ Login string }
								Body              string
								CreatedAt         time.Time
								DiffHunk          string
								PullRequestReview struct{ ID string }
							}
						}
					}
				}
				Commits struct {
					Nodes []struct {
						Commit struct {
							Oid             string
							MessageHeadline string
							CommittedDate   time.Time
							Author          struct {
								User struct{ Login string }
								Name string
							}
						}
					}
				}
				StatusCommit struct {
					Nodes []struct {
						Commit struct {
							StatusCheckRollup *struct {
								Contexts struct {
									Nodes []struct {
										Typename string `json:"__typename"`
										// CheckRun
										Name       string
										Status     string
										Conclusion string
										DetailsURL string `json:"detailsUrl"`
										// StatusContext
										Context   string
										State     string
										TargetURL string `json:"targetUrl"`
									}
								}
							}
						}
					}
				} `json:"statusCommit"`
				TimelineItems struct {
					Nodes []struct {
						Typename          string    `json:"__typename"`
						CreatedAt         time.Time `json:"createdAt"`
						Actor             struct{ Login string }
						RequestedReviewer struct {
							Typename string `json:"__typename"`
							Login    string
							Slug     string
						}
					}
				}
			}
		}
	}

	vars := map[string]interface{}{"owner": owner, "repo": repo, "number": number}
	if err := client.Do(prDetailQuery, vars, &resp); err != nil {
		return PRDetail{}, err
	}

	pr := resp.Repository.PullRequest
	d := PRDetail{
		Number:       pr.Number,
		Title:        pr.Title,
		Body:         pr.Body,
		Author:       pr.Author.Login,
		CreatedAt:    pr.CreatedAt,
		State:        pr.State,
		URL:          pr.URL,
		BaseRef:      pr.BaseRefName,
		HeadRef:      pr.HeadRefName,
		HeadSHA:      pr.HeadRefOid,
		Additions:    pr.Additions,
		Deletions:    pr.Deletions,
		ChangedFiles: pr.ChangedFiles,
		IsDraft:      pr.IsDraft,
	}
	for _, rr := range pr.ReviewRequests.Nodes {
		r := rr.RequestedReviewer
		switch r.Typename {
		case "User":
			d.ReviewRequests = append(d.ReviewRequests, ReviewRequest{
				Name: r.Login, IsYou: strings.EqualFold(r.Login, resp.Viewer.Login),
			})
		case "Team":
			d.ReviewRequests = append(d.ReviewRequests, ReviewRequest{Name: r.Slug, IsTeam: true})
		}
	}
	for _, c := range pr.Comments.Nodes {
		d.Comments = append(d.Comments, Comment{Author: c.Author.Login, Body: c.Body, DatabaseID: c.DatabaseID, CreatedAt: c.CreatedAt})
	}
	for ti, th := range pr.ReviewThreads.Nodes {
		side := th.DiffSide
		if side == "" {
			side = "RIGHT"
		}
		// 1-based so 0 stays the "ungrouped" sentinel. Every comment in this thread
		// shares the id; the first is the anchor, the rest are replies.
		threadID := ti + 1
		for _, c := range th.Comments.Nodes {
			d.ReviewComments = append(d.ReviewComments, ReviewComment{
				Author: c.Author.Login, Body: c.Body, Path: th.Path, Line: th.Line,
				OriginalLine: th.OriginalLine, Side: side,
				DiffHunk: c.DiffHunk, ReviewID: c.PullRequestReview.ID, ThreadID: threadID,
				DatabaseID: c.DatabaseID, CreatedAt: c.CreatedAt,
			})
		}
	}
	for _, r := range pr.Reviews.Nodes {
		// GitHub records a PENDING/empty review per submit; keep meaningful ones.
		if r.State == "" || r.State == "PENDING" {
			continue
		}
		d.Reviews = append(d.Reviews, Review{ID: r.ID, Author: r.Author.Login, State: r.State, Body: r.Body, CreatedAt: r.CreatedAt})
	}
	for _, cn := range pr.Commits.Nodes {
		c := cn.Commit
		author := c.Author.User.Login
		if author == "" {
			author = c.Author.Name
		}
		d.Commits = append(d.Commits, Commit{SHA: c.Oid, Message: c.MessageHeadline, Author: author, CreatedAt: c.CommittedDate})
	}
	// The CI rollup belongs to the tip commit (fetched separately via last:1).
	if len(pr.StatusCommit.Nodes) > 0 {
		if roll := pr.StatusCommit.Nodes[0].Commit.StatusCheckRollup; roll != nil {
			for _, ctx := range roll.Contexts.Nodes {
				if ctx.Typename == "CheckRun" {
					d.Checks = append(d.Checks, Check{Name: ctx.Name, Status: ctx.Status, Conclusion: ctx.Conclusion, URL: ctx.DetailsURL})
				} else {
					d.Checks = append(d.Checks, Check{Name: ctx.Context, Status: "COMPLETED", Conclusion: ctx.State, URL: ctx.TargetURL})
				}
			}
		}
	}
	// Lifecycle events. A merge fires both a MergedEvent and a ClosedEvent; keep
	// only the merge so the timeline doesn't say "merged" and "closed" together.
	merged := false
	for _, t := range pr.TimelineItems.Nodes {
		if t.Typename == "MergedEvent" {
			merged = true
		}
	}
	for _, t := range pr.TimelineItems.Nodes {
		ev := Event{Actor: t.Actor.Login, CreatedAt: t.CreatedAt}
		switch t.Typename {
		case "ReadyForReviewEvent":
			ev.Type = "READY_FOR_REVIEW"
		case "ReviewRequestedEvent":
			ev.Type = "REVIEW_REQUESTED"
			if ev.Subject = t.RequestedReviewer.Login; ev.Subject == "" {
				ev.Subject = t.RequestedReviewer.Slug
			}
		case "MergedEvent":
			ev.Type = "MERGED"
		case "ClosedEvent":
			if merged {
				continue
			}
			ev.Type = "CLOSED"
		case "ReopenedEvent":
			ev.Type = "REOPENED"
		case "ConvertToDraftEvent":
			ev.Type = "CONVERT_TO_DRAFT"
		default:
			continue
		}
		d.Events = append(d.Events, ev)
	}
	return d, nil
}

// FetchPRFiles loads a PR's changed files (with patches) via REST, paginating
// up to maxFiles. The diff is a patch-text read with no clean GraphQL form, so
// it uses the REST files endpoint.
func FetchPRFiles(owner, repo string, number int) ([]FileDiff, error) {
	client, err := api.DefaultRESTClient()
	if err != nil {
		return nil, err
	}

	const perPage = 100
	const maxFiles = 500
	var out []FileDiff
	for page := 1; len(out) < maxFiles; page++ {
		var batch []struct {
			Filename         string `json:"filename"`
			PreviousFilename string `json:"previous_filename"`
			Status           string `json:"status"`
			Additions        int    `json:"additions"`
			Deletions        int    `json:"deletions"`
			Patch            string `json:"patch"`
		}
		path := fmt.Sprintf("repos/%s/%s/pulls/%d/files?per_page=%d&page=%d", owner, repo, number, perPage, page)
		if err := client.Get(path, &batch); err != nil {
			return nil, err
		}
		for _, b := range batch {
			out = append(out, FileDiff(b))
		}
		if len(batch) < perPage {
			break
		}
	}
	return out, nil
}

// AddComment posts an issue-level comment on the PR conversation (REST write).
func AddComment(owner, repo string, number int, body string) error {
	client, err := api.DefaultRESTClient()
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]string{"body": body})
	path := fmt.Sprintf("repos/%s/%s/issues/%d/comments", owner, repo, number)
	return client.Post(path, bytes.NewReader(payload), nil)
}

// SubmitReview submits a PR review (REST write). event is APPROVE,
// REQUEST_CHANGES, or COMMENT.
func SubmitReview(owner, repo string, number int, event, body string) error {
	client, err := api.DefaultRESTClient()
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]string{"event": event, "body": body})
	path := fmt.Sprintf("repos/%s/%s/pulls/%d/reviews", owner, repo, number)
	return client.Post(path, bytes.NewReader(payload), nil)
}

// AddReviewComment posts a single inline comment anchored to a file line — the
// equivalent of GitHub's "Add single comment" button (a COMMENT-event review
// comment). commitID is the PR head SHA; side is RIGHT (new) or LEFT (old).
// Unlike approve/request-changes, this is allowed on your own PR.
func AddReviewComment(owner, repo string, number int, commitID, path string, line int, side, body string) error {
	client, err := api.DefaultRESTClient()
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]interface{}{
		"body":      body,
		"commit_id": commitID,
		"path":      path,
		"line":      line,
		"side":      side,
	})
	endpoint := fmt.Sprintf("repos/%s/%s/pulls/%d/comments", owner, repo, number)
	return client.Post(endpoint, bytes.NewReader(payload), nil)
}

// ReplyToReviewComment posts a reply that threads under an existing inline review
// comment (GitHub's "Reply" on a review thread). commentID is the REST id of any
// comment in the target thread; the reply joins that thread. Allowed on your own
// PR, like AddReviewComment.
func ReplyToReviewComment(owner, repo string, number, commentID int, body string) error {
	client, err := api.DefaultRESTClient()
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]string{"body": body})
	endpoint := fmt.Sprintf("repos/%s/%s/pulls/%d/comments/%d/replies", owner, repo, number, commentID)
	return client.Post(endpoint, bytes.NewReader(payload), nil)
}

// UpdatePRBody edits a pull request's description (REST PATCH). Allowed only for
// the PR author; GitHub returns 403 otherwise, surfaced to the caller.
func UpdatePRBody(owner, repo string, number int, body string) error {
	client, err := api.DefaultRESTClient()
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]string{"body": body})
	path := fmt.Sprintf("repos/%s/%s/pulls/%d", owner, repo, number)
	return client.Patch(path, bytes.NewReader(payload), nil)
}

// UpdateIssueComment edits one of your top-level PR conversation comments (REST
// PATCH). commentID is the comment's REST database id.
func UpdateIssueComment(owner, repo string, commentID int, body string) error {
	client, err := api.DefaultRESTClient()
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]string{"body": body})
	path := fmt.Sprintf("repos/%s/%s/issues/comments/%d", owner, repo, commentID)
	return client.Patch(path, bytes.NewReader(payload), nil)
}

// UpdateReviewComment edits one of your inline code-review comments (REST PATCH).
// commentID is the review comment's REST database id.
func UpdateReviewComment(owner, repo string, commentID int, body string) error {
	client, err := api.DefaultRESTClient()
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]string{"body": body})
	path := fmt.Sprintf("repos/%s/%s/pulls/comments/%d", owner, repo, commentID)
	return client.Patch(path, bytes.NewReader(payload), nil)
}

// UpdateReview edits the top-level body of a review you submitted. Reviews aren't
// PATCHable over REST, so this uses the GraphQL updatePullRequestReview mutation;
// reviewID is the review's GraphQL node id (Review.ID).
func UpdateReview(reviewID, body string) error {
	client, err := graphQLClient()
	if err != nil {
		return err
	}
	if reviewID == "" {
		return fmt.Errorf("this review can't be edited")
	}
	var resp struct {
		UpdatePullRequestReview struct {
			PullRequestReview struct{ ID string }
		}
	}
	mutation := `mutation($id:ID!,$body:String!){updatePullRequestReview(input:{pullRequestReviewId:$id,body:$body}){pullRequestReview{id}}}`
	return client.Do(mutation, map[string]interface{}{"id": reviewID, "body": body}, &resp)
}

// FindPROpenForBranch returns the number of the open PR whose head is branch in
// owner/repo, or 0 when there is none. Used to ship a stack's bottom branch:
// stack mode knows the branch locally but needs its PR number to merge.
func FindPROpenForBranch(owner, repo, branch string) (int, error) {
	client, err := api.DefaultRESTClient()
	if err != nil {
		return 0, err
	}
	var prs []struct{ Number int }
	path := fmt.Sprintf("repos/%s/%s/pulls?head=%s:%s&state=open", owner, repo, owner, branch)
	if err := client.Get(path, &prs); err != nil {
		return 0, err
	}
	if len(prs) == 0 {
		return 0, nil
	}
	return prs[0].Number, nil
}

// CreatePR opens a pull request on GitHub (REST POST): head branch → base branch,
// with title/body and an optional draft flag. It returns the new PR's number and
// URL. The base is the branch the head "points at" — for a stacked PR that's the
// branch below it, read from the local git-town lineage, not the trunk. Common
// failures get a friendlier message than GitHub's raw 422: a head that isn't on
// the remote yet, a base branch that hasn't been pushed, an existing PR, or a
// head with no commits ahead of its base.
func CreatePR(owner, repo, head, base, title, body string, draft bool) (int, string, error) {
	client, err := api.DefaultRESTClient()
	if err != nil {
		return 0, "", err
	}
	payload, _ := json.Marshal(map[string]interface{}{
		"title": title,
		"head":  head,
		"base":  base,
		"body":  body,
		"draft": draft,
	})
	var resp struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
	}
	path := fmt.Sprintf("repos/%s/%s/pulls", owner, repo)
	if err := client.Post(path, bytes.NewReader(payload), &resp); err != nil {
		return 0, "", friendlyCreatePRError(err, head, base)
	}
	return resp.Number, resp.HTMLURL, nil
}

// friendlyCreatePRError translates GitHub's terse 422 validation errors on PR
// creation into guidance that fits Cairn's pedagogical tone.
func friendlyCreatePRError(err error, head, base string) error {
	low := strings.ToLower(err.Error())
	switch {
	case strings.Contains(low, "no commits between"):
		return fmt.Errorf("%s has no commits beyond %s yet — commit something first", head, base)
	case strings.Contains(low, "field 'head'") || strings.Contains(low, "head") && strings.Contains(low, "invalid"):
		return fmt.Errorf("%s isn't on the remote yet — push/sync it before proposing", head)
	case strings.Contains(low, "field 'base'") || strings.Contains(low, "base") && strings.Contains(low, "invalid"):
		return fmt.Errorf("base %s isn't on the remote yet — open its PR (or sync) first", base)
	case strings.Contains(low, "already exist"):
		return fmt.Errorf("a pull request for %s already exists", head)
	default:
		return err
	}
}

// OpenPRNumbersByBranch lists the repo's open PRs and maps each head branch to its
// PR number — so the local stack tree can flag which branches already have a PR.
// It paginates up to a sane cap; identically-headed PRs keep the first seen.
func OpenPRNumbersByBranch(owner, repo string) (map[string]int, error) {
	client, err := api.DefaultRESTClient()
	if err != nil {
		return nil, err
	}
	const perPage = 100
	out := map[string]int{}
	for page := 1; page <= 5; page++ {
		var batch []struct {
			Number int `json:"number"`
			Head   struct {
				Ref string `json:"ref"`
			} `json:"head"`
		}
		path := fmt.Sprintf("repos/%s/%s/pulls?state=open&per_page=%d&page=%d", owner, repo, perPage, page)
		if err := client.Get(path, &batch); err != nil {
			return out, err
		}
		for _, p := range batch {
			if _, seen := out[p.Head.Ref]; !seen {
				out[p.Head.Ref] = p.Number
			}
		}
		if len(batch) < perPage {
			break
		}
	}
	return out, nil
}

// PRMergeability is an open PR's landing readiness, used to gate the ship / ship
// stack actions (and annotate the ship-stack confirmation) so a merge that can't
// succeed is caught before it runs instead of failing mid-op.
type PRMergeability struct {
	Number         int
	Draft          bool
	Mergeable      string // MERGEABLE | CONFLICTING | UNKNOWN (GitHub's mergeable state)
	ReviewDecision string // APPROVED | CHANGES_REQUESTED | REVIEW_REQUIRED | "" (repo doesn't require review)
}

// Blocked reports a HARD blocker we can see up front that WILL make a merge fail:
// the PR is a draft, or it conflicts with its base. These are certain, so the ship
// actions dim on them. Note GitHub's `mergeable` only reflects CONFLICTS — not
// branch protection (failing checks, missing reviews), which we can't reliably
// pre-check, so those are handled at merge time by FriendlyMergeError instead.
// UNKNOWN (GitHub still computing) is deliberately NOT blocked — we allow the
// attempt rather than dim a PR that may well be mergeable.
func (m PRMergeability) Blocked() bool {
	return m.Draft || m.Mergeable == "CONFLICTING"
}

// Reason is a short, human explanation of a hard blocker ("" when not blocked),
// for the dim-and-explain gating and the ship-stack confirmation annotations.
func (m PRMergeability) Reason() string {
	switch {
	case m.Draft:
		return "still a draft"
	case m.Mergeable == "CONFLICTING":
		return "conflicts with its base"
	default:
		return ""
	}
}

// FixHint is the concrete next step that clears a hard blocker ("" when none), so
// the gating never just says "blocked" without telling the user what to do.
func (m PRMergeability) FixHint() string {
	switch {
	case m.Draft:
		return "mark it ready for review, then retry"
	case m.Mergeable == "CONFLICTING":
		// "the stack", not "it": sync --stack rebases the WHOLE stack from any branch
		// (it checks out the conflicting one for you), so the user needn't be on it.
		return "sync (S) to rebase the stack onto the trunk and resolve the conflict, then retry"
	default:
		return ""
	}
}

// Caution is a non-blocking risk worth surfacing BEFORE a merge ("" when none):
// review requirements GitHub may enforce via branch protection. It is not a hard
// block — repo admins can bypass it, and whether review is actually required (and
// required-check state) is only certain at merge time — so this warns rather than
// dims; a real refusal is still caught and explained by FriendlyMergeError.
func (m PRMergeability) Caution() string {
	switch m.ReviewDecision {
	case "CHANGES_REQUESTED":
		return "changes were requested — may block the merge"
	case "REVIEW_REQUIRED":
		return "an approving review is required — may block the merge"
	default:
		return ""
	}
}

const openPRsMergeQuery = `
query($owner:String!,$repo:String!,$cursor:String){
  repository(owner:$owner,name:$repo){
    pullRequests(states:OPEN,first:100,after:$cursor){
      pageInfo{hasNextPage endCursor}
      nodes{number headRefName isDraft mergeable reviewDecision}
    }
  }
}`

// OpenPRsByBranch maps each open PR's head branch to its landing readiness via
// GraphQL (Hard Rule 3). It supersedes OpenPRNumbersByBranch for the stack screen:
// one query yields both the #N flags AND the mergeable/draft/review state the ship
// gating needs. Identically-headed PRs keep the first seen; it paginates fully.
func OpenPRsByBranch(owner, repo string) (map[string]PRMergeability, error) {
	client, err := graphQLClient()
	if err != nil {
		return nil, err
	}
	out := map[string]PRMergeability{}
	var cursor *string
	for {
		var resp struct {
			Repository struct {
				PullRequests struct {
					PageInfo struct {
						HasNextPage bool
						EndCursor   string
					}
					Nodes []struct {
						Number         int
						HeadRefName    string
						IsDraft        bool
						Mergeable      string
						ReviewDecision string
					}
				}
			}
		}
		vars := map[string]interface{}{"owner": owner, "repo": repo, "cursor": (*string)(nil)}
		if cursor != nil {
			vars["cursor"] = *cursor
		}
		if err := client.Do(openPRsMergeQuery, vars, &resp); err != nil {
			return out, err
		}
		for _, n := range resp.Repository.PullRequests.Nodes {
			if _, seen := out[n.HeadRefName]; seen {
				continue
			}
			out[n.HeadRefName] = PRMergeability{
				Number: n.Number, Draft: n.IsDraft,
				Mergeable: n.Mergeable, ReviewDecision: n.ReviewDecision,
			}
		}
		pi := resp.Repository.PullRequests.PageInfo
		if !pi.HasNextPage {
			break
		}
		end := pi.EndCursor
		cursor = &end
	}
	return out, nil
}

// PRLanding records that a branch's most recent pull request has left the open
// state — merged (shipped) or closed (abandoned) on the remote — while the local
// stack still carries the branch. It is the drift signal local stack mode uses to
// warn that the working copy is stale after someone lands the stack remotely (or a
// merge happens via the GitHub UI). Number is that PR.
type PRLanding struct {
	Number int
	Merged bool // true = merged (landed); false = closed without merging
}

// LandedPRsByBranch reports, for each head branch, whether its most recent PR has
// left the open state — merged or closed — the drift signal for local stack mode.
// Branches whose latest PR is still open, or that never had a PR, are omitted, so
// the returned map holds only drifted branches. One REST call per branch (stacks
// are small); a per-branch error skips that branch rather than failing the batch.
func LandedPRsByBranch(owner, repo string, branches []string) (map[string]PRLanding, error) {
	client, err := api.DefaultRESTClient()
	if err != nil {
		return nil, err
	}
	out := map[string]PRLanding{}
	for _, b := range branches {
		if b == "" {
			continue
		}
		var prs []struct {
			Number   int        `json:"number"`
			State    string     `json:"state"`
			MergedAt *time.Time `json:"merged_at"`
		}
		// state=all, newest-updated first, one result: the branch's latest PR, so a
		// reopened-then-merged history reads as merged (its current state).
		path := fmt.Sprintf("repos/%s/%s/pulls?head=%s:%s&state=all&sort=updated&direction=desc&per_page=1", owner, repo, owner, b)
		if err := client.Get(path, &prs); err != nil {
			continue // best-effort: a lookup failure just leaves this branch unflagged
		}
		if len(prs) == 0 || prs[0].State == "open" {
			continue
		}
		out[b] = PRLanding{Number: prs[0].Number, Merged: prs[0].MergedAt != nil}
	}
	return out, nil
}

// MergePR merges a pull request on GitHub (REST PUT). method is "squash",
// "merge", or "rebase" (defaults to squash). This is how a stack's bottom branch
// lands on the trunk; the rest of the stack is re-parented afterwards by sync.
func MergePR(owner, repo string, number int, method string) error {
	client, err := api.DefaultRESTClient()
	if err != nil {
		return err
	}
	if method == "" {
		method = "squash"
	}
	payload, _ := json.Marshal(map[string]string{"merge_method": method})
	path := fmt.Sprintf("repos/%s/%s/pulls/%d/merge", owner, repo, number)
	return client.Put(path, bytes.NewReader(payload), nil)
}

// MarkPRReady takes an open PR out of draft — marks it ready for review — via the
// GraphQL markPullRequestReadyForReview mutation. GitHub's REST API has no
// draft→ready transition, so this resolves the PR's node ID from its number, then
// runs the mutation. Clears the draft blocker that dims ship / stops a whole-stack
// ship, so the author can unblock the merge from inside Cairn.
func MarkPRReady(owner, repo string, number int) error {
	client, err := graphQLClient()
	if err != nil {
		return err
	}
	var idResp struct {
		Repository struct {
			PullRequest struct{ ID string }
		}
	}
	idQuery := `query($owner:String!,$repo:String!,$num:Int!){repository(owner:$owner,name:$repo){pullRequest(number:$num){id}}}`
	if err := client.Do(idQuery, map[string]interface{}{"owner": owner, "repo": repo, "num": number}, &idResp); err != nil {
		return err
	}
	id := idResp.Repository.PullRequest.ID
	if id == "" {
		return fmt.Errorf("could not find PR #%d", number)
	}
	var mResp struct {
		MarkPullRequestReadyForReview struct {
			PullRequest struct{ IsDraft bool }
		}
	}
	mutation := `mutation($id:ID!){markPullRequestReadyForReview(input:{pullRequestId:$id}){pullRequest{isDraft}}}`
	return client.Do(mutation, map[string]interface{}{"id": id}, &mResp)
}

// FriendlyMergeError translates GitHub's terse merge-endpoint failures — branch
// protection, failing required checks, a moved head, a genuine conflict, missing
// permission — into one actionable sentence, so a refused merge always explains
// WHY and what to do next instead of surfacing a raw 405/409. label identifies the
// PR in the message (its branch name). Returns nil for a nil error.
func FriendlyMergeError(err error, label string) error {
	if err == nil {
		return nil
	}
	low := strings.ToLower(err.Error())
	switch {
	case strings.Contains(low, "draft"):
		return fmt.Errorf("%s can't merge: it is still a draft — mark it ready for review, then retry", label)
	case strings.Contains(low, "not mergeable"):
		return fmt.Errorf("%s is not mergeable right now: clear merge conflicts (sync/restack) or satisfy the required reviews and checks, then retry", label)
	case strings.Contains(low, "status check") || strings.Contains(low, "checks have") || strings.Contains(low, "checks are"):
		return fmt.Errorf("%s can't merge: its required status checks haven't passed — wait for CI to go green, then retry", label)
	case strings.Contains(low, "approv") || (strings.Contains(low, "review") && strings.Contains(low, "requir")):
		return fmt.Errorf("%s can't merge: it needs an approving review first", label)
	case strings.Contains(low, "protected") || strings.Contains(low, "branch protection"):
		return fmt.Errorf("%s can't merge: branch protection isn't satisfied (reviews or checks) — resolve those, then retry", label)
	case strings.Contains(low, "head branch was modified") || strings.Contains(low, "base branch was modified") || strings.Contains(low, "409"):
		return fmt.Errorf("%s changed under us (a new push landed) — refresh (r), then retry", label)
	case strings.Contains(low, "not found") || strings.Contains(low, "404"):
		return fmt.Errorf("can't merge %s: its PR wasn't found, or you lack permission to merge it", label)
	default:
		return fmt.Errorf("couldn't merge %s: %w", label, err)
	}
}

// PRsWithBase returns the numbers of open PRs that target base in owner/repo.
// When a stack's bottom branch is shipped, the PRs that targeted it must be
// retargeted to the trunk before its branch is deleted — otherwise deleting the
// branch closes them.
func PRsWithBase(owner, repo, base string) ([]int, error) {
	client, err := api.DefaultRESTClient()
	if err != nil {
		return nil, err
	}
	var prs []struct{ Number int }
	path := fmt.Sprintf("repos/%s/%s/pulls?base=%s&state=open", owner, repo, base)
	if err := client.Get(path, &prs); err != nil {
		return nil, err
	}
	out := make([]int, 0, len(prs))
	for _, p := range prs {
		out = append(out, p.Number)
	}
	return out, nil
}

// RetargetPR changes a PR's base branch (REST PATCH). Used to point a stacked
// PR at the trunk once the branch below it has been merged.
func RetargetPR(owner, repo string, number int, newBase string) error {
	client, err := api.DefaultRESTClient()
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]string{"base": newBase})
	path := fmt.Sprintf("repos/%s/%s/pulls/%d", owner, repo, number)
	return client.Patch(path, bytes.NewReader(payload), nil)
}

// DeleteRemoteBranch deletes a branch on the remote (best-effort: an
// already-deleted branch is not an error). After squash/rebase-merging a stack's
// bottom PR, its branch must be GONE for git town sync to detect the ship —
// deleting the branch and rebasing the children with --onto (dropping the now
// squashed commits) — instead of rebasing the merged branch live (which conflicts
// because the squash commit isn't an ancestor).
func DeleteRemoteBranch(owner, repo, branch string) error {
	client, err := api.DefaultRESTClient()
	if err != nil {
		return err
	}
	path := fmt.Sprintf("repos/%s/%s/git/refs/heads/%s", owner, repo, branch)
	if err := client.Delete(path, nil); err != nil {
		// 404/422 == already gone (e.g. the repo auto-deletes on merge).
		low := strings.ToLower(err.Error())
		if strings.Contains(low, "not found") || strings.Contains(err.Error(), "404") ||
			strings.Contains(err.Error(), "422") {
			return nil
		}
		return err
	}
	return nil
}

// SplitRepo splits an "owner/name" string into its parts.
func SplitRepo(nameWithOwner string) (owner, repo string, ok bool) {
	i := strings.IndexByte(nameWithOwner, '/')
	if i <= 0 || i == len(nameWithOwner)-1 {
		return "", "", false
	}
	return nameWithOwner[:i], nameWithOwner[i+1:], true
}
