package gh

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"
)

// Notification is one notification thread with the fields the inbox renders: the
// subject (what), the reason you got it (why), read state, and timestamps. The
// "who" (a comment author, say) isn't in the list payload — it's resolved when
// the preview pane fetches the thread content.
type Notification struct {
	ThreadID   string // REST thread id, used to mark the thread read
	Reason     string // review_requested, mention, comment, author, assign, …
	Unread     bool
	Type       string // PullRequest, Issue, Release, Discussion, CheckSuite, …
	Repo       string // owner/name
	Number     int    // PR/issue number, 0 when the subject has none
	Title      string
	APIURL     string // subject.url (REST API url for the subject)
	LatestURL  string // subject.latest_comment_url — points at the new activity
	UpdatedAt  time.Time
	LastReadAt time.Time
}

// notificationThread is the raw REST shape, shared by the feed and Item mappers.
type notificationThread struct {
	ID         string    `json:"id"`
	Reason     string    `json:"reason"`
	Unread     bool      `json:"unread"`
	UpdatedAt  time.Time `json:"updated_at"`
	LastReadAt time.Time `json:"last_read_at"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	Subject struct {
		Title         string `json:"title"`
		URL           string `json:"url"`
		LatestComment string `json:"latest_comment_url"`
		Type          string `json:"type"`
	} `json:"subject"`
}

// FetchNotificationFeed returns notification threads — both unread and recently
// read (all=true) so the inbox can split them into UNREAD / READ — newest first
// (GitHub's default order). Notifications are REST-only (the one read not on
// GraphQL); the inbox view consumes this richer shape rather than the Item one.
func FetchNotificationFeed(limit int) ([]Notification, error) {
	client, err := api.DefaultRESTClient()
	if err != nil {
		return nil, err
	}

	var raw []notificationThread
	path := fmt.Sprintf("notifications?all=true&per_page=%d", limit)
	if err := client.Get(path, &raw); err != nil {
		return nil, err
	}

	out := make([]Notification, 0, len(raw))
	for _, n := range raw {
		out = append(out, Notification{
			ThreadID:   n.ID,
			Reason:     n.Reason,
			Unread:     n.Unread,
			Type:       n.Subject.Type,
			Repo:       n.Repository.FullName,
			Number:     numberFromAPIURL(n.Subject.URL),
			Title:      n.Subject.Title,
			APIURL:     n.Subject.URL,
			LatestURL:  n.Subject.LatestComment,
			UpdatedAt:  n.UpdatedAt,
			LastReadAt: n.LastReadAt,
		})
	}
	return out, nil
}

// MarkThreadRead marks one notification thread as read (PATCH
// /notifications/threads/{id}). GitHub's API has no mark-as-UNread counterpart.
func MarkThreadRead(threadID string) error {
	client, err := api.DefaultRESTClient()
	if err != nil {
		return err
	}
	return client.Patch("notifications/threads/"+threadID, nil, nil)
}

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

	var notes []notificationThread
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
