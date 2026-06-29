// Package gh wraps go-gh's GitHub clients. Per Hard Rule 2, Cairn never reads,
// writes, or stores a token itself — go-gh resolves the token from the user's
// existing `gh` login. Per Hard Rule 3, reads go through GraphQL.
package gh

import "github.com/cli/go-gh/v2/pkg/api"

// graphQLClient returns a GraphQL client whose token go-gh resolves from the
// user's existing gh login (Hard Rule 2 — Cairn never handles tokens itself).
func graphQLClient() (*api.GraphQLClient, error) {
	return api.DefaultGraphQLClient()
}

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
	client, err := graphQLClient()
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

// FetchOrgs returns the login names of the organizations the authenticated user
// belongs to (including private memberships, since the query runs as the
// viewer). Order follows GitHub's default; the Orgs tab groups PRs by these.
func FetchOrgs() ([]string, error) {
	client, err := graphQLClient()
	if err != nil {
		return nil, err
	}

	var query struct {
		Viewer struct {
			Organizations struct {
				Nodes []struct{ Login string }
			} `graphql:"organizations(first: 100)"`
		}
	}
	if err := client.Query("CairnOrgs", &query, nil); err != nil {
		return nil, err
	}

	orgs := make([]string, 0, len(query.Viewer.Organizations.Nodes))
	for _, n := range query.Viewer.Organizations.Nodes {
		if n.Login != "" {
			orgs = append(orgs, n.Login)
		}
	}
	return orgs, nil
}
