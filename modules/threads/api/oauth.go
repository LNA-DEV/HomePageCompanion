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

// The Threads login's endpoints; exported so tests can override them. The
// app ID and secret are the Threads app's, not the Meta app's. Threads only
// redirects to https URLs, and never to localhost.
var (
	AuthorizeURL = "https://www.threads.com/oauth/authorize"
	TokenURL     = "https://graph.threads.net/oauth/access_token" // code → short-lived token
	ExchangeURL  = "https://graph.threads.net/access_token"       // short-lived → long-lived (60 days)
)

// Scopes are what the module does: post (content_publish), read likes
// (manage_insights) and replies (read_replies), delete (delete).
var Scopes = []string{"threads_basic", "threads_content_publish", "threads_manage_insights", "threads_read_replies", "threads_delete"}

// ExchangeLongLived trades the short-lived token of a login for a
// long-lived one; it needs the app secret.
func ExchangeLongLived(ctx context.Context, appSecret, shortLived string) (RefreshedToken, error) {
	params := url.Values{"grant_type": {"th_exchange_token"}, "client_secret": {appSecret}, "access_token": {shortLived}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ExchangeURL+"?"+params.Encode(), nil)
	if err != nil {
		return RefreshedToken{}, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return RefreshedToken{}, redact.URLError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return RefreshedToken{}, fmt.Errorf("threads long-lived token status %d body: %s", resp.StatusCode, string(body))
	}
	var res struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return RefreshedToken{}, err
	}
	if res.AccessToken == "" {
		return RefreshedToken{}, fmt.Errorf("threads: no access_token in the long-lived token answer")
	}
	return RefreshedToken{AccessToken: res.AccessToken, ExpiresIn: res.ExpiresIn}, nil
}

// Profile is the account a token belongs to; posts go to /{UserID}/threads.
type Profile struct {
	UserID   string
	Username string
}

// GetProfile asks /me for the account's ID and username.
func GetProfile(ctx context.Context, accessToken string) (Profile, error) {
	params := url.Values{"fields": {"id,username"}, "access_token": {accessToken}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, GraphURL+"me?"+params.Encode(), nil)
	if err != nil {
		return Profile{}, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Profile{}, redact.URLError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return Profile{}, fmt.Errorf("threads me status %d body: %s", resp.StatusCode, string(body))
	}
	var res struct {
		ID       json.RawMessage `json:"id"`
		Username string          `json:"username"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return Profile{}, err
	}
	p := Profile{UserID: idString(res.ID), Username: res.Username}
	if p.UserID == "" || p.Username == "" {
		return Profile{}, fmt.Errorf("threads: no id or username in the me answer")
	}
	return p, nil
}

// idString reads an ID Meta sends as a number or a string.
func idString(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		return n.String()
	}
	return ""
}
