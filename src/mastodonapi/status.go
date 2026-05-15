package mastodonapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Status is the published-status response from Mastodon's /api/v1/statuses.
// Only the fields we use are mapped.
type Status struct {
	ID  string `json:"id"`
	URL string `json:"url"`
	URI string `json:"uri"`
}

// CreateStatus publishes a status on the configured Mastodon instance.
// visibility defaults to "public" if empty. mediaIDs is optional.
func CreateStatus(instance, pat, body, spoiler string, mediaIDs []string, visibility string) (*Status, error) {
	instance = trimInstance(instance)
	if instance == "" {
		return nil, fmt.Errorf("mastodon: empty instance")
	}
	if strings.TrimSpace(body) == "" && len(mediaIDs) == 0 {
		return nil, fmt.Errorf("mastodon: status requires body or media")
	}
	if visibility == "" {
		visibility = "public"
	}

	form := url.Values{}
	form.Set("status", body)
	form.Set("visibility", visibility)
	if strings.TrimSpace(spoiler) != "" {
		form.Set("spoiler_text", spoiler)
	}
	for _, id := range mediaIDs {
		form.Add("media_ids[]", id)
	}

	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, instance+"/api/v1/statuses", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+pat)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mastodon create status: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, ErrRateLimited
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("mastodon create status: %d body: %s", resp.StatusCode, string(respBody))
	}

	var s Status
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return nil, fmt.Errorf("mastodon decode status: %w", err)
	}
	if s.ID == "" {
		return nil, fmt.Errorf("mastodon: empty status id in response")
	}
	return &s, nil
}

// DeleteStatus removes a status from the configured Mastodon instance.
// HTTP 404 is treated as success (idempotent delete) by returning ErrNotFound
// which the caller may compare with errors.Is.
func DeleteStatus(instance, pat, statusID string) error {
	instance = trimInstance(instance)
	if instance == "" {
		return fmt.Errorf("mastodon: empty instance")
	}
	if statusID == "" {
		return fmt.Errorf("mastodon: empty status id")
	}

	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	endpoint := instance + "/api/v1/statuses/" + url.PathEscape(statusID)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+pat)
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusOK:
		return nil
	case resp.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case resp.StatusCode == http.StatusTooManyRequests:
		return ErrRateLimited
	default:
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("mastodon delete status: %d body: %s", resp.StatusCode, string(respBody))
	}
}
