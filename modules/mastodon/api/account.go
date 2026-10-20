package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// VerifyCredentials returns the account the token belongs to.
func VerifyCredentials(ctx context.Context, instance, pat string) (*Account, error) {
	instance = trimInstance(instance)
	if instance == "" {
		return nil, fmt.Errorf("mastodon: empty instance")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, instance+"/api/v1/accounts/verify_credentials", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+pat)
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mastodon verify credentials: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, ErrRateLimited
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("mastodon verify credentials: %d body: %s", resp.StatusCode, string(body))
	}

	var a Account
	if err := json.NewDecoder(resp.Body).Decode(&a); err != nil {
		return nil, fmt.Errorf("mastodon decode account: %w", err)
	}
	if a.Username == "" {
		return nil, fmt.Errorf("mastodon: empty username in verify_credentials")
	}
	return &a, nil
}
