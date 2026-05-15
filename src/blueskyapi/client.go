// Package blueskyapi is a self-contained HTTP client for the Bluesky AT
// Protocol surfaces we care about (login, blob upload, post creation,
// per-post likes, author feed pagination). It deals only in primitive
// arguments and returns structured response types; project-internal
// concerns like config.Target or models.AutoUploadItem are kept out so
// the package stays reusable.
package blueskyapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// ErrRateLimited is returned by any operation when Bluesky responds with
// HTTP 429. Consumers can compare with errors.Is to drive a retry loop.
var ErrRateLimited = errors.New("bluesky: rate limited")

// Endpoint constants kept here so call-sites stay focused on the contract.
const (
	baseURL          = "https://bsky.social"
	loginEndpoint    = baseURL + "/xrpc/com.atproto.server.createSession"
	uploadEndpoint   = baseURL + "/xrpc/com.atproto.repo.uploadBlob"
	createRecordPath = baseURL + "/xrpc/com.atproto.repo.createRecord"
	getLikesPath     = baseURL + "/xrpc/app.bsky.feed.getLikes"
	getAuthorFeed    = baseURL + "/xrpc/app.bsky.feed.getAuthorFeed"
)

// Session is the result of a successful Login. Embed in subsequent calls.
type Session struct {
	AccessJwt string `json:"accessJwt"`
	Did       string `json:"did"`
	Handle    string `json:"handle"`
}

// Login authenticates against bsky.social with an app password.
func Login(username, password string) (*Session, error) {
	payload := map[string]string{
		"identifier": username,
		"password":   password,
	}
	body, _ := json.Marshal(payload)

	resp, err := http.Post(loginEndpoint, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, ErrRateLimited
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bluesky login failed, status: %d", resp.StatusCode)
	}

	var session Session
	if err := json.NewDecoder(resp.Body).Decode(&session); err != nil {
		return nil, err
	}
	return &session, nil
}

// Back-compat aliases for the previous package surface so non-refactored
// callers keep compiling during the transition. New code should use the
// shorter names above.
//
// Deprecated: use Session.
type BlueskySession = Session

// Deprecated: use Login.
func BlueskyLogin(username, password string) (*Session, error) { return Login(username, password) }

// PostWebURL composes the human-clickable bsky.app URL for a post given
// the author's handle and the AT URI. Returns "" when either argument is
// empty or the URI has no rkey segment.
//
//	bluesky uri:  at://did:plc:abc.../app.bsky.feed.post/3l5xyz
//	web URL:      https://bsky.app/profile/<handle>/post/3l5xyz
func PostWebURL(handle, uri string) string {
	if handle == "" || uri == "" {
		return ""
	}
	idx := strings.LastIndex(uri, "/")
	if idx < 0 || idx == len(uri)-1 {
		return ""
	}
	rkey := uri[idx+1:]
	return "https://bsky.app/profile/" + handle + "/post/" + rkey
}
