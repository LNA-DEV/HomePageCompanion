package threadsapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/LNA-DEV/HomePageCompanion/redact"
)

// RefreshURL is the endpoint that extends a long-lived Threads user token.
// Unlike GraphURL it carries no version. Exported so tests can override it.
var RefreshURL = "https://graph.threads.net/refresh_access_token"

// RefreshedToken is what RefreshToken returns: the new token and how many
// seconds it is valid for (about 60 days).
type RefreshedToken struct {
	AccessToken string
	ExpiresIn   int
}

// RefreshToken trades a long-lived token for a new one valid for 60 days from
// now. Meta only accepts a token that is at least 24 hours old and has not
// expired yet; an expired one cannot be refreshed at all.
func RefreshToken(accessToken string) (RefreshedToken, error) {
	params := url.Values{}
	params.Set("grant_type", "th_refresh_token")
	params.Set("access_token", accessToken)

	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, RefreshURL, nil)
	if err != nil {
		return RefreshedToken{}, err
	}
	req.URL.RawQuery = params.Encode()
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return RefreshedToken{}, redact.URLError(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return RefreshedToken{}, ErrRateLimited
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return RefreshedToken{}, fmt.Errorf("threads refresh token status %d body: %s", resp.StatusCode, string(body))
	}

	var res struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return RefreshedToken{}, fmt.Errorf("threads decode refresh token: %w", err)
	}
	if res.AccessToken == "" || res.ExpiresIn <= 0 {
		return RefreshedToken{}, fmt.Errorf("threads: refresh response without access_token or expires_in")
	}
	return RefreshedToken{AccessToken: res.AccessToken, ExpiresIn: res.ExpiresIn}, nil
}
