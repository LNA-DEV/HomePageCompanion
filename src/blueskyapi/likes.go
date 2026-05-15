package blueskyapi

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// Like is a single actor that has liked a post.
type Like struct {
	CreatedAt string `json:"createdAt"`
	Actor     struct {
		Did         string `json:"did"`
		Handle      string `json:"handle"`
		DisplayName string `json:"displayName"`
	} `json:"actor"`
}

// LikesResponse mirrors app.bsky.feed.getLikes one-page response. Callers
// typically receive the aggregated form from ListPostLikes which joins
// all pages.
type LikesResponse struct {
	Uri    string `json:"uri"`
	Cid    string `json:"cid"`
	Likes  []Like `json:"likes"`
	Cursor string `json:"cursor,omitempty"`
}

// ListPostLikes returns every like for a post by paginating through
// app.bsky.feed.getLikes until the cursor empties.
func ListPostLikes(session *Session, uri, cid string) (*LikesResponse, error) {
	if session == nil {
		return nil, fmt.Errorf("bluesky: nil session")
	}
	client := &http.Client{}
	var allLikes []Like
	var result *LikesResponse
	cursor := ""

	for {
		apiURL := fmt.Sprintf("%s?uri=%s&cid=%s&limit=100", getLikesPath, uri, cid)
		if cursor != "" {
			apiURL += "&cursor=" + cursor
		}

		req, err := http.NewRequest(http.MethodGet, apiURL, nil)
		if err != nil {
			return nil, fmt.Errorf("bluesky: build likes request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+session.AccessJwt)
		req.Header.Set("Accept", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("bluesky: call getLikes: %w", err)
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			resp.Body.Close()
			return nil, ErrRateLimited
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("bluesky getLikes status %d", resp.StatusCode)
		}

		var page LikesResponse
		if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("bluesky decode likes: %w", err)
		}
		resp.Body.Close()

		allLikes = append(allLikes, page.Likes...)
		if result == nil {
			result = &page
		}
		if page.Cursor == "" {
			break
		}
		cursor = page.Cursor
	}

	result.Likes = allLikes
	result.Cursor = ""
	return result, nil
}
