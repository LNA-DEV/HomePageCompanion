package instagram_test

import (
	"context"
	"errors"
	"fmt"
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
	"github.com/LNA-DEV/HomePageCompanion/lib/metatoken"
	"github.com/LNA-DEV/HomePageCompanion/modules/instagram"
	"github.com/LNA-DEV/HomePageCompanion/modules/instagram/api"
)

// fakeGraph is the part of Instagram's Graph API the module uses.
type fakeGraph struct {
	mu          sync.Mutex
	tokens      []string // access_token of every call
	tagsRefused bool
	refreshErr  bool
	created     []string // user_tags of each container
	*httptest.Server
}

func newGraph(t *testing.T) *fakeGraph {
	f := &fakeGraph{}
	mux := http.NewServeMux()
	note := func(r *http.Request) {
		f.mu.Lock()
		f.tokens = append(f.tokens, r.URL.Query().Get("access_token"))
		f.mu.Unlock()
	}
	mux.HandleFunc("POST /17/media", func(w http.ResponseWriter, r *http.Request) {
		note(r)
		tags := r.URL.Query().Get("user_tags")
		f.mu.Lock()
		f.created = append(f.created, tags)
		refused := f.tagsRefused && tags != ""
		f.mu.Unlock()
		if refused {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":{"message":"invalid user"}}`)
			return
		}
		io.WriteString(w, `{"id":"c1"}`)
	})
	mux.HandleFunc("GET /c1", func(w http.ResponseWriter, r *http.Request) { note(r); io.WriteString(w, `{"status_code":"FINISHED"}`) })
	mux.HandleFunc("POST /17/media_publish", func(w http.ResponseWriter, r *http.Request) { note(r); io.WriteString(w, `{"id":"m1"}`) })
	mux.HandleFunc("GET /m1", func(w http.ResponseWriter, r *http.Request) { note(r); io.WriteString(w, `{"like_count":42}`) })
	mux.HandleFunc("GET /me", func(w http.ResponseWriter, r *http.Request) { note(r); io.WriteString(w, `{"username":"lna.dev"}`) })
	mux.HandleFunc("GET /refresh", func(w http.ResponseWriter, r *http.Request) {
		note(r)
		f.mu.Lock()
		bad := f.refreshErr
		f.mu.Unlock()
		if bad {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":{"message":"Error validating access token: Session has expired","code":190}}`)
			return
		}
		io.WriteString(w, `{"access_token":"refreshed-token","expires_in":5184000}`)
	})
	f.Server = httptest.NewServer(mux)
	prevGraph, prevRefresh := api.GraphURL, api.RefreshURL
	api.GraphURL, api.RefreshURL = f.URL+"/", f.URL+"/refresh"
	t.Cleanup(func() { api.GraphURL, api.RefreshURL = prevGraph, prevRefresh; f.Close() })
	return f
}

func (f *fakeGraph) lastToken() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tokens[len(f.tokens)-1]
}

const config = `
modules:
  fake.notifier: {}
  instagram:
    instances:
      main: { accountId: "17", accessToken: env-token, notify: fake.notifier }
`

func setup(t *testing.T) (*companiontest.Harness, *fakeGraph, *instagram.Account) {
	f := newGraph(t)
	h := companiontest.New(t, config)
	a := companiontest.Instance[*instagram.Account](h, "instagram:main")
	a.SetPollGap(time.Millisecond)
	return h, f, a
}

func TestConformance(t *testing.T) {
	_, _, a := setup(t)
	companiontest.PublisherConformance(t, a, "https://example.com/a.jpg")
}

func TestPhotoTagsAndTheirFallback(t *testing.T) {
	_, f, a := setup(t)
	post := social.Post{Text: "With Jane (@jane)", Images: []social.Image{{URL: "https://example.com/a.jpg"}},
		Mentions: []social.Mention{{Text: "@jane", Account: "jane"}, {Text: "@joe", Account: "joe"}}}
	if _, err := a.Publish(context.Background(), post); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.created[0], `"username":"jane","x":0.33,"y":0.5`) || !strings.Contains(f.created[0], `"username":"joe","x":0.67`) {
		t.Fatalf("tags %s", f.created[0])
	}
	f.tagsRefused = true
	res, err := a.Publish(context.Background(), post)
	if err != nil || res.ID != "m1" || f.created[len(f.created)-1] != "" {
		t.Fatalf("retry without tags: %v %v", res, f.created)
	}
}

