// Package webfinger looks up fediverse accounts with WebFinger (RFC 7033).
// Mastodon and Pixelfed both answer it without a token, which is why the
// autouploader uses it to turn a profile URL into a canonical acct.
package webfinger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// ErrNotFound is returned when the server does not know the account.
var ErrNotFound = errors.New("webfinger: not found")

// Lookup asks the server at base (scheme://host) for acct ("user@domain")
// and returns the canonical acct from the response's subject, without the
// "acct:" prefix. The subject can differ from the query when the account's
// domain is not the web host.
func Lookup(ctx context.Context, base, acct string) (string, error) {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" || acct == "" {
		return "", fmt.Errorf("webfinger: base and acct required")
	}
	endpoint := base + "/.well-known/webfinger?resource=" + url.QueryEscape("acct:"+acct)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/jrd+json, application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		return "", ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("webfinger %s: status %d", acct, resp.StatusCode)
	}

	var res struct {
		Subject string `json:"subject"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", fmt.Errorf("webfinger decode: %w", err)
	}
	subject, ok := strings.CutPrefix(res.Subject, "acct:")
	if !ok || !strings.Contains(subject, "@") {
		return "", fmt.Errorf("webfinger %s: subject %q is not an acct", acct, res.Subject)
	}
	return subject, nil
}
