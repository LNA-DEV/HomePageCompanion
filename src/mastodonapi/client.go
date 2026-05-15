// Package mastodonapi is a self-contained HTTP client for the Mastodon v1
// REST API surfaces the microblog feature needs: media upload, status
// create/delete, like listing, and reply (context) fetch.
package mastodonapi

import (
	"errors"
	"strings"
	"time"
)

// ErrRateLimited is returned by any operation when Mastodon responds 429.
var ErrRateLimited = errors.New("mastodon: rate limited")

// ErrNotFound is returned on HTTP 404. Useful for DeleteStatus where a
// missing remote should be treated as success (idempotent delete).
var ErrNotFound = errors.New("mastodon: not found")

// defaultTimeout caps every HTTP call from this package.
const defaultTimeout = 30 * time.Second

// trimInstance normalises a target's instance URL by removing trailing slashes.
func trimInstance(instance string) string {
	return strings.TrimRight(strings.TrimSpace(instance), "/")
}

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
