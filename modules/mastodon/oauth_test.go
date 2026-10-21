package mastodon_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/LNA-DEV/HomePageCompanion/capability/social"
	"github.com/LNA-DEV/HomePageCompanion/companiontest"
	"github.com/LNA-DEV/HomePageCompanion/modules/mastodon"
)

// oauthServer is a Mastodon server as far as connecting an account goes.
type oauthServer struct {
	mu       sync.Mutex
	apps     []url.Values
	tokens   []url.Values
	revoked  []string
	statuses []string // the bearer token each status was posted with
	*httptest.Server
}

func newOAuthServer(t *testing.T) *oauthServer {
	s := &oauthServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/apps", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		s.mu.Lock()
		s.apps = append(s.apps, r.PostForm)
		s.mu.Unlock()
		io.WriteString(w, `{"id":"7","name":"HomePageCompanion","client_id":"cid","client_secret":"csecret","redirect_uris":["x"]}`)
	})
	mux.HandleFunc("POST /oauth/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		s.mu.Lock()
		s.tokens = append(s.tokens, r.PostForm)
		s.mu.Unlock()
		if r.PostForm.Get("client_secret") != "csecret" || r.PostForm.Get("code") != "the-code" || r.PostForm.Get("code_verifier") == "" {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":"invalid_grant"}`)
			return
		}
		io.WriteString(w, `{"access_token":"oauth-tok","token_type":"Bearer","scope":"read:accounts read:statuses write:media write:statuses","created_at":1}`)
	})
	mux.HandleFunc("POST /oauth/revoke", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		s.mu.Lock()
		s.revoked = append(s.revoked, r.PostForm.Get("token"))
		s.mu.Unlock()
		io.WriteString(w, `{}`)
	})
	mux.HandleFunc("GET /api/v1/accounts/verify_credentials", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer oauth-tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		io.WriteString(w, `{"id":"42","username":"lna"}`)
	})
	mux.HandleFunc("POST /api/v1/statuses", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.statuses = append(s.statuses, r.Header.Get("Authorization"))
		n := len(s.statuses)
		s.mu.Unlock()
		fmt.Fprintf(w, `{"id":"%d","url":"https://social.example/@lna/%d"}`, n, n)
	})
	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func TestConnectAccountWithOAuth(t *testing.T) {
	s := newOAuthServer(t)
	h := companiontest.New(t, fmt.Sprintf("core: { publicUrl: \"https://companion.example\" }\nmodules:\n  mastodon:\n    instances:\n      main: { instance: %q }\n", s.URL))
	a := companiontest.Instance[*mastodon.Account](h, "mastodon:main")

	// Without a token or a connection, posting fails as an account problem.
	_, err := a.Publish(context.Background(), social.Post{Text: "x"})
	if !errors.Is(err, social.ErrNotConnected) || social.Classify(err).Cause != social.CauseAccount {
		t.Fatalf("unconnected publish: %v", err)
	}

	start := func() url.Values {
		w := h.Admin("POST", "/api/b/mastodon/oauth/start", map[string]string{"instance": "main"})
		r := companiontest.JSON[struct{ URL string }](h, w)
		u, err := url.Parse(r.URL)
		if err != nil || !strings.HasPrefix(r.URL, s.URL+"/oauth/authorize?") {
			t.Fatalf("start %d %s", w.Code, w.Body)
		}
		return u.Query()
	}
	q := start()
	if len(s.apps) != 1 || s.apps[0].Get("redirect_uris") != "https://companion.example/api/b/mastodon/oauth/callback" ||
		s.apps[0].Get("scopes") != "read:accounts read:statuses write:media write:statuses" || s.apps[0].Get("website") != "https://companion.example" {
		t.Fatalf("app registration %v", s.apps)
	}
	if q.Get("client_id") != "cid" || q.Get("scope") != "read:accounts read:statuses write:media write:statuses" || q.Get("code_challenge") == "" {
		t.Fatalf("authorisation %v", q)
	}
	w := h.Public("GET", "/api/b/mastodon/oauth/callback?code=the-code&state="+url.QueryEscape(q.Get("state")), nil)
	host := strings.TrimPrefix(s.URL, "http://")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Connected as @lna@"+host) {
		t.Fatalf("callback %d %s", w.Code, w.Body)
	}

	// From now on the connected token posts.
	if _, err := a.Publish(context.Background(), social.Post{Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	if s.statuses[0] != "Bearer oauth-tok" {
		t.Fatalf("posted with %q", s.statuses[0])
	}
	view := h.Admin("GET", "/api/core/instances/mastodon:main", nil).Body.String()
	if !strings.Contains(view, `"connected":true,"account":"@lna@`+host+`"`) || strings.Contains(view, "oauth-tok") || strings.Contains(view, "csecret") {
		t.Fatalf("instance view %s", view)
	}

	// A second connection reuses the registered app.
	start()
	if len(s.apps) != 1 {
		t.Fatalf("registered again: %d", len(s.apps))
	}

	if w := h.Admin("POST", "/api/b/mastodon/oauth/disconnect", map[string]string{"instance": "main"}); w.Code != 200 {
		t.Fatalf("disconnect %d %s", w.Code, w.Body)
	}
	if len(s.revoked) != 1 || s.revoked[0] != "oauth-tok" {
		t.Fatalf("revoked %v", s.revoked)
	}
	if _, err := a.Publish(context.Background(), social.Post{Text: "x"}); !errors.Is(err, social.ErrNotConnected) {
		t.Fatalf("after disconnect: %v", err)
	}
}

func TestConfiguredTokenStillWorks(t *testing.T) {
	// The settings page says the configured token is unused while an
	// account is connected; without one it is what posts.
	_, f, a := setup(t)
	if st := a.OAuthState(); st.Connected || st.Note != "" {
		t.Fatalf("state %+v", st)
	}
	if _, err := a.Publish(context.Background(), social.Post{Text: "x"}); err != nil || len(f.statuses) != 1 {
		t.Fatalf("publish with the configured token: %v", err)
	}
}
