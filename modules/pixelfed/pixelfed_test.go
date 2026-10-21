package pixelfed_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/LNA-DEV/HomePageCompanion/capability/social"
	"github.com/LNA-DEV/HomePageCompanion/companiontest"
	"github.com/LNA-DEV/HomePageCompanion/modules/pixelfed"
)

func fake(t *testing.T) *httptest.Server {
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/media", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"id":"m1"}`)
	})
	mux.HandleFunc("POST /api/v1/statuses", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if !strings.HasPrefix(r.PostForm.Get("status"), "CW: ") && r.PostForm.Get("status") != "Conformance test" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		fmt.Fprintf(w, `{"id":"p1","url":"%s/p/lna/p1"}`, srv.URL)
	})
	mux.HandleFunc("GET /api/v1/statuses/{id}/favourited_by", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `[{"id":"1"},{"id":"2"}]`)
	})
	mux.HandleFunc("GET /api/v1/accounts/verify_credentials", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"id":"7","username":"lna"}`)
	})
	mux.HandleFunc("GET /api/v1/accounts/7/statuses", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("max_id") != "" {
			io.WriteString(w, `[]`)
			return
		}
		io.WriteString(w, `[{"id":"p1","url":"https://px/p1","created_at":"2026-01-01T00:00:00Z","media_attachments":[{"url":"https://px/1.jpg"}]}]`)
	})
	mux.HandleFunc("GET /image.jpg", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("jpeg")) })
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestPixelfed(t *testing.T) {
	srv := fake(t)
	h := companiontest.New(t, fmt.Sprintf("modules:\n  pixelfed:\n    instances:\n      main: { instance: %q, token: tok }\n", srv.URL))
	a := companiontest.Instance[*pixelfed.Account](h, "pixelfed:main")
	ctx := context.Background()

	companiontest.PublisherConformance(t, a, srv.URL+"/image.jpg")

	if _, err := a.Publish(ctx, social.Post{Text: "A blog post"}); !errors.Is(err, social.ErrNeedsImage) {
		t.Fatalf("no image: %v", err)
	}
	res, err := a.Publish(ctx, social.Post{Text: "x", ContentWarning: "spiders", Images: []social.Image{{URL: srv.URL + "/image.jpg"}}})
	if err != nil || res.ID != "p1" {
		t.Fatalf("publish %+v %v", res, err)
	}
	s, err := a.GetStats(ctx, res)
	if err != nil || s.Likes != 2 {
		t.Fatalf("stats %+v %v", s, err)
	}
	posts, err := a.ListPosts(ctx)
	if err != nil || len(posts) != 1 || posts[0].ImageURLs[0] != "https://px/1.jpg" {
		t.Fatalf("posts %+v %v", posts, err)
	}
}
