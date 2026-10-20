package threads_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/capability/social"
	"github.com/LNA-DEV/HomePageCompanion/companiontest"
	"github.com/LNA-DEV/HomePageCompanion/modules/threads"
	"github.com/LNA-DEV/HomePageCompanion/modules/threads/api"
)

type fakeThreads struct {
	mu         sync.Mutex
	containers []map[string]string
	*httptest.Server
}

func newFake(t *testing.T) *fakeThreads {
	f := &fakeThreads{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /99/threads", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		f.mu.Lock()
		f.containers = append(f.containers, map[string]string{"media_type": q.Get("media_type"), "text": q.Get("text"), "image_url": q.Get("image_url")})
		f.mu.Unlock()
		io.WriteString(w, `{"id":"c1"}`)
	})
	mux.HandleFunc("GET /c1", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"status":"FINISHED"}`) })
	mux.HandleFunc("POST /99/threads_publish", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"id":"t1"}`) })
	mux.HandleFunc("GET /t1", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"id":"t1","permalink":"https://www.threads.net/@lna/post/abc"}`)
	})
	mux.HandleFunc("DELETE /gone", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) })
	mux.HandleFunc("GET /t1/insights", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"data":[{"name":"likes","values":[{"value":17}]}]}`)
	})
	mux.HandleFunc("GET /t1/replies", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"data":[{"id":"r1","text":"Schön","timestamp":"2026-10-01T10:00:00+0000","from":{"username":"jane"}}]}`)
	})
	mux.HandleFunc("GET /me", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"username":"lna"}`) })
	f.Server = httptest.NewServer(mux)
	prev := api.GraphURL
	api.GraphURL = f.URL + "/"
	t.Cleanup(func() { api.GraphURL = prev; f.Close() })
	return f
}

func setup(t *testing.T) (*fakeThreads, *threads.Account) {
	f := newFake(t)
	h := companiontest.New(t, "modules:\n  threads:\n    instances:\n      main: { userId: \"99\", accessToken: tok }\n")
	a := companiontest.Instance[*threads.Account](h, "threads:main")
	a.SetPollGap(time.Millisecond)
	return f, a
}

func TestConformance(t *testing.T) {
	_, a := setup(t)
	companiontest.PublisherConformance(t, a, "https://example.com/a.jpg")
}

func TestTextAndImagePosts(t *testing.T) {
	f, a := setup(t)
	ctx := context.Background()
	res, err := a.Publish(ctx, social.Post{Kind: "article", Text: "Neuer Beitrag", Link: &social.LinkCard{URL: "https://example.com/p/"}})
	if err != nil || res.ID != "t1" || res.URL != "https://www.threads.net/@lna/post/abc" {
		t.Fatalf("text post %+v %v", res, err)
	}
	if c := f.containers[0]; c["media_type"] != "TEXT" || c["text"] != "Neuer Beitrag\n\nhttps://example.com/p/" {
		t.Fatalf("text container %v", c)
	}
	a.Publish(ctx, social.Post{Text: "Foto", Images: []social.Image{{URL: "https://example.com/a.jpg"}}})
	if c := f.containers[1]; c["media_type"] != "IMAGE" || c["image_url"] != "https://example.com/a.jpg" {
		t.Fatalf("image container %v", c)
	}
}

func TestStatsRepliesMentionsDelete(t *testing.T) {
	_, a := setup(t)
	ctx := context.Background()
	if s, err := a.GetStats(ctx, social.Result{ID: "t1"}); err != nil || s.Likes != 17 {
		t.Fatalf("stats %+v %v", s, err)
	}
	if r, err := a.ListReplies(ctx, social.Result{ID: "t1"}); err != nil || len(r) != 1 || r[0].AuthorURL != "https://www.threads.net/@jane" {
		t.Fatalf("replies %+v %v", r, err)
	}
	if _, err := a.ResolveMention(ctx, "https://www.threads.net/@lna"); !errors.Is(err, social.ErrSelf) {
		t.Fatalf("self %v", err)
	}
	if m, err := a.ResolveMention(ctx, "https://threads.com/@jane.doe"); err != nil || m.Text != "@jane.doe" {
		t.Fatalf("mention %+v %v", m, err)
	}
	if err := a.Delete(ctx, social.Result{ID: "gone"}); err != nil {
		t.Fatalf("delete gone: %v", err)
	}
}
