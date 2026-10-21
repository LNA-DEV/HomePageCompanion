package threads_test

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
	"github.com/LNA-DEV/HomePageCompanion/modules/threads"
	"github.com/LNA-DEV/HomePageCompanion/modules/threads/api"
)

// login is the Threads login: code exchange, long-lived token, /me, and
// the calls of one text post.
type login struct {
	mu       sync.Mutex
	codes    []url.Values
	exchange []url.Values
	posted   []string // "<user>/<token>" of each container
	*httptest.Server
}

func newLogin(t *testing.T) *login {
	l := &login{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		l.mu.Lock()
		l.codes = append(l.codes, r.PostForm)
		l.mu.Unlock()
		if r.PostForm.Get("client_secret") != "th-secret" {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":{"message":"Error validating client secret.","type":"OAuthException","code":1}}`)
			return
		}
		io.WriteString(w, `{"access_token":"short","user_id":5550}`)
	})
	mux.HandleFunc("GET /access_token", func(w http.ResponseWriter, r *http.Request) {
		l.mu.Lock()
		l.exchange = append(l.exchange, r.URL.Query())
		l.mu.Unlock()
		io.WriteString(w, `{"access_token":"long","token_type":"bearer","expires_in":5184000}`)
	})
	mux.HandleFunc("GET /me", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"id":"5550","username":"lna"}`)
	})
	mux.HandleFunc("POST /5550/threads", func(w http.ResponseWriter, r *http.Request) {
		l.mu.Lock()
		l.posted = append(l.posted, "5550/"+r.URL.Query().Get("access_token"))
		l.mu.Unlock()
		io.WriteString(w, `{"id":"c1"}`)
	})
	mux.HandleFunc("GET /c1", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"status":"FINISHED"}`) })
	mux.HandleFunc("POST /5550/threads_publish", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"id":"t1"}`) })
	mux.HandleFunc("GET /t1", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"id":"t1","permalink":"https://www.threads.net/@lna/post/abc"}`)
	})
	l.Server = httptest.NewServer(mux)
	prev := []string{api.GraphURL, api.TokenURL, api.ExchangeURL}
	api.GraphURL, api.TokenURL, api.ExchangeURL = l.URL+"/", l.URL+"/oauth/access_token", l.URL+"/access_token"
	t.Cleanup(func() { api.GraphURL, api.TokenURL, api.ExchangeURL = prev[0], prev[1], prev[2]; l.Close() })
	return l
}

func TestConnectWithThreadsLogin(t *testing.T) {
	l := newLogin(t)
	h := companiontest.New(t, "core: { publicUrl: \"https://companion.example\" }\nmodules:\n  threads:\n    instances:\n      main: { appId: \"th-app\", appSecret: th-secret }\n")
	a := companiontest.Instance[*threads.Account](h, "threads:main")
	a.SetPollGap(time.Millisecond)
	if _, err := a.Publish(context.Background(), social.Post{Text: "x"}); !errors.Is(err, social.ErrNotConnected) {
		t.Fatalf("unconnected: %v", err)
	}

	r := companiontest.JSON[struct{ URL string }](h, h.Admin("POST", "/api/b/threads/oauth/start", map[string]string{"instance": "main"}))
	u, _ := url.Parse(r.URL)
	q := u.Query()
	if !strings.HasPrefix(r.URL, "https://www.threads.com/oauth/authorize?") || q.Get("client_id") != "th-app" ||
		q.Get("scope") != "threads_basic,threads_content_publish,threads_manage_insights,threads_read_replies,threads_delete" {
		t.Fatalf("authorisation %s", r.URL)
	}
	w := h.Public("GET", "/api/b/threads/oauth/callback?code=th-code&state="+url.QueryEscape(q.Get("state")), nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Connected as @lna") {
		t.Fatalf("callback %d %s", w.Code, w.Body)
	}
	if e := l.exchange[0]; e.Get("grant_type") != "th_exchange_token" || e.Get("client_secret") != "th-secret" || e.Get("access_token") != "short" {
		t.Fatalf("long-lived exchange %v", e)
	}
	res, err := a.Publish(context.Background(), social.Post{Text: "hello"})
	if err != nil || l.posted[0] != "5550/long" || res.URL == "" {
		t.Fatalf("publish %+v %v %v", res, err, l.posted)
	}
	if st := a.OAuthState(); !st.Connected || st.Account != "@lna" {
		t.Fatalf("state %+v", st)
	}
}
