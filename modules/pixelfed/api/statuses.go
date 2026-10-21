package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Status is one of the account's own statuses, as the backfill needs it.
type Status struct {
	ID          string    `json:"id"`
	URL         string    `json:"url"`
	Content     string    `json:"content"`
	CreatedAt   time.Time `json:"created_at"`
	MediaAttach []struct {
		URL string `json:"url"`
	} `json:"media_attachments"`
}

// ListOwnStatuses returns every status of the token's account, newest first.
func ListOwnStatuses(ctx context.Context, instance, pat string) ([]Status, error) {
	acc, err := VerifyCredentials(ctx, instance, pat)
	if err != nil {
		return nil, err
	}
	var all []Status
	maxID := ""
	for {
		u := fmt.Sprintf("%s/api/v1/accounts/%s/statuses?limit=40", strings.TrimRight(strings.TrimSpace(instance), "/"), url.PathEscape(acc.ID))
		if maxID != "" {
			u += "&max_id=" + url.QueryEscape(maxID)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return all, err
		}
		req.Header.Set("Authorization", "Bearer "+pat)
		req.Header.Set("Accept", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return all, err
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			resp.Body.Close()
			return all, ErrRateLimited
		}
		var page []Status
		err = json.NewDecoder(resp.Body).Decode(&page)
		resp.Body.Close()
		if err != nil {
			return all, err
		}
		if len(page) == 0 {
			return all, nil
		}
		all = append(all, page...)
		maxID = page[len(page)-1].ID
	}
}
