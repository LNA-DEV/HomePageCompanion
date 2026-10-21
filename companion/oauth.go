package companion

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Connecting an instance's account through OAuth (docs/concepts/
// modular-architecture.md §5.8, D15). The core runs the flow: the settings
// page asks POST /api/b/<id>/oauth/start for the authorisation URL, the
// platform sends the browser back to /api/b/<id>/oauth/callback, and the core
// checks the state, exchanges the code (with PKCE where the platform has it)
// and hands the token to the instance. The instance keeps the token and
// refreshes it itself.

// OAuthConnector is an instance whose account can be connected through
// OAuth.
type OAuthConnector interface {
	// OAuthClient names the platform's endpoints, the app and the scopes.
	// redirectURI is the core's callback; an instance whose platform wants
	// an app registered first (Mastodon, Pixelfed) does that here, once per
	// server and redirect URI.
	OAuthClient(ctx context.Context, redirectURI string) (OAuthClient, error)
	// OAuthConnect takes the token of a finished flow: the instance checks
	// it, keeps it in its secrets, uses it from now on and answers the
	// connected account's name.
	OAuthConnect(ctx context.Context, client OAuthClient, tok OAuthToken) (account string, err error)
	// OAuthState says whether an account is connected and which.
	OAuthState() OAuthState
	// OAuthDisconnect forgets the connected account (and revokes its token
	// where the platform allows it); the configured token applies again.
	OAuthDisconnect(ctx context.Context) error
}

// OAuthClient is one app on one platform.
type OAuthClient struct {
	AuthURL      string
	TokenURL     string
	ClientID     string
	ClientSecret string
	Scopes       []string
	// ScopeSep joins the scopes; a space when empty (Meta uses a comma).
	ScopeSep string
	// PKCE sends an S256 code challenge (RFC 7636).
	PKCE bool
	// Params are further query parameters of the authorisation URL.
	Params map[string]string
}

// OAuthToken is a token endpoint's answer.
type OAuthToken struct {
	AccessToken  string    `json:"accessToken"`
	RefreshToken string    `json:"refreshToken,omitempty"`
	TokenType    string    `json:"tokenType,omitempty"`
	Scope        string    `json:"scope,omitempty"`
	ExpiresAt    time.Time `json:"expiresAt,omitempty"` // zero: does not expire
	// Raw is the whole answer, for fields only one platform has
	// (Instagram's user_id).
	Raw json.RawMessage `json:"-"`
}

// Expired reports whether the token is past its expiry, with a margin.
func (t OAuthToken) Expired(now time.Time, margin time.Duration) bool {
	return !t.ExpiresAt.IsZero() && !now.Add(margin).Before(t.ExpiresAt)
}

