package mastodonapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// ContextStatus mirrors a single status returned by /api/v1/statuses/:id/context.
// Only the fields the microblog importer needs are mapped.
type ContextStatus struct {
	ID        string `json:"id"`
	URL       string `json:"url"`
	Content   string `json:"content"`
	CreatedAt time.Time `json:"created_at"`
	Account   struct {
		ID           string `json:"id"`
		Username     string `json:"username"`
		Acct         string `json:"acct"`
		DisplayName  string `json:"display_name"`
		URL          string `json:"url"`
		Avatar       string `json:"avatar"`
		AvatarStatic string `json:"avatar_static"`
	} `json:"account"`
}

// Context is the response shape of /api/v1/statuses/:id/context. Only
// descendants matter for replies.
type Context struct {
	Ancestors   []ContextStatus `json:"ancestors"`
	Descendants []ContextStatus `json:"descendants"`
}

// ListStatusContext returns ancestors + descendants of the given status.
// Microblog import only uses descendants.
func ListStatusContext(instance, pat, statusID string) (*Context, error) {
	instance = trimInstance(instance)
	if instance == "" || statusID == "" {
		return nil, fmt.Errorf("mastodon: instance and statusID required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	endpoint := fmt.Sprintf("%s/api/v1/statuses/%s/context", instance, url.PathEscape(statusID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+pat)
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, ErrRateLimited
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("mastodon context status %d body: %s", resp.StatusCode, string(body))
	}

	var c Context
	if err := json.NewDecoder(resp.Body).Decode(&c); err != nil {
		return nil, fmt.Errorf("mastodon decode context: %w", err)
	}
	return &c, nil
}
