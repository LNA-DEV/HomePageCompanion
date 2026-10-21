package pixelfed_test

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
	"github.com/LNA-DEV/HomePageCompanion/modules/pixelfed"
)

// passport is a Pixelfed server as far as OAuth goes: its tokens expire
// and come with a refresh token.
type passport struct {
	mu        sync.Mutex
	grants    []string
	posted    []string
	refuse    bool
	issued    int
	refreshes int
	*httptest.Server
}

func newPassport(t *testing.T) *passport {
	p := &passport{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/apps", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"id":"3","client_id":"3","client_secret":"psecret"}`)
	})
	mux.HandleFunc("POST /oauth/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		p.mu.Lock()
		defer p.mu.Unlock()
		grant := r.PostForm.Get("grant_type")
		p.grants = append(p.grants, grant)
		if p.refuse || r.PostForm.Get("client_secret") != "psecret" {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":"invalid_grant","error_description":"The refresh token is invalid."}`)
			return
		}
		if grant == "refresh_token" && r.PostForm.Get("refresh_token") != fmt.Sprintf("r%d", p.issued) {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":"invalid_grant"}`)
			return
		}
		p.issued++
		// Five minutes: inside the margin, so every use refreshes it.
		fmt.Fprintf(w, `{"token_type":"Bearer","expires_in":300,"access_token":"a%d","refresh_token":"r%d"}`, p.issued, p.issued)
	})
	mux.HandleFunc("GET /api/v1/accounts/verify_credentials", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"id":"9","username":"lna"}`)
	})
	mux.HandleFunc("/oauth/revoke", func(w http.ResponseWriter, r *http.Request) {
		t.Error("Pixelfed has no revoke endpoint")
	})
	mux.HandleFunc("POST /api/v1/media", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"id":"m1"}`)
	})
	mux.HandleFunc("POST /api/v1/statuses", func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.posted = append(p.posted, r.Header.Get("Authorization"))
		p.mu.Unlock()
		io.WriteString(w, `{"id":"1","url":"https://pixelfed.example/p/lna/1"}`)
	})
	mux.HandleFunc("GET /image.jpg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write([]byte("jpeg"))
	})
	p.Server = httptest.NewServer(mux)
	t.Cleanup(p.Close)
	return p
}

func TestConnectAndRefresh(t *testing.T) {
	p := newPassport(t)
	h := companiontest.New(t, fmt.Sprintf("core: { publicUrl: \"https://companion.example\" }\nmodules:\n  pixelfed:\n    instances:\n      main: { instance: %q, token: old-pat }\n", p.URL))
	a := companiontest.Instance[*pixelfed.Account](h, "pixelfed:main")

	r := companiontest.JSON[struct{ URL string }](h, h.Admin("POST", "/api/b/pixelfed/oauth/start", map[string]string{"instance": "main"}))
	u, _ := url.Parse(r.URL)
	if u.Query().Get("scope") != "read write" || u.Query().Get("client_id") != "3" {
		t.Fatalf("authorisation %s", r.URL)
	}
	if w := h.Public("GET", "/api/b/pixelfed/oauth/callback?code=c&state="+url.QueryEscape(u.Query().Get("state")), nil); w.Code != 200 {
		t.Fatalf("callback %d %s", w.Code, w.Body)
	}
	// The configured token is shadowed, and the settings page says so.
	if st := a.OAuthState(); !st.Connected || !strings.Contains(st.Note, "not used") || st.ExpiresAt == nil {
		t.Fatalf("state %+v", st)
	}

	post := social.Post{Text: "x", Images: []social.Image{{URL: p.URL + "/image.jpg"}}}
	if _, err := a.Publish(context.Background(), post); err != nil {
		t.Fatal(err)
	}
	// The token was about to expire: refreshed first, the new one posts.
	if strings.Join(p.grants, ",") != "authorization_code,refresh_token" || p.posted[0] != "Bearer a2" {
		t.Fatalf("grants %v posted %v", p.grants, p.posted)
	}

	// A server that no longer renews it: an account problem, not a retry.
	p.refuse = true
	_, err := a.Publish(context.Background(), post)
	if !errors.Is(err, social.ErrNotConnected) || social.Classify(err).Cause != social.CauseAccount || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("refused refresh: %v", err)
	}
	if w := h.Admin("POST", "/api/b/pixelfed/oauth/disconnect", map[string]string{"instance": "main"}); w.Code != 200 || a.OAuthState().Connected {
		t.Fatalf("disconnect %d", w.Code)
	}
}
