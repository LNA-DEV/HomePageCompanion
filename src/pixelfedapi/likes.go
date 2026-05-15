package pixelfedapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
)

// Account is a Pixelfed user that has favourited a post.
type Account struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	Acct        string `json:"acct"`
	DisplayName string `json:"display_name"`
	Avatar      string `json:"avatar"`
	URL         string `json:"url"`
}

// LikesResponse is the aggregated result of all pages of /favourited_by.
type LikesResponse struct {
	Instance string    `json:"instance"`
	PostID   string    `json:"post_id"`
	Accounts []Account `json:"accounts"`
}

// ListPostLikes returns every account that has favourited the given post.
// Pages are followed via the Mastodon-style Link header until the next link
// disappears.
func ListPostLikes(instance, pat, postID string) (*LikesResponse, error) {
	if instance == "" || postID == "" {
		return nil, errors.New("pixelfed: instance and postID are required")
	}
	if pat == "" {
		return nil, errors.New("pixelfed: empty token")
	}

	var all []Account
	nextURL := fmt.Sprintf("https://%s/api/v1/statuses/%s/favourited_by", instance, url.PathEscape(postID))

	for nextURL != "" {
		ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, nextURL, nil)
		if err != nil {
			cancel()
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+pat)
		req.Header.Set("Accept", "application/json")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			cancel()
			return nil, err
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			resp.Body.Close()
			cancel()
			return nil, ErrRateLimited
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			resp.Body.Close()
			cancel()
			return nil, fmt.Errorf("pixelfed favourited_by %s -> %s", nextURL, resp.Status)
		}

		var accounts []Account
		if err := json.NewDecoder(resp.Body).Decode(&accounts); err != nil {
			resp.Body.Close()
			cancel()
			return nil, fmt.Errorf("pixelfed decode response: %w", err)
		}

		all = append(all, accounts...)
		nextURL = parseNextLink(resp.Header.Get("Link"))

		resp.Body.Close()
		cancel()
	}

	return &LikesResponse{Instance: instance, PostID: postID, Accounts: all}, nil
}
