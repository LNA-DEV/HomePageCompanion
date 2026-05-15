package pixelfedapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Post is the publish result on a Pixelfed instance.
type Post struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

// CreatePost publishes a status with the given caption referencing the
// previously uploaded media. Returns the post id/url.
func CreatePost(instance, pat, caption, mediaID string) (*Post, error) {
	if strings.TrimSpace(caption) == "" {
		return nil, fmt.Errorf("pixelfed: empty caption")
	}
	if strings.TrimSpace(mediaID) == "" {
		return nil, fmt.Errorf("pixelfed: empty media id")
	}
	form := url.Values{}
	form.Set("status", caption)
	form.Add("media_ids[]", mediaID)

	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		strings.TrimRight(instance, "/")+"/api/v1/statuses",
		strings.NewReader(form.Encode()),
	)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+pat)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("pixelfed create post: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, ErrRateLimited
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("pixelfed create post failed, status: %d body: %s", resp.StatusCode, string(body))
	}

	var post Post
	if err := json.NewDecoder(resp.Body).Decode(&post); err != nil {
		return nil, fmt.Errorf("pixelfed decode post response: %w", err)
	}
	if post.ID == "" {
		return nil, fmt.Errorf("pixelfed: empty post id in response")
	}
	return &post, nil
}
