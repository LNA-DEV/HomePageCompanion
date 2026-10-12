package instagramapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/me" || q.Get("fields") != "username" {
			t.Errorf("unexpected request %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		if q.Get("access_token") != "good" {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":{"message":"Invalid OAuth access token"}}`)
			return
		}
		io.WriteString(w, `{"id":"1789","username":"lna_dev"}`)
	}))
	defer srv.Close()
	prev := GraphURL
	GraphURL = srv.URL + "/"
	t.Cleanup(func() { GraphURL = prev })

	if got, err := Me(context.Background(), "good"); err != nil || got != "lna_dev" {
		t.Errorf("Me = %q, %v", got, err)
	}
	if _, err := Me(context.Background(), "bad"); err == nil {
		t.Error("Me with a bad token succeeded")
	}
}
