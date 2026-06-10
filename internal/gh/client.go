// Package gh wraps go-gh's GitHub clients. Per Hard Rule 2, Cairn never reads,
// writes, or stores a token itself — go-gh resolves the token from the user's
// existing `gh` login. Per Hard Rule 3, reads go through GraphQL.
package gh

import "github.com/cli/go-gh/v2/pkg/api"

// Viewer is the authenticated user plus current rate-limit state — exactly what
// the Phase 0 header needs.
type Viewer struct {
	Login         string
	RateRemaining int
	RateLimit     int
}

// FetchViewer issues a single GraphQL query for the logged-in user and the
// current rate-limit quota.
func FetchViewer() (Viewer, error) {
	client, err := api.DefaultGraphQLClient()
	if err != nil {
		return Viewer{}, err
	}

	var query struct {
		Viewer struct {
			Login string
		}
		RateLimit struct {
			Remaining int
			Limit     int
		}
	}
	if err := client.Query("CairnViewer", &query, nil); err != nil {
		return Viewer{}, err
	}

	return Viewer{
		Login:         query.Viewer.Login,
		RateRemaining: query.RateLimit.Remaining,
		RateLimit:     query.RateLimit.Limit,
	}, nil
}