func TestMentionsAndStats(t *testing.T) {
	_, _, a := setup(t)
	ctx := context.Background()
	if m, err := a.ResolveMention(ctx, "https://www.instagram.com/jane.doe/"); err != nil || m.Text != "@jane.doe" {
		t.Fatalf("mention %+v %v", m, err)
	}
	if _, err := a.ResolveMention(ctx, "https://instagram.com/LNA.dev"); !errors.Is(err, social.ErrSelf) {
		t.Fatalf("self: %v", err)
	}
	if s, err := a.GetStats(ctx, social.Result{ID: "m1"}); err != nil || s.Likes != 42 {
		t.Fatalf("stats %+v %v", s, err)
	}
}

// The token lifecycle of meta-token-refresh.md: the .env token is seeded,
// refreshed once due, and used from then on; a failed refresh keeps the old
// token and warns through the notify slot; a new .env token wins.
func TestTokenRefresh(t *testing.T) {
	h, f, a := setup(t)
	ctx := context.Background()
	a.GetStats(ctx, social.Result{ID: "m1"})
	if f.lastToken() != "env-token" {
		t.Fatalf("before refresh: %s", f.lastToken())
	}
	h.MustRunJob("instagram:main/token-refresh")
	a.GetStats(ctx, social.Result{ID: "m1"})
	if f.lastToken() != "refreshed-token" {
		t.Fatalf("after refresh: %s", f.lastToken())
	}
	var stored []string
	h.Core.DB().Raw("SELECT value FROM core_secrets").Scan(&stored)
	if len(stored) != 1 || strings.Contains(stored[0], "refreshed-token") {
		t.Fatalf("token stored in plain text: %v", stored)
	}
	w := h.Admin("GET", "/api/b/instagram/tokens", nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), "refreshed-token") || !strings.Contains(w.Body.String(), `"status":"ok"`) {
		t.Fatalf("status: %s", w.Body)
	}
	// Not due again within a week.
	before := len(f.tokens)
	h.MustRunJob("instagram:main/token-refresh")
	if len(f.tokens) != before {
		t.Fatal("refreshed again within a week")
	}

	// A refusal is kept as the last error; the notifier hears about an
	// expired token.
	f.refreshErr = true
	w = h.Admin("POST", "/api/b/instagram/tokens/refresh", map[string]string{"instance": "main"})
	if !strings.Contains(w.Body.String(), `"status":"expired"`) {
		t.Fatalf("after a failed refresh: %s", w.Body)
	}
	h.RunJob("instagram:main/token-refresh")
	n := companiontest.Instance[*companiontest.FakeNotifier](h, "fake.notifier:default")
	if len(n.Messages()) == 0 {
		t.Fatal("no warning for an expired token")
	}

	// A new token in .env replaces the stored one.
	if err := h.Rewrite(strings.Replace(config, "env-token", "new-env-token", 1)); err != nil {
		t.Fatal(err)
	}
	a = companiontest.Instance[*instagram.Account](h, "instagram:main")
	a.GetStats(ctx, social.Result{ID: "m1"})
	if f.lastToken() != "new-env-token" {
		t.Fatalf("after a new .env token: %s", f.lastToken())
	}
}

func TestImportsTheRefreshedToken(t *testing.T) {
	h, f, a := setup(t)
	key := "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="
	t.Setenv("TOKEN_ENCRYPTION_KEY", key)
	sealed, _ := metatoken.SealLegacy(key, "main", "old-refreshed-token")
	old := companiontest.LegacyDB(t,
		`CREATE TABLE platform_tokens (id integer primary key, target_name text, platform text, access_token text, seed_hash text, refreshed_at datetime, expires_at datetime)`,
		fmt.Sprintf(`INSERT INTO platform_tokens (target_name, platform, access_token, seed_hash, refreshed_at, expires_at) VALUES ('main', 'instagram', '%s', 'x', '2026-10-01 00:00:00', '2026-11-30 00:00:00')`, sealed))
	info, _ := companion.Lookup("instagram")
	if err := info.Import(h.Module("instagram"), old); err != nil {
		t.Fatal(err)
	}
	a.GetStats(context.Background(), social.Result{ID: "m1"})
	if f.lastToken() != "old-refreshed-token" {
		t.Fatalf("token after import: %s", f.lastToken())
	}
}
