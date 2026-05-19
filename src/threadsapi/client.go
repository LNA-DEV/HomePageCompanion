// Package threadsapi is a self-contained HTTP client for the Meta Threads
// Graph API surfaces the autouploader + microblog features use: container
// create/poll/publish for image and text posts, permalink resolution, insights
// (likes), replies listing, and media delete.
//
// Auth is a long-lived OAuth access token attached as a query parameter to
// every call, plus a Threads user id (the "account id" on the target) that
// scopes container creation and publish. Callers pass both per-call rather
// than constructing a stateful client.
package threadsapi

import (
	"errors"
	"time"
)

// ErrRateLimited is returned when Threads responds with HTTP 429.
var ErrRateLimited = errors.New("threads: rate limited")

// ErrNotFound is returned on HTTP 404. DeleteMedia treats it as success so
// the microblog Delete path can be idempotent.
var ErrNotFound = errors.New("threads: not found")

// GraphURL is the base URL for the Threads Graph API used by every operation
// in this package. Exported so callers can override it in tests.
var GraphURL = "https://graph.threads.net/v1.0/"

const defaultTimeout = 30 * time.Second
