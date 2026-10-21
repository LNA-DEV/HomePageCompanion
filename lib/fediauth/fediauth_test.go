package fediauth_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/capability/social"
	"github.com/LNA-DEV/HomePageCompanion/companion"
	"github.com/LNA-DEV/HomePageCompanion/companiontest"
	"github.com/LNA-DEV/HomePageCompanion/lib/fediauth"
)

// holder is an instance that keeps its context, as a module's would.
type holder struct{ ctx companion.Context }

func (h *holder) Provision(ctx companion.Context) error { h.ctx = ctx; return nil }

func init() {
	companion.Register(companion.Info{ID: "fediauth-test", New: func() companion.Instance { return new(holder) }})
}

// server is a Mastodon-style server: app registration, token refresh and
// revoke.
type server struct {
	mu       sync.Mutex
	apps     int
	refresh  int
	revoked  []string
	refuse   bool
	noRevoke bool
	*httptest.Server
}

func newServer(t *testing.T) *server {
	s := &server{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		s.mu.Lock()
		defer s.mu.Unlock()
		switch r.URL.Path {
		case "/api/v1/apps":
			s.apps++
			io.WriteString(w, `{"client_id":"cid","client_secret":"csec"}`)
		case "/oauth/token":
			s.refresh++
			if s.refuse || r.PostForm.Get("grant_type") != "refresh_token" || r.PostForm.Get("client_secret") != "csec" {
				w.WriteHeader(http.StatusBadRequest)
				io.WriteString(w, `{"error":"invalid_grant"}`)
				return
			}
			io.WriteString(w, `{"access_token":"renewed","token_type":"Bearer","expires_in":86400}`)
		case "/oauth/revoke":
			if s.noRevoke {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			s.revoked = append(s.revoked, r.PostForm.Get("token"))
			io.WriteString(w, `{}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func setup(t *testing.T) (companion.Context, *server) {
	h := companiontest.New(t, "modules:\n  fediauth-test: { instances: { main: {} } }\n")
	return companiontest.Instance[*holder](h, "fediauth-test:main").ctx, newServer(t)
}

func TestRegistersTheAppOncePerServerAndScopes(t *testing.T) {
	ctx, s := setup(t)
	c := fediauth.New(ctx, []string{"read", "write"})
	cl, err := c.Client(context.Background(), s.URL+"/", "https://companion.example/cb")
	if err != nil {
		t.Fatal(err)
	}
	if cl.AuthURL != s.URL+"/oauth/authorize" || cl.TokenURL != s.URL+"/oauth/token" || cl.ClientID != "cid" || !cl.PKCE {
		t.Fatalf("client %+v", cl)
	}
	c.Client(context.Background(), s.URL, "https://companion.example/cb")
	if s.apps != 1 {
		t.Fatalf("registered %d times", s.apps)
	}
	// Another callback or other scopes need their own registration.
	c.Client(context.Background(), s.URL, "https://other.example/cb")
	fediauth.New(ctx, []string{"read"}).Client(context.Background(), s.URL, "https://companion.example/cb")
	if s.apps != 3 {
		t.Fatalf("registered %d times, want 3", s.apps)
	}
	if _, err := c.Client(context.Background(), " ", "x"); err == nil {
		t.Fatal("no server accepted")
	}
}

func TestTokenRefreshAndDisconnect(t *testing.T) {
	ctx, s := setup(t)
	c := fediauth.New(ctx, []string{"read", "write"})
	if _, ok, err := c.Token(context.Background()); ok || err != nil {
		t.Fatalf("not connected: %v %v", ok, err)
	}
	cl, _ := c.Client(context.Background(), s.URL, "https://companion.example/cb")
	now := time.Now()
	c.SetClock(func() time.Time { return now })
	c.Connect(cl, companion.OAuthToken{AccessToken: "first", RefreshToken: "r1", ExpiresAt: now.Add(time.Hour)}, "@lna@server", "1")
	if tok, ok, _ := c.Token(context.Background()); !ok || tok != "first" || s.refresh != 0 {
		t.Fatalf("valid token %q, refreshed %d", tok, s.refresh)
	}
	if st := c.State(true); !st.Connected || st.Account != "@lna@server" || st.Note == "" {
		t.Fatalf("state %+v", st)
	}
	// Within ten minutes of its end it is renewed, and the new one kept.
	now = now.Add(55 * time.Minute)
	if tok, _, err := c.Token(context.Background()); tok != "renewed" || err != nil {
		t.Fatalf("refresh %q %v", tok, err)
	}
	if tok, _, _ := c.Token(context.Background()); tok != "renewed" || s.refresh != 1 {
		t.Fatalf("refreshed again: %q %d", tok, s.refresh)
	}

	// The server stops renewing it: an account problem.
	now = now.Add(48 * time.Hour)
	s.refuse = true
	_, _, err := c.Token(context.Background())
	if !errors.Is(err, social.ErrNotConnected) || social.Classify(err).Cause != social.CauseAccount {
		t.Fatalf("refused refresh: %v", err)
	}
	// The server is down: retried later, not the account's fault.
	s.refuse = false
	url := s.URL
	s.Close()
	c.Connect(companion.OAuthClient{TokenURL: url + "/oauth/token", ClientID: "cid", ClientSecret: "csec"},
		companion.OAuthToken{AccessToken: "x", RefreshToken: "r", ExpiresAt: now}, "@lna@server", "1")
	if _, _, err := c.Token(context.Background()); err == nil || errors.Is(err, social.ErrNotConnected) {
		t.Fatalf("network failure as account problem: %v", err)
	}
	// Without a refresh token an expired one needs a new connection.
	c.Connect(cl, companion.OAuthToken{AccessToken: "x", ExpiresAt: now.Add(-time.Hour)}, "@lna@server", "1")
	if _, _, err := c.Token(context.Background()); !errors.Is(err, social.ErrNotConnected) || !strings.Contains(err.Error(), "connect the account again") {
		t.Fatalf("expired without refresh token: %v", err)
	}
}

func TestDisconnectRevokesWhereTheServerCan(t *testing.T) {
	ctx, s := setup(t)
	c := fediauth.New(ctx, []string{"read"})
	cl, _ := c.Client(context.Background(), s.URL, "https://companion.example/cb")
	c.Connect(cl, companion.OAuthToken{AccessToken: "tok"}, "@lna@server", "1")
	if err := c.Disconnect(context.Background(), s.URL); err != nil || len(s.revoked) != 1 || s.revoked[0] != "tok" {
		t.Fatalf("disconnect %v, revoked %v", err, s.revoked)
	}
	if c.State(false).Connected {
		t.Fatal("still connected")
	}
	// Pixelfed has no revoke endpoint: not asked, and forgotten anyway.
	s.noRevoke = true
	c.Revoke = false
	c.Connect(cl, companion.OAuthToken{AccessToken: "tok2"}, "@lna@server", "1")
	if err := c.Disconnect(context.Background(), s.URL); err != nil || len(s.revoked) != 1 || c.State(false).Connected {
		t.Fatalf("disconnect without revoke: %v %v", err, s.revoked)
	}
}
