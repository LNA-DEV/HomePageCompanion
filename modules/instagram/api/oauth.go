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

// The endpoints of the Instagram API with Instagram Login ("business
// login"); exported so tests can override them. The app ID and secret are
// the Instagram app's, shown under API setup with Instagram login, not the
// Meta app's.
var (
	AuthorizeURL = "https://www.instagram.com/oauth/authorize"
	TokenURL     = "https://api.instagram.com/oauth/access_token" // code → short-lived token (1 hour)
	ExchangeURL  = "https://graph.instagram.com/access_token"     // short-lived → long-lived (60 days)
)

// Scopes are what the module does: read the profile, the media and their
// likes (basic), and publish (content_publish).
var Scopes = []string{"instagram_business_basic", "instagram_business_content_publish"}

// ExchangeLongLived trades the short-lived token of a login for a
// long-lived one; it needs the app secret, so it only runs on the server.
func ExchangeLongLived(ctx context.Context, appSecret, shortLived string) (RefreshedToken, error) {
	params := url.Values{"grant_type": {"ig_exchange_token"}, "client_secret": {appSecret}, "access_token": {shortLived}}
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
		return RefreshedToken{}, fmt.Errorf("instagram long-lived token status %d body: %s", resp.StatusCode, string(body))
	}
	var res struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return RefreshedToken{}, err
	}
	if res.AccessToken == "" {
		return RefreshedToken{}, fmt.Errorf("instagram: no access_token in the long-lived token answer")
	}
	return RefreshedToken{AccessToken: res.AccessToken, ExpiresIn: res.ExpiresIn}, nil
}

// Profile is the account a token belongs to. UserID is the professional
// account's ID, the one media are published under.
type Profile struct {
	UserID   string
	Username string
}

// GetProfile asks /me for the account's ID and username.
func GetProfile(ctx context.Context, accessToken string) (Profile, error) {
	params := url.Values{"fields": {"user_id,username"}, "access_token": {accessToken}}
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
		return Profile{}, fmt.Errorf("instagram me status %d body: %s", resp.StatusCode, string(body))
	}
	var res struct {
		ID       string          `json:"id"`
		UserID   json.RawMessage `json:"user_id"`
		Username string          `json:"username"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return Profile{}, err
	}
	p := Profile{Username: res.Username, UserID: idString(res.UserID)}
	if p.UserID == "" {
		p.UserID = res.ID
	}
	if p.UserID == "" || p.Username == "" {
		return Profile{}, fmt.Errorf("instagram: no user_id or username in the me answer")
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
