package core_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/companion"
	"github.com/LNA-DEV/HomePageCompanion/companiontest"
)

// platform is a fake OAuth provider: it checks the PKCE verifier against
// the challenge the authorisation URL carried.
type platform struct {
	mu         sync.Mutex
	srv        *httptest.Server
	challenges map[string]string // code → challenge
	exchanged  int
}

func newPlatform(t *testing.T) *platform {
	p := &platform{challenges: map[string]string{}}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		p.mu.Lock()
		defer p.mu.Unlock()
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		want, ok := p.challenges[r.PostForm.Get("code")]
		if !ok || base64.RawURLEncoding.EncodeToString(sum[:]) != want || r.PostForm.Get("client_secret") != "sec" {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":"invalid_grant","error_description":"bad code or verifier"}`))
			return
		}
		p.exchanged++
		w.Write([]byte(`{"access_token":"tok-` + r.PostForm.Get("code") + `","token_type":"Bearer","expires_in":3600}`))
	}))
	t.Cleanup(p.srv.Close)
	return p
}

// authorize is what the platform does when the operator agrees: it issues
// a code for the challenge.
func (p *platform) authorize(t *testing.T, authURL, code string) url.Values {
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.challenges[code] = u.Query().Get("code_challenge")
	p.mu.Unlock()
	return u.Query()
}

var oauthTokenURL string

type oauthAccount struct {
	ctx companion.Context
}

func (a *oauthAccount) Provision(ctx companion.Context) error { a.ctx = ctx; return nil }

func (a *oauthAccount) OAuthClient(_ context.Context, redirectURI string) (companion.OAuthClient, error) {
	return companion.OAuthClient{AuthURL: "https://platform.test/authorize", TokenURL: oauthTokenURL, ClientID: "app", ClientSecret: "sec",
		Scopes: []string{"read", "write"}, PKCE: true}, nil
}

func (a *oauthAccount) OAuthConnect(_ context.Context, client companion.OAuthClient, tok companion.OAuthToken) (string, error) {
	return "@me", companion.NewOAuthStore(a.ctx).Save(companion.Connected(client, tok, "@me", "1", time.Now()))
}

func (a *oauthAccount) OAuthState() companion.OAuthState {
	return companion.NewOAuthStore(a.ctx).State()
}

func (a *oauthAccount) OAuthDisconnect(context.Context) error {
	return companion.NewOAuthStore(a.ctx).Delete()
}

func init() {
	companion.Register(companion.Info{ID: "t.oauth", New: func() companion.Instance { return new(oauthAccount) }})
}

func TestOAuthFlow(t *testing.T) {
	p := newPlatform(t)
	oauthTokenURL = p.srv.URL
	h := companiontest.New(t, "core: { publicUrl: \"https://companion.example/\" }\nmodules:\n  t.oauth:\n    instances:\n      main: {}\n")

	if w := h.Public("POST", "/api/b/t.oauth/oauth/start", map[string]string{"instance": "main"}); w.Code != http.StatusUnauthorized {
		t.Fatalf("start without login: %d", w.Code)
	}
	type started struct{ URL, RedirectURI string }
	st := companiontest.JSON[started](h, h.Admin("POST", "/api/b/t.oauth/oauth/start", map[string]string{"instance": "main"}))
	if st.RedirectURI != "https://companion.example/api/b/t.oauth/oauth/callback" {
		t.Fatalf("redirect %q", st.RedirectURI)
	}
	q := p.authorize(t, st.URL, "c1")
	if q.Get("redirect_uri") != st.RedirectURI || q.Get("scope") != "read write" || q.Get("code_challenge_method") != "S256" || len(q.Get("state")) < 30 {
		t.Fatalf("authorisation URL %s", st.URL)
	}

	// The platform sends the browser back, without the session cookie.
	cb := "/api/b/t.oauth/oauth/callback?code=c1&state=" + url.QueryEscape(q.Get("state"))
	w := h.Public("GET", cb, nil)
	body := w.Body.String()
	if w.Code != 200 || !strings.Contains(body, `url=/settings/t.oauth:main?account=%40me&amp;oauth=connected`) || w.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("callback %d %s", w.Code, body)
	}
	view := h.Admin("GET", "/api/core/instances/t.oauth:main", nil).Body.String()
	if !strings.Contains(view, `"oauth":{"connected":true,"account":"@me"`) || strings.Contains(view, "tok-c1") {
		t.Fatalf("instance view %s", view)
	}
	// A state works once.
	if w := h.Public("GET", cb, nil); w.Code != 400 || !strings.Contains(w.Body.String(), "unknown or older than 15 minutes") || p.exchanged != 1 {
		t.Fatalf("second use %d %s", w.Code, w.Body)
	}

	// A refusal by the platform, and a code the token endpoint rejects.
	st = companiontest.JSON[started](h, h.Admin("POST", "/api/b/t.oauth/oauth/start", map[string]string{"instance": "main"}))
	state := p.authorize(t, st.URL, "c2").Get("state")
	if w := h.Public("GET", "/api/b/t.oauth/oauth/callback?error=access_denied&error_description=The+user+denied&state="+url.QueryEscape(state), nil); w.Code != 400 || !strings.Contains(w.Body.String(), "access_denied: The user denied") {
		t.Fatalf("refusal %d %s", w.Code, w.Body)
	}
	st = companiontest.JSON[started](h, h.Admin("POST", "/api/b/t.oauth/oauth/start", map[string]string{"instance": "main"}))
	state = p.authorize(t, st.URL, "c3").Get("state")
	if w := h.Public("GET", "/api/b/t.oauth/oauth/callback?code=forged&state="+url.QueryEscape(state), nil); w.Code != 400 || !strings.Contains(w.Body.String(), "invalid_grant") {
		t.Fatalf("bad code %d %s", w.Code, w.Body)
	}

	if w := h.Admin("POST", "/api/b/t.oauth/oauth/disconnect", map[string]string{"instance": "main"}); w.Code != 200 {
		t.Fatalf("disconnect %d", w.Code)
	}
	if view := h.Admin("GET", "/api/core/instances/t.oauth:main", nil).Body.String(); !strings.Contains(view, `"oauth":{"connected":false}`) {
		t.Fatalf("after disconnect %s", view)
	}
}

func TestOAuthNeedsPublicURL(t *testing.T) {
	h := companiontest.New(t, "core: { publicUrl: \"\" }\nmodules:\n  t.oauth:\n    instances:\n      main: {}\n")
	w := h.Admin("POST", "/api/b/t.oauth/oauth/start", map[string]string{"instance": "main"})
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "core.publicUrl") {
		t.Fatalf("start %d %s", w.Code, w.Body)
	}
	if w := h.Admin("POST", "/api/b/t.oauth/oauth/start", map[string]string{"instance": "other"}); w.Code != http.StatusNotFound {
		t.Fatalf("unknown instance %d", w.Code)
	}
}
