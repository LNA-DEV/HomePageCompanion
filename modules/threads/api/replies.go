package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/lib/redact"
)

// Reply is one entry from the Threads /{media-id}/replies endpoint. Only the
// fields the microblog comment importer needs are mapped.
type Reply struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	Permalink string `json:"permalink"`
	Timestamp Time   `json:"timestamp"`
	From      struct {
		ID       string `json:"id"`
		Username string `json:"username"`
	} `json:"from"`
}

// Time is a Graph API timestamp: "2026-10-01T10:00:00+0000", an offset
// without a colon, which time.Time's RFC 3339 does not accept.
type Time struct{ time.Time }

func (t *Time) UnmarshalJSON(b []byte) error {
	s := string(b)
	if s == "null" || s == `""` {
		return nil
	}
	if len(s) >= 2 {
		s = s[1 : len(s)-1]
	}
	for _, layout := range []string{"2006-01-02T15:04:05-0700", time.RFC3339, time.RFC3339Nano} {
		if v, err := time.Parse(layout, s); err == nil {
			t.Time = v
			return nil
		}
	}
	return fmt.Errorf("threads: cannot parse time %q", s)
}

// RepliesResponse is the page shape of /replies. Paging is followed via the
// `next` cursor until exhausted.
type repliesPage struct {
	Data   []Reply `json:"data"`
	Paging struct {
		Next string `json:"next"`
	} `json:"paging"`
}

// ListReplies returns the full reply tree of a Threads media item, following
// pagination cursors. Threads exposes only the immediate replies on this
// endpoint — nested grandchildren are not included.
func ListReplies(mediaID, accessToken string) ([]Reply, error) {
	if mediaID == "" {
		return nil, fmt.Errorf("threads: empty media id")
	}

	endpoint := fmt.Sprintf("%s%s/replies", GraphURL, mediaID)
	params := url.Values{}
	params.Set("fields", "id,text,from,timestamp,permalink")
	params.Set("access_token", accessToken)
	nextURL := endpoint + "?" + params.Encode()

	var all []Reply
	for nextURL != "" {
		page, next, err := fetchRepliesPage(nextURL)
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		nextURL = next
	}
	return all, nil
}

func fetchRepliesPage(pageURL string) ([]Reply, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return nil, "", redact.URLError(err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", redact.URLError(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, "", ErrRateLimited
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, "", fmt.Errorf("threads replies status %d body: %s", resp.StatusCode, string(body))
	}

	var page repliesPage
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		return nil, "", fmt.Errorf("threads decode replies: %w", err)
	}
	return page.Data, page.Paging.Next, nil
}
