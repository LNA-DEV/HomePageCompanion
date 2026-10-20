package webfinger

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLookup(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/webfinger" {
			t.Errorf("path = %s", r.URL.Path)
		}
		switch r.URL.Query().Get("resource") {
		case "acct:LNA-DEV@pixelfed.example":
			io.WriteString(w, `{"subject":"acct:LNA-DEV@pixelfed.example","links":[]}`)
		case "acct:jane@web.example":
			io.WriteString(w, `{"subject":"acct:jane@example.social"}`)
		case "acct:odd@web.example":
			io.WriteString(w, `{"subject":"https://web.example/users/odd"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	ctx := context.Background()

	if got, err := Lookup(ctx, srv.URL, "LNA-DEV@pixelfed.example"); err != nil || got != "LNA-DEV@pixelfed.example" {
		t.Errorf("hit = %q, %v", got, err)
	}
	// The subject is canonical even when the web host differs.
	if got, err := Lookup(ctx, srv.URL+"/", "jane@web.example"); err != nil || got != "jane@example.social" {
		t.Errorf("other domain = %q, %v", got, err)
	}
	if _, err := Lookup(ctx, srv.URL, "nobody@web.example"); !errors.Is(err, ErrNotFound) {
		t.Errorf("miss err = %v, want ErrNotFound", err)
	}
	if _, err := Lookup(ctx, srv.URL, "odd@web.example"); err == nil {
		t.Error("subject without acct: accepted")
	}
}

func TestLookupTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := Lookup(ctx, srv.URL, "slow@web.example"); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want deadline exceeded", err)
	}
}
