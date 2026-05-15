// Package instagramapi is a self-contained HTTP client for the Instagram
// Graph API surfaces we use (create media container, poll its status, publish
// it, and read like_count).
package instagramapi

import (
	"errors"
	"time"
)

// ErrRateLimited is returned when Instagram responds with HTTP 429.
var ErrRateLimited = errors.New("instagram: rate limited")

// GraphURL is the base URL for the Instagram Graph API used by every
// operation in this package. Exported so callers can override it in tests.
var GraphURL = "https://graph.instagram.com/v22.0/"

const defaultTimeout = 30 * time.Second
