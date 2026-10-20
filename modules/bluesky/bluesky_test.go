package bluesky_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/LNA-DEV/HomePageCompanion/capability/social"
	"github.com/LNA-DEV/HomePageCompanion/companiontest"
	"github.com/LNA-DEV/HomePageCompanion/modules/bluesky"
	"github.com/LNA-DEV/HomePageCompanion/modules/bluesky/api"
)

// fakePDS is a PDS plus the public AppView.
type fakePDS struct {
	mu      sync.Mutex
	logins  int
	records []map[string]any
	blobs   int
	*httptest.Server
}

func newPDS(t *testing.T) *fakePDS {
	f := &fakePDS{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /xrpc/com.atproto.server.createSession", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.logins++
		f.mu.Unlock()
		io.WriteString(w, `{"accessJwt":"jwt","did":"did:plc:lna","handle":"lna.example"}`)
	})
	mux.HandleFunc("POST /xrpc/com.atproto.repo.uploadBlob", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.blobs++
		n := f.blobs
		f.mu.Unlock()
		fmt.Fprintf(w, `{"blob":{"ref":{"$link":"bafy%d"},"mimeType":"image/jpeg","size":4}}`, n)
	})
	mux.HandleFunc("POST /xrpc/com.atproto.repo.createRecord", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.records = append(f.records, body["record"].(map[string]any))
		n := len(f.records)
		f.mu.Unlock()
		fmt.Fprintf(w, `{"uri":"at://did:plc:lna/app.bsky.feed.post/rk%d","cid":"cid%d"}`, n, n)
	})
	mux.HandleFunc("GET /xrpc/app.bsky.feed.getLikes", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"uri":"x","cid":"y","likes":[{"actor":{"did":"a"}},{"actor":{"did":"b"}}]}`)
	})
	mux.HandleFunc("GET /xrpc/app.bsky.feed.getPostThread", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"thread":{"post":{"uri":"p"},"replies":[{"post":{"uri":"at://did:plc:jane/app.bsky.feed.post/r1","author":{"handle":"jane.example"},"record":{"text":"Nice","createdAt":"2026-10-01T10:00:00Z"}}}]}}`)
	})
	mux.HandleFunc("GET /xrpc/app.bsky.actor.getProfile", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("actor") {
		case "lna.example":
			io.WriteString(w, `{"did":"did:plc:lna","handle":"lna.example"}`)
		case "jane.bsky.social", "did:plc:jane":
			io.WriteString(w, `{"did":"did:plc:jane","handle":"jane.example"}`)
		default:
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":"InvalidRequest"}`)
		}
	})
	mux.HandleFunc("GET /image.jpg", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("jpeg")) })
	f.Server = httptest.NewServer(mux)
	prev := api.PublicURL
	api.PublicURL = f.URL
	t.Cleanup(func() { api.PublicURL = prev; f.Close() })
	return f
}

func setup(t *testing.T) (*fakePDS, *bluesky.Account) {
	f := newPDS(t)
	h := companiontest.New(t, fmt.Sprintf("modules:\n  bluesky:\n    instances:\n      main: { username: lna.example, password: app-pw, service: %q }\n", f.URL))
	return f, companiontest.Instance[*bluesky.Account](h, "bluesky:main")
}

func TestConformance(t *testing.T) {
	f, a := setup(t)
	companiontest.PublisherConformance(t, a, f.URL+"/image.jpg")
}

func TestPublishReusesTheSessionAndSetsFacets(t *testing.T) {
	f, a := setup(t)
	ctx := context.Background()
	res, err := a.Publish(ctx, social.Post{Text: "With Jane (@jane.example)", Images: []social.Image{{URL: f.URL + "/image.jpg", Alt: "a bird"}},
		Mentions: []social.Mention{{Text: "@jane.example", Account: "did:plc:jane"}}})
	if err != nil {
		t.Fatal(err)
	}
	if res.ID != "at://did:plc:lna/app.bsky.feed.post/rk1" || res.Version != "cid1" || res.URL != "https://bsky.app/profile/lna.example/post/rk1" {
		t.Fatalf("result %+v", res)
	}
	rec := f.records[0]
	facets, _ := json.Marshal(rec["facets"])
	if !json.Valid(facets) || !contains(string(facets), "did:plc:jane") {
		t.Fatalf("facets %s", facets)
	}
	embed := rec["embed"].(map[string]any)
	if embed["$type"] != "app.bsky.embed.images" {
		t.Fatalf("embed %v", embed)
	}
	a.Publish(ctx, social.Post{Text: "Second"})
	if f.logins != 1 {
		t.Fatalf("logged in %d times", f.logins)
	}
}

func TestArticleGetsALinkCard(t *testing.T) {
	f, a := setup(t)
	_, err := a.Publish(context.Background(), social.Post{Kind: "article", Text: "Lofoten",
		Link: &social.LinkCard{URL: "https://example.com/posts/lofoten/", Title: "Lofoten", Description: "Two weeks north", ImageURL: f.URL + "/image.jpg"}})
	if err != nil {
		t.Fatal(err)
	}
	embed := f.records[0]["embed"].(map[string]any)
	ext := embed["external"].(map[string]any)
	if embed["$type"] != "app.bsky.embed.external" || ext["uri"] != "https://example.com/posts/lofoten/" || ext["title"] != "Lofoten" || ext["thumb"] == nil {
		t.Fatalf("link card %v", embed)
	}
}

func TestMentionsStatsReplies(t *testing.T) {
	_, a := setup(t)
	ctx := context.Background()
	m, err := a.ResolveMention(ctx, "https://bsky.app/profile/jane.bsky.social")
	if err != nil || m.Text != "@jane.example" || m.Account != "did:plc:jane" {
		t.Fatalf("mention %+v %v", m, err)
	}
	if _, err := a.ResolveMention(ctx, "https://bsky.app/profile/lna.example"); !errors.Is(err, social.ErrSelf) {
		t.Fatalf("self: %v", err)
	}
	if _, err := a.ResolveMention(ctx, "https://mastodon.social/@jane"); !errors.Is(err, social.ErrNotAProfile) {
		t.Fatalf("other network: %v", err)
	}
	s, err := a.GetStats(ctx, social.Result{ID: "at://x", Version: "cid"})
	if err != nil || s.Likes != 2 {
		t.Fatalf("stats %+v %v", s, err)
	}
	r, err := a.ListReplies(ctx, social.Result{ID: "at://x"})
	if err != nil || len(r) != 1 || r[0].Author != "jane.example" {
		t.Fatalf("replies %+v %v", r, err)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
