// Package pixelfedapi is a self-contained HTTP client for the Pixelfed
// (Mastodon-compatible) endpoints we use: media upload, post creation, and
// the per-post favouritedBy listing.
package pixelfedapi

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// ErrRateLimited is returned when Pixelfed responds with HTTP 429.
var ErrRateLimited = errors.New("pixelfed: rate limited")

// defaultTimeout caps every HTTP call from this package.
const defaultTimeout = 30 * time.Second

// parseNextLink extracts the rel="next" URL from a Mastodon-style Link
// header. Returns "" when no next link is present.
func parseNextLink(linkHeader string) string {
	if linkHeader == "" {
		return ""
	}
	for _, part := range strings.Split(linkHeader, ",") {
		part = strings.TrimSpace(part)
		if strings.Contains(part, `rel="next"`) {
			start := strings.Index(part, "<")
			end := strings.Index(part, ">")
			if start != -1 && end != -1 && end > start {
				return part[start+1 : end]
			}
		}
	}
	return ""
}

// InstanceFromPostURL extracts the host portion of a public Pixelfed post URL.
// Useful when a caller only has the post URL and not the instance.
func InstanceFromPostURL(postURL string) (string, error) {
	u, err := url.Parse(postURL)
	if err != nil {
		return "", err
	}
	h := strings.TrimSpace(u.Host)
	if h == "" {
		return "", fmt.Errorf("pixelfed: no host in url")
	}
	return h, nil
}
