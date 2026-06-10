package gh

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"
)

// FetchNotifications returns the user's notification threads as board items.
// Notifications are a REST-only concept, so this is the one read that does not
// go through GraphQL; it is mapped into the same Item shape the dashboard
// renders. Review/CI state is not part of the notifications payload, so those
// glyphs render as "none".
func FetchNotifications(limit int) (items []Item, total int, err error) {
	client, err := api.DefaultRESTClient()
	if err != nil {
		return nil, 0, err
	}

	var notes []struct {
		Reason     string    `json:"reason"`
		Unread     bool      `json:"unread"`
		UpdatedAt  time.Time `json:"updated_at"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
		Subject struct {
			Title string `json:"title"`
			URL   string `json:"url"`
			Type  string `json:"type"`
		} `json:"subject"`
	}

	path := fmt.Sprintf("notifications?per_page=%d", limit)
	if err := client.Get(path, &notes); err != nil {
		return nil, 0, err
	}

	items = make([]Item, 0, len(notes))
	for _, n := range notes {
		items = append(items, Item{
			IsPR:      n.Subject.Type == "PullRequest",
			Repo:      n.Repository.FullName,
			Number:    numberFromAPIURL(n.Subject.URL),
			Title:     n.Subject.Title,
			Author:    n.Reason, // surface the reason (review_requested, mention, …) in the author column
			UpdatedAt: n.UpdatedAt,
		})
	}
	return items, len(items), nil
}

// numberFromAPIURL extracts the trailing issue/PR number from a subject URL
// like ".../repos/o/r/pulls/29"; returns 0 when there is no numeric tail
// (e.g. Release/Discussion/CheckSuite notifications).
func numberFromAPIURL(u string) int {
	if u == "" {
		return 0
	}
	last := u[strings.LastIndexByte(u, '/')+1:]
	n, err := strconv.Atoi(last)
	if err != nil {
		return 0
	}
	return n
}
