package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// PublicURL is Bluesky's public AppView, which answers profile lookups
// without a session. Exported so tests can override it.
var PublicURL = "https://public.api.bsky.app"

// Profile identifies an account: its DID, which never changes, and its
// current handle.
type Profile struct {
	Did    string `json:"did"`
	Handle string `json:"handle"`
}

// GetProfile looks up an account by handle or DID.
func GetProfile(ctx context.Context, actor string) (*Profile, error) {
	if actor == "" {
		return nil, fmt.Errorf("bluesky: empty actor")
	}
	endpoint := PublicURL + "/xrpc/app.bsky.actor.getProfile?actor=" + url.QueryEscape(actor)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
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
		return nil, fmt.Errorf("bluesky getProfile %s: status %d body: %s", actor, resp.StatusCode, string(body))
	}

	var p Profile
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return nil, fmt.Errorf("bluesky decode profile: %w", err)
	}
	if p.Did == "" || p.Handle == "" {
		return nil, fmt.Errorf("bluesky: profile %s without did or handle", actor)
	}
	return &p, nil
}
