package blueskyapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// FeedItem is one entry from app.bsky.feed.getAuthorFeed. Shape matches the
// previously inline backfill type so the backfill code can swap directly.
type FeedItem struct {
	Post struct {
		URI    string `json:"uri"`
		CID    string `json:"cid"`
		Record struct {
			Embed struct {
				Images []struct {
					Image struct {
						Ref struct {
							Link string `json:"$link"`
						} `json:"ref"`
					} `json:"image"`
				} `json:"images"`
			} `json:"embed"`
		} `json:"record"`
		Embed struct {
			Images []struct {
				Fullsize string `json:"fullsize"`
			} `json:"images"`
		} `json:"embed"`
	} `json:"post"`
}

// FeedPage is one page of an author-feed response.
type FeedPage struct {
	Feed   []FeedItem `json:"feed"`
	Cursor string     `json:"cursor"`
}

// ListAuthorFeed returns a single page of the given actor's feed. Use cursor
// to walk pages; pass "" for the first call. limit is clamped server-side.
func ListAuthorFeed(session *Session, actor, cursor string, limit int) (*FeedPage, error) {
	if session == nil {
		return nil, fmt.Errorf("bluesky: nil session")
	}
	if limit <= 0 {
		limit = 50
	}
	feedURL := fmt.Sprintf("%s?actor=%s&limit=%d", getAuthorFeed, url.QueryEscape(actor), limit)
	if cursor != "" {
		feedURL += "&cursor=" + url.QueryEscape(cursor)
	}

	req, _ := http.NewRequest(http.MethodGet, feedURL, nil)
	req.Header.Set("Authorization", "Bearer "+session.AccessJwt)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, ErrRateLimited
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("bluesky getAuthorFeed status %d body: %s", resp.StatusCode, string(body))
	}

	var page FeedPage
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		return nil, err
	}
	return &page, nil
}

// ListAllAuthorPosts walks every page of an actor's feed and returns the
// flattened list. Convenience wrapper around ListAuthorFeed for the common
// "fetch everything" use case.
func ListAllAuthorPosts(session *Session, actor string) ([]FeedItem, error) {
	var all []FeedItem
	cursor := ""
	for {
		page, err := ListAuthorFeed(session, actor, cursor, 50)
		if err != nil {
			return all, err
		}
		if len(page.Feed) == 0 {
			break
		}
		all = append(all, page.Feed...)
		if page.Cursor == "" {
			break
		}
		cursor = page.Cursor
	}
	return all, nil
}

// --- post thread (replies) ---------------------------------------------------

// ThreadAuthor is the minimal subset of an author embedded in a thread node.
type ThreadAuthor struct {
	Did         string `json:"did"`
	Handle      string `json:"handle"`
	DisplayName string `json:"displayName"`
	Avatar      string `json:"avatar"`
}

// ThreadRecord is the post record (text + createdAt) embedded in a thread
// node.
type ThreadRecord struct {
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"createdAt"`
}

// ThreadPost is the post-shaped half of a thread node.
type ThreadPost struct {
	URI    string       `json:"uri"`
	CID    string       `json:"cid"`
	Author ThreadAuthor `json:"author"`
	Record ThreadRecord `json:"record"`
}

// ThreadView mirrors app.bsky.feed.defs#threadViewPost: one Post plus a list
// of recursive child Replies. The reply tree is walked iteratively by callers
// that want a flat list.
type ThreadView struct {
	Thread struct {
		Post    ThreadPost   `json:"post"`
		Replies []ThreadView `json:"replies"`
	} `json:"thread"`
}

// GetPostThread returns the thread containing the given post URI. depth
// controls the requested reply-depth (Bluesky caps it server-side; pass 1 for
// direct replies only, more for nested).
func GetPostThread(session *Session, uri string, depth int) (*ThreadView, error) {
	if session == nil {
		return nil, fmt.Errorf("bluesky: nil session")
	}
	if uri == "" {
		return nil, fmt.Errorf("bluesky: empty post uri")
	}
	if depth < 0 {
		depth = 0
	}
	apiURL := fmt.Sprintf("%s?uri=%s&depth=%d", baseURL+"/xrpc/app.bsky.feed.getPostThread", url.QueryEscape(uri), depth)
	req, err := http.NewRequest(http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+session.AccessJwt)
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, ErrRateLimited
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("bluesky getPostThread status %d body: %s", resp.StatusCode, string(body))
	}

	var view ThreadView
	if err := json.NewDecoder(resp.Body).Decode(&view); err != nil {
		return nil, fmt.Errorf("bluesky decode thread: %w", err)
	}
	return &view, nil
}
