package instagramapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// MediaLikeCount returns the current `like_count` for a published media id.
func MediaLikeCount(mediaID, accessToken string) (int, error) {
	if mediaID == "" {
		return 0, fmt.Errorf("instagram: empty media id")
	}
	endpoint := fmt.Sprintf("%s%s", GraphURL, mediaID)
	params := url.Values{}
	params.Set("fields", "like_count")
	params.Set("access_token", accessToken)

	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, err
	}
	req.URL.RawQuery = params.Encode()
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return 0, ErrRateLimited
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("instagram like_count status %d body: %s", resp.StatusCode, string(body))
	}

	var res struct {
		LikeCount int    `json:"like_count"`
		ID        string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return 0, fmt.Errorf("instagram decode like_count: %w", err)
	}
	return res.LikeCount, nil
}
