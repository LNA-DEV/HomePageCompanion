// Package fediauth connects a Mastodon or Pixelfed account through OAuth
// (docs/concepts/modular-architecture.md §5.8, D15). Both servers want an
// app registered first (POST /api/v1/apps); it is registered once per
// server, redirect URI and set of scopes and kept in the module's secrets.
// The connected token lives in the instance's OAuthStore and takes the
// place of the token in companion.yaml while it is connected.
package fediauth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/capability/social"
	"github.com/LNA-DEV/HomePageCompanion/companion"
)

// AppName is how the app is called on the server ("authorised apps").
const AppName = "HomePageCompanion"

// Conn is one instance's connection.
type Conn struct {
	ctx    companion.Context
	scopes []string
	store  companion.OAuthStore
	mu     sync.Mutex
	now    func() time.Time
}

// New prepares the connection of the instance ctx belongs to.
func New(ctx companion.Context, scopes []string) *Conn {
	return &Conn{ctx: ctx, scopes: scopes, store: companion.NewOAuthStore(ctx), now: time.Now}
}

type app struct {
	Server       string   `json:"server"`
	RedirectURI  string   `json:"redirectUri"`
	Scopes       []string `json:"scopes"`
	ClientID     string   `json:"clientId"`
	ClientSecret string   `json:"clientSecret"`
}

func appKey(server, redirect string) string {
	sum := sha256.Sum256([]byte(server + "\n" + redirect))
	return "oauth-app:" + hex.EncodeToString(sum[:8])
}

func trim(server string) string { return strings.TrimRight(strings.TrimSpace(server), "/") }

// Client is the OAuth client on server, registering the app when there is
// none for this redirect URI and these scopes yet.
func (c *Conn) Client(ctx context.Context, server, redirectURI string) (companion.OAuthClient, error) {
	server = trim(server)
	if server == "" {
		return companion.OAuthClient{}, errors.New("no server configured (instance)")
	}
	a, err := c.app(ctx, server, redirectURI)
	if err != nil {
		return companion.OAuthClient{}, err
	}
	return companion.OAuthClient{AuthURL: server + "/oauth/authorize", TokenURL: server + "/oauth/token",
		ClientID: a.ClientID, ClientSecret: a.ClientSecret, Scopes: c.scopes, PKCE: true}, nil
}

func (c *Conn) app(ctx context.Context, server, redirect string) (app, error) {
	key := appKey(server, redirect)
	if v, ok, err := c.ctx.Secrets().Get(key); err == nil && ok {
		var a app
		if json.Unmarshal([]byte(v), &a) == nil && a.ClientID != "" && slices.Equal(a.Scopes, c.scopes) {
			return a, nil
		}
	}
	form := url.Values{"client_name": {AppName}, "redirect_uris": {redirect}, "scopes": {strings.Join(c.scopes, " ")}}
	if site := c.ctx.PublicURL(); site != "" {
		form.Set("website", site)
	}
	body, err := c.post(ctx, server+"/api/v1/apps", form)
	if err != nil {
		return app{}, fmt.Errorf("registering the app on %s: %w", server, err)
	}
	var reg struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	if err := json.Unmarshal(body, &reg); err != nil || reg.ClientID == "" {
		return app{}, fmt.Errorf("registering the app on %s: no client_id in %.200s", server, body)
	}
	a := app{Server: server, RedirectURI: redirect, Scopes: c.scopes, ClientID: reg.ClientID, ClientSecret: reg.ClientSecret}
	b, _ := json.Marshal(a)
	if err := c.ctx.Secrets().Put(key, string(b)); err != nil {
		return app{}, err
	}
	c.ctx.Log().Printf("%s: registered the app on %s", c.ctx.InstanceName(), server)
	return a, nil
}

func (c *Conn) post(ctx context.Context, u string, form url.Values) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := c.ctx.HTTP().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("%d: %.300s", resp.StatusCode, body)
	}
	return body, nil
}

// Connect keeps the token of a finished flow.
func (c *Conn) Connect(client companion.OAuthClient, tok companion.OAuthToken, account, accountID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.store.Save(companion.Connected(client, tok, account, accountID, c.now()))
}

// refreshMargin renews an expiring token this long before it ends.
const refreshMargin = 10 * time.Minute

// Token is the connected account's token, refreshed first when it expires
// and the server gave a refresh token; ok is false when no account is
// connected (then the configured token applies).
func (c *Conn) Token(ctx context.Context) (token string, ok bool, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	a, ok, err := c.store.Load()
	if err != nil || !ok {
		return "", false, err
	}
	if !a.Token.Expired(c.now(), refreshMargin) {
		return a.Token.AccessToken, true, nil
	}
	if a.Token.RefreshToken == "" {
		return "", true, fmt.Errorf("%w: its token expired on %s; connect the account again", social.ErrNotConnected, a.Token.ExpiresAt.Format(time.DateOnly))
	}
	client := a.Client()
	client.Scopes = c.scopes
	tok, err := client.Refresh(ctx, c.ctx.HTTP(), a.Token.RefreshToken)
	var refused *companion.OAuthError
	if errors.As(err, &refused) {
		// The server does not renew it any more: only a new connection helps.
		return "", true, fmt.Errorf("%w: refreshing its token: %w", social.ErrNotConnected, err)
	}
	if err != nil {
		return "", true, fmt.Errorf("refreshing the connected account's token: %w", err)
	}
	a.Token = tok
	if err := c.store.Save(a); err != nil {
		return "", true, err
	}
	c.ctx.Log().Printf("%s: token refreshed", c.ctx.InstanceName())
	return tok.AccessToken, true, nil
}

// State is the connection for the settings page; configured says whether
// companion.yaml has a token too, which is unused while connected.
func (c *Conn) State(configured bool) companion.OAuthState {
	st := c.store.State()
	if st.Connected && configured {
		st.Note = "The token in the settings is not used while the account is connected."
	}
	return st
}

// Disconnect revokes the token on the server, as far as it can, and
// forgets it.
func (c *Conn) Disconnect(ctx context.Context, server string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	a, ok, _ := c.store.Load()
	if ok && a.ClientID != "" {
		form := url.Values{"client_id": {a.ClientID}, "client_secret": {a.ClientSecret}, "token": {a.Token.AccessToken}}
		if _, err := c.post(ctx, trim(server)+"/oauth/revoke", form); err != nil {
			c.ctx.Log().Printf("%s: revoking the token: %v (forgotten anyway)", c.ctx.InstanceName(), err)
		}
	}
	return c.store.Delete()
}

// SetClock replaces the clock; for tests.
func (c *Conn) SetClock(now func() time.Time) { c.now = now }
