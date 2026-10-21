package instagram_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/capability/social"
	"github.com/LNA-DEV/HomePageCompanion/companiontest"
	"github.com/LNA-DEV/HomePageCompanion/modules/instagram"
	"github.com/LNA-DEV/HomePageCompanion/modules/instagram/api"
)

// meta is the login side of Instagram: the code exchange, the long-lived
// token and /me, plus the media calls of one account.
type meta struct {
	mu       sync.Mutex
	codes    []url.Values
	exchange []url.Values
	posted   []string // "<account>/<token>" of each container
	*httptest.Server
}

func newMeta(t *testing.T) *meta {
	m := &meta{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		m.mu.Lock()
		m.codes = append(m.codes, r.PostForm)
		m.mu.Unlock()
		if r.PostForm.Get("client_secret") != "ig-secret" || r.PostForm.Get("code") != "ig-code" {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error_type":"OAuthException","code":400,"error_message":"Invalid platform app"}`)
			return
		}
		io.WriteString(w, `{"data":[{"access_token":"short","user_id":"9999","permissions":"instagram_business_basic,instagram_business_content_publish"}]}`)
	})
	mux.HandleFunc("GET /access_token", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.exchange = append(m.exchange, r.URL.Query())
		m.mu.Unlock()
		io.WriteString(w, `{"access_token":"long","token_type":"bearer","expires_in":5183944}`)
	})
	mux.HandleFunc("GET /me", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("access_token") != "long" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		io.WriteString(w, `{"user_id":"17841","username":"lna.dev","id":"9999"}`)
	})
	mux.HandleFunc("POST /17841/media", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.posted = append(m.posted, "17841/"+r.URL.Query().Get("access_token"))
		m.mu.Unlock()
		io.WriteString(w, `{"id":"c1"}`)
	})
	mux.HandleFunc("GET /c1", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"status_code":"FINISHED"}`) })
	mux.HandleFunc("POST /17841/media_publish", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"id":"m1"}`) })
	m.Server = httptest.NewServer(mux)
	prev := []string{api.GraphURL, api.TokenURL, api.ExchangeURL}
	api.GraphURL, api.TokenURL, api.ExchangeURL = m.URL+"/", m.URL+"/oauth/access_token", m.URL+"/access_token"
	t.Cleanup(func() { api.GraphURL, api.TokenURL, api.ExchangeURL = prev[0], prev[1], prev[2]; m.Close() })
	return m
}

func TestConnectWithInstagramLogin(t *testing.T) {
	m := newMeta(t)
	h := companiontest.New(t, "core: { publicUrl: \"https://companion.example\" }\nmodules:\n  instagram:\n    instances:\n      main: { appId: \"ig-app\", appSecret: ig-secret }\n")
	a := companiontest.Instance[*instagram.Account](h, "instagram:main")
	a.SetPollGap(time.Millisecond)
	post := social.Post{Text: "x", Images: []social.Image{{URL: "https://example.com/a.jpg"}}}
	if _, err := a.Publish(context.Background(), post); !errors.Is(err, social.ErrNotConnected) {
		t.Fatalf("unconnected: %v", err)
	}

	r := companiontest.JSON[struct{ URL string }](h, h.Admin("POST", "/api/b/instagram/oauth/start", map[string]string{"instance": "main"}))
	u, _ := url.Parse(r.URL)
	q := u.Query()
	if !strings.HasPrefix(r.URL, "https://www.instagram.com/oauth/authorize?") || q.Get("client_id") != "ig-app" ||
		q.Get("scope") != "instagram_business_basic,instagram_business_content_publish" || q.Get("enable_fb_login") != "0" || q.Get("code_challenge") != "" {
		t.Fatalf("authorisation %s", r.URL)
	}
	w := h.Public("GET", "/api/b/instagram/oauth/callback?code=ig-code&state="+url.QueryEscape(q.Get("state")), nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Connected as @lna.dev") {
		t.Fatalf("callback %d %s", w.Code, w.Body)
	}
	if f := m.codes[0]; f.Get("redirect_uri") != "https://companion.example/api/b/instagram/oauth/callback" || f.Get("grant_type") != "authorization_code" {
		t.Fatalf("code exchange %v", f)
	}
	if e := m.exchange[0]; e.Get("grant_type") != "ig_exchange_token" || e.Get("client_secret") != "ig-secret" || e.Get("access_token") != "short" {
		t.Fatalf("long-lived exchange %v", e)
	}

	// The long-lived token posts, under the account /me named.
	if _, err := a.Publish(context.Background(), post); err != nil {
		t.Fatal(err)
	}
	if m.posted[0] != "17841/long" {
		t.Fatalf("posted %v", m.posted)
	}
	st := a.OAuthState()
	if !st.Connected || st.Account != "@lna.dev" || st.ExpiresAt == nil || st.ExpiresAt.Before(time.Now().Add(59*24*time.Hour)) {
		t.Fatalf("state %+v", st)
	}
	tokens := h.Admin("GET", "/api/b/instagram/tokens", nil).Body.String()
	if !strings.Contains(tokens, `"source":"connected"`) || strings.Contains(tokens, "long\"") {
		t.Fatalf("token widget %s", tokens)
	}

	if w := h.Admin("POST", "/api/b/instagram/oauth/disconnect", map[string]string{"instance": "main"}); w.Code != 200 || a.OAuthState().Connected {
		t.Fatalf("disconnect %d", w.Code)
	}
}

func TestConnectNeedsTheApp(t *testing.T) {
	h := companiontest.New(t, "modules:\n  instagram:\n    instances:\n      main: { accountId: \"17\", accessToken: tok }\n")
	w := h.Admin("POST", "/api/b/instagram/oauth/start", map[string]string{"instance": "main"})
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "appId and appSecret") || !strings.Contains(w.Body.String(), "/api/b/instagram/oauth/callback") {
		t.Fatalf("start without the app %d %s", w.Code, w.Body)
	}
}
