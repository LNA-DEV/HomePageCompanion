package mastodonapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// Account is a Mastodon user that has favourited a status (subset of fields).
type Account struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	Acct        string `json:"acct"`
	DisplayName string `json:"display_name"`
	Avatar      string `json:"avatar"`
	URL         string `json:"url"`
}

// LikesResponse aggregates every page of /favourited_by.
type LikesResponse struct {
	Instance string    `json:"instance"`
	StatusID string    `json:"statusId"`
	Accounts []Account `json:"accounts"`
}

// ListStatusLikes returns every account that has favourited the given status.
func ListStatusLikes(instance, pat, statusID string) (*LikesResponse, error) {
	instance = trimInstance(instance)
	if instance == "" || statusID == "" {
		return nil, fmt.Errorf("mastodon: instance and statusID required")
	}

	var all []Account
	nextURL := fmt.Sprintf("%s/api/v1/statuses/%s/favourited_by", instance, url.PathEscape(statusID))

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
			return nil, fmt.Errorf("mastodon favourited_by %s -> %s", nextURL, resp.Status)
		}

		var page []Account
		if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
			resp.Body.Close()
			cancel()
			return nil, fmt.Errorf("mastodon decode likes: %w", err)
		}
		all = append(all, page...)
		nextURL = parseNextLink(resp.Header.Get("Link"))
		resp.Body.Close()
		cancel()
	}

	return &LikesResponse{Instance: instance, StatusID: statusID, Accounts: all}, nil
}
