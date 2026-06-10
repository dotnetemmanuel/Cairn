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
	Author    string
	Body      string
	CreatedAt time.Time
}

// Review is a submitted PR review.
type Review struct {
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
	Author    string
	Body      string
	Path      string
	Line      int
	CreatedAt time.Time
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
	Additions      int
	Deletions      int
	ChangedFiles   int
	Comments       []Comment
	Reviews        []Review
	ReviewComments []ReviewComment
	Checks         []Check
}

// TimelineKind classifies a conversation entry.
type TimelineKind int

const (
	KindDescription TimelineKind = iota
	KindComment
	KindReview
	KindInline
)

// TimelineEntry is one item in the unified, chronological conversation.
type TimelineEntry struct {
	Kind      TimelineKind
	Author    string
	Body      string
	State     string // review state, for KindReview
	Path      string // for KindInline
	Line      int    // for KindInline
	CreatedAt time.Time
}

// Timeline merges the PR description, conversation comments, review summaries,
// and inline code comments into a single list ordered by creation time. The
// description always leads.
func (d PRDetail) Timeline() []TimelineEntry {
	var rest []TimelineEntry
	for _, c := range d.Comments {
		rest = append(rest, TimelineEntry{Kind: KindComment, Author: c.Author, Body: c.Body, CreatedAt: c.CreatedAt})
	}
	for _, r := range d.Reviews {
		// Skip bare COMMENTED reviews with no body (their substance is the
		// inline comments, surfaced separately).
		if r.State == "COMMENTED" && strings.TrimSpace(r.Body) == "" {
			continue
		}
		rest = append(rest, TimelineEntry{Kind: KindReview, Author: r.Author, Body: r.Body, State: r.State, CreatedAt: r.CreatedAt})
	}
	for _, rc := range d.ReviewComments {
		rest = append(rest, TimelineEntry{Kind: KindInline, Author: rc.Author, Body: rc.Body, Path: rc.Path, Line: rc.Line, CreatedAt: rc.CreatedAt})
	}
	sort.SliceStable(rest, func(i, j int) bool {
		return rest[i].CreatedAt.Before(rest[j].CreatedAt)
	})

	out := make([]TimelineEntry, 0, len(rest)+1)
	out = append(out, TimelineEntry{Kind: KindDescription, Author: d.Author, Body: d.Body, CreatedAt: d.CreatedAt})
	return append(out, rest...)
}

const prDetailQuery = `
query($owner:String!,$repo:String!,$number:Int!){
  repository(owner:$owner,name:$repo){
    pullRequest(number:$number){
      number title body state url createdAt
      additions deletions changedFiles
      baseRefName headRefName
      author{login}
      comments(first:50){nodes{author{login} body createdAt}}
      reviews(first:50){nodes{author{login} state body createdAt}}
      reviewThreads(first:50){nodes{path line comments(first:20){nodes{author{login} body createdAt}}}}
      commits(last:1){nodes{commit{statusCheckRollup{contexts(first:100){nodes{
        __typename
        ... on CheckRun{name status conclusion detailsUrl}
        ... on StatusContext{context state targetUrl}
      }}}}}}
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
		Repository struct {
			PullRequest struct {
				Number       int
				Title        string
				Body         string
				State        string
				URL          string
				CreatedAt    time.Time
				Additions    int
				Deletions    int
				ChangedFiles int
				BaseRefName  string
				HeadRefName  string
				Author       struct{ Login string }
				Comments     struct {
					Nodes []struct {
						Author    struct{ Login string }
						Body      string
						CreatedAt time.Time
					}
				}
				Reviews struct {
					Nodes []struct {
						Author    struct{ Login string }
						State     string
						Body      string
						CreatedAt time.Time
					}
				}
				ReviewThreads struct {
					Nodes []struct {
						Path     string
						Line     int
						Comments struct {
							Nodes []struct {
								Author    struct{ Login string }
								Body      string
								CreatedAt time.Time
							}
						}
					}
				}
				Commits struct {
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
										Context  string
										State    string
										TargetURL string `json:"targetUrl"`
									}
								}
							}
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
		Additions:    pr.Additions,
		Deletions:    pr.Deletions,
		ChangedFiles: pr.ChangedFiles,
	}
	for _, c := range pr.Comments.Nodes {
		d.Comments = append(d.Comments, Comment{Author: c.Author.Login, Body: c.Body, CreatedAt: c.CreatedAt})
	}
	for _, th := range pr.ReviewThreads.Nodes {
		for _, c := range th.Comments.Nodes {
			d.ReviewComments = append(d.ReviewComments, ReviewComment{
				Author: c.Author.Login, Body: c.Body, Path: th.Path, Line: th.Line, CreatedAt: c.CreatedAt,
			})
		}
	}
	for _, r := range pr.Reviews.Nodes {
		// GitHub records a PENDING/empty review per submit; keep meaningful ones.
		if r.State == "" || r.State == "PENDING" {
			continue
		}
		d.Reviews = append(d.Reviews, Review{Author: r.Author.Login, State: r.State, Body: r.Body, CreatedAt: r.CreatedAt})
	}
	if len(pr.Commits.Nodes) > 0 {
		if roll := pr.Commits.Nodes[0].Commit.StatusCheckRollup; roll != nil {
			for _, n := range roll.Contexts.Nodes {
				if n.Typename == "CheckRun" {
					d.Checks = append(d.Checks, Check{Name: n.Name, Status: n.Status, Conclusion: n.Conclusion, URL: n.DetailsURL})
				} else {
					d.Checks = append(d.Checks, Check{Name: n.Context, Status: "COMPLETED", Conclusion: n.State, URL: n.TargetURL})
				}
			}
		}
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

// SplitRepo splits an "owner/name" string into its parts.
func SplitRepo(nameWithOwner string) (owner, repo string, ok bool) {
	i := strings.IndexByte(nameWithOwner, '/')
	if i <= 0 || i == len(nameWithOwner)-1 {
		return "", "", false
	}
	return nameWithOwner[:i], nameWithOwner[i+1:], true
}