// OAuthState is what the settings page shows about a connection.
type OAuthState struct {
	Connected bool       `json:"connected"`
	Account   string     `json:"account,omitempty"`
	Since     *time.Time `json:"since,omitempty"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
	// Note explains something the operator should know, e.g. that the
	// token in companion.yaml is not used while an account is connected.
	Note string `json:"note,omitempty"`
}

// OAuthError is a refusal by the token endpoint.
type OAuthError struct {
	Status      int
	Code        string // RFC 6749 "error", e.g. invalid_grant
	Description string
}

func (e *OAuthError) Error() string {
	msg := fmt.Sprintf("token endpoint answered %d", e.Status)
	if e.Code != "" {
		msg += ": " + e.Code
	}
	if e.Description != "" {
		msg += ": " + e.Description
	}
	return msg
}

// NewPKCE returns a code verifier and its S256 challenge.
func NewPKCE() (verifier, challenge string) {
	verifier = RandomToken(32)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

// RandomToken is n random bytes, URL-safe base64.
func RandomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("companion: no randomness: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// AuthCodeURL is the authorisation URL the browser goes to.
func (c OAuthClient) AuthCodeURL(redirectURI, state, challenge string) string {
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", c.ClientID)
	q.Set("redirect_uri", redirectURI)
	if len(c.Scopes) > 0 {
		sep := c.ScopeSep
		if sep == "" {
			sep = " "
		}
		q.Set("scope", strings.Join(c.Scopes, sep))
	}
	q.Set("state", state)
	if c.PKCE && challenge != "" {
		q.Set("code_challenge", challenge)
		q.Set("code_challenge_method", "S256")
	}
	for k, v := range c.Params {
		q.Set(k, v)
	}
	sep := "?"
	if strings.Contains(c.AuthURL, "?") {
		sep = "&"
	}
	return c.AuthURL + sep + q.Encode()
}

// Exchange trades an authorisation code for a token.
func (c OAuthClient) Exchange(ctx context.Context, hc *http.Client, code, redirectURI, verifier string) (OAuthToken, error) {
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirectURI}}
	if c.PKCE && verifier != "" {
		form.Set("code_verifier", verifier)
	}
	return c.tokenRequest(ctx, hc, form)
}

// Refresh trades a refresh token for a new token. A platform that rotates
// refresh tokens answers a new one; otherwise the old one is kept.
func (c OAuthClient) Refresh(ctx context.Context, hc *http.Client, refreshToken string) (OAuthToken, error) {
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refreshToken}}
	if len(c.Scopes) > 0 {
		sep := c.ScopeSep
		if sep == "" {
			sep = " "
		}
		form.Set("scope", strings.Join(c.Scopes, sep))
	}
	tok, err := c.tokenRequest(ctx, hc, form)
	if err == nil && tok.RefreshToken == "" {
		tok.RefreshToken = refreshToken
	}
	return tok, err
}

// tokenRequest posts to the token endpoint with the client's credentials
// in the form (client_secret_post), which every platform here accepts.
func (c OAuthClient) tokenRequest(ctx context.Context, hc *http.Client, form url.Values) (OAuthToken, error) {
	form.Set("client_id", c.ClientID)
	if c.ClientSecret != "" {
		form.Set("client_secret", c.ClientSecret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return OAuthToken{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return OAuthToken{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return OAuthToken{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return OAuthToken{}, tokenError(resp.StatusCode, body)
	}
	return ParseOAuthToken(body, time.Now())
}

func tokenError(status int, body []byte) error {
	e := &OAuthError{Status: status}
	var std struct {
		Error            json.RawMessage `json:"error"`
		ErrorDescription string          `json:"error_description"`
		ErrorMessage     string          `json:"error_message"` // Instagram
		ErrorType        string          `json:"error_type"`
	}
	if json.Unmarshal(body, &std) == nil {
		var code string
		var meta struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    int    `json:"code"`
		}
		switch {
		case json.Unmarshal(std.Error, &code) == nil:
			e.Code, e.Description = code, std.ErrorDescription
		case json.Unmarshal(std.Error, &meta) == nil && meta.Message != "":
			// Graph API: {"error":{"message","type","code"}}
			e.Code, e.Description = meta.Type, fmt.Sprintf("%s (code %d)", meta.Message, meta.Code)
		case std.ErrorMessage != "":
			e.Code, e.Description = std.ErrorType, std.ErrorMessage
		}
	}
	if e.Code == "" && e.Description == "" {
		s := strings.TrimSpace(string(body))
		if len(s) > 300 {
			s = s[:300] + "…"
		}
		e.Description = s
	}
	return e
}

// ParseOAuthToken reads a token endpoint's JSON answer. expires_in may be
// a number or a string; Instagram wraps the token in "data": [...].
func ParseOAuthToken(body []byte, now time.Time) (OAuthToken, error) {
	var raw struct {
		AccessToken  string            `json:"access_token"`
		RefreshToken string            `json:"refresh_token"`
		TokenType    string            `json:"token_type"`
		Scope        json.RawMessage   `json:"scope"`
		ExpiresIn    json.RawMessage   `json:"expires_in"`
		Data         []json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return OAuthToken{}, fmt.Errorf("token endpoint: %w", err)
	}
	if raw.AccessToken == "" && len(raw.Data) > 0 {
		return ParseOAuthToken(raw.Data[0], now)
	}
	if raw.AccessToken == "" {
		return OAuthToken{}, errors.New("token endpoint answered no access_token")
	}
	t := OAuthToken{AccessToken: raw.AccessToken, RefreshToken: raw.RefreshToken, TokenType: raw.TokenType, Raw: json.RawMessage(body)}
	var scope string
	var scopes []string
	switch {
	case json.Unmarshal(raw.Scope, &scope) == nil:
		t.Scope = scope
	case json.Unmarshal(raw.Scope, &scopes) == nil:
		t.Scope = strings.Join(scopes, " ")
	}
	if secs := expiresIn(raw.ExpiresIn); secs > 0 {
		t.ExpiresAt = now.Add(time.Duration(secs) * time.Second)
	}
	return t, nil
}

func expiresIn(v json.RawMessage) int64 {
	var n json.Number
	if json.Unmarshal(v, &n) == nil {
		if i, err := n.Int64(); err == nil {
			return i
		}
	}
	var s string
	if json.Unmarshal(v, &s) == nil {
		i, _ := strconv.ParseInt(s, 10, 64)
		return i
	}
	return 0
}

// OAuthStore keeps an instance's connected account in its module's secrets,
// as one JSON value named "oauth:<instance>".
type OAuthStore struct {
	secrets Secrets
	name    string
}

// ConnectedAccount is what OAuthStore keeps.
type ConnectedAccount struct {
	Token       OAuthToken `json:"token"`
	Account     string     `json:"account"`
	AccountID   string     `json:"accountId,omitempty"`
	ConnectedAt time.Time  `json:"connectedAt"`
	// The app the token was issued to, for refreshing it.
	TokenURL     string `json:"tokenUrl,omitempty"`
	ClientID     string `json:"clientId,omitempty"`
	ClientSecret string `json:"clientSecret,omitempty"`
}

// Connected is the account a finished flow hands to OAuthConnect, with the
// app it was issued to.
func Connected(client OAuthClient, tok OAuthToken, account, accountID string, now time.Time) ConnectedAccount {
	return ConnectedAccount{Token: tok, Account: account, AccountID: accountID, ConnectedAt: now,
		TokenURL: client.TokenURL, ClientID: client.ClientID, ClientSecret: client.ClientSecret}
}

// Client is the app the token was issued to, enough to refresh it.
func (a ConnectedAccount) Client() OAuthClient {
	return OAuthClient{TokenURL: a.TokenURL, ClientID: a.ClientID, ClientSecret: a.ClientSecret}
}

// NewOAuthStore is the store of the instance ctx belongs to.
func NewOAuthStore(ctx Context) OAuthStore {
	return OAuthStore{secrets: ctx.Secrets(), name: "oauth:" + ctx.InstanceName()}
}

// Load returns the connected account; ok is false when there is none.
func (s OAuthStore) Load() (ConnectedAccount, bool, error) {
	v, ok, err := s.secrets.Get(s.name)
	if err != nil || !ok {
		return ConnectedAccount{}, false, err
	}
	var a ConnectedAccount
	if err := json.Unmarshal([]byte(v), &a); err != nil {
		return ConnectedAccount{}, false, fmt.Errorf("stored account: %w", err)
	}
	return a, a.Token.AccessToken != "", nil
}

// Save stores the connected account.
func (s OAuthStore) Save(a ConnectedAccount) error {
	b, err := json.Marshal(a)
	if err != nil {
		return err
	}
	return s.secrets.Put(s.name, string(b))
}

// Delete forgets the connected account.
func (s OAuthStore) Delete() error { return s.secrets.Delete(s.name) }

// State is the OAuthState of what the store holds.
func (s OAuthStore) State() OAuthState {
	a, ok, err := s.Load()
	if err != nil {
		return OAuthState{Note: err.Error()}
	}
	if !ok {
		return OAuthState{}
	}
	st := OAuthState{Connected: true, Account: a.Account, Since: &a.ConnectedAt}
	if !a.Token.ExpiresAt.IsZero() {
		st.ExpiresAt = &a.Token.ExpiresAt
	}
	return st
}
