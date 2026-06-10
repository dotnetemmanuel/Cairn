package gh

import "time"

// ReviewState mirrors GitHub's PR reviewDecision (may be empty when no review
// is required or the item is an issue).
type ReviewState string

const (
	ReviewApproved         ReviewState = "APPROVED"
	ReviewChangesRequested ReviewState = "CHANGES_REQUESTED"
	ReviewRequired         ReviewState = "REVIEW_REQUIRED"
	ReviewNone             ReviewState = ""
)

// CheckState mirrors GitHub's statusCheckRollup state (empty when there are no
// checks).
type CheckState string

const (
	CheckSuccess CheckState = "SUCCESS"
	CheckFailure CheckState = "FAILURE"
	CheckError   CheckState = "ERROR"
	CheckPending CheckState = "PENDING"
	CheckExpected CheckState = "EXPECTED"
	CheckNone    CheckState = ""
)

// Item is one row in a board section — a PR or an issue with the fields the
// dashboard renders.
type Item struct {
	IsPR      bool
	Repo      string // owner/name
	Number    int
	Title     string
	Author    string
	URL       string
	IsDraft   bool
	State     string // OPEN / CLOSED / MERGED
	Review    ReviewState
	Checks    CheckState
	UpdatedAt time.Time
}

// searchQuery fills an entire board section in one round-trip: items plus
// review decision plus CI rollup. Reads go through GraphQL (Hard Rule 3).
const searchQuery = `
query($q: String!, $n: Int!) {
  search(query: $q, type: ISSUE, first: $n) {
    issueCount
    nodes {
      __typename
      ... on PullRequest {
        number title url isDraft state updatedAt
        author { login }
        repository { nameWithOwner }
        reviewDecision
        commits(last: 1) { nodes { commit { statusCheckRollup { state } } } }
      }
      ... on Issue {
        number title url state updatedAt
        author { login }
        repository { nameWithOwner }
      }
    }
  }
}`

type searchResponse struct {
	Search struct {
		IssueCount int `json:"issueCount"`
		Nodes      []struct {
			Typename   string `json:"__typename"`
			Number     int
			Title      string
			URL        string
			IsDraft    bool
			State      string
			UpdatedAt  time.Time
			Author     struct{ Login string }
			Repository struct{ NameWithOwner string }
			ReviewDecision string
			Commits        struct {
				Nodes []struct {
					Commit struct {
						StatusCheckRollup *struct{ State string }
					}
				}
			}
		}
	}
}

// SearchItems runs a GitHub search (GitHub search syntax) and returns the
// matching items plus the total match count (which may exceed len(items) when
// capped by limit).
func SearchItems(filter string, limit int) (items []Item, total int, err error) {
	client, err := graphQLClient()
	if err != nil {
		return nil, 0, err
	}

	var resp searchResponse
	vars := map[string]interface{}{"q": filter, "n": limit}
	if err := client.Do(searchQuery, vars, &resp); err != nil {
		return nil, 0, err
	}

	items = make([]Item, 0, len(resp.Search.Nodes))
	for _, n := range resp.Search.Nodes {
		it := Item{
			IsPR:      n.Typename == "PullRequest",
			Repo:      n.Repository.NameWithOwner,
			Number:    n.Number,
			Title:     n.Title,
			Author:    n.Author.Login,
			URL:       n.URL,
			IsDraft:   n.IsDraft,
			State:     n.State,
			Review:    ReviewState(n.ReviewDecision),
			UpdatedAt: n.UpdatedAt,
		}
		if len(n.Commits.Nodes) > 0 {
			if roll := n.Commits.Nodes[0].Commit.StatusCheckRollup; roll != nil {
				it.Checks = CheckState(roll.State)
			}
		}
		items = append(items, it)
	}
	return items, resp.Search.IssueCount, nil
}
