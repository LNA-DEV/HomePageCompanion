package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/LNA-DEV/HomePageCompanion/lib/redact"
)

// Me returns the username of the account the access token belongs to.
func Me(ctx context.Context, accessToken string) (string, error) {
	params := url.Values{}
	params.Set("fields", "username")
	params.Set("access_token", accessToken)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, GraphURL+"me", nil)
	if err != nil {
		return "", err
	}
	req.URL.RawQuery = params.Encode()
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", redact.URLError(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return "", ErrRateLimited
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("instagram me status %d body: %s", resp.StatusCode, string(body))
	}

	var res struct {
		Username string `json:"username"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", err
	}
	if res.Username == "" {
		return "", fmt.Errorf("instagram: empty username in me response")
	}
	return res.Username, nil
}
