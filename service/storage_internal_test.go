package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/LNA-DEV/HomePageCompanion/companion"
)

// In its own process a service gets the core's URLs through the core;
// other URLs are not its business.
func TestFetchLocal(t *testing.T) {
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/core/v1/fetch" || r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		io.WriteString(w, "bytes of "+r.URL.Query().Get("url"))
	}))
	defer core.Close()
	s, err := New(Options{CoreURL: core.URL, Token: "tok", DataDir: t.TempDir(), Logs: io.Discard}, companion.Info{ID: "x", New: func() companion.Instance { return new(struct{}) }})
	if err != nil {
		t.Fatal(err)
	}
	s.mc.setPublicURL("https://companion.example.com")
	b, ct, ok, err := s.mc.fetchLocal(context.Background(), "https://companion.example.com/api/core/tmp/abc?exp=1&sig=2")
	if !ok || err != nil || ct != "image/jpeg" || !strings.HasPrefix(string(b), "bytes of https://companion.example.com/api/core/tmp/abc") {
		t.Fatalf("%q %q %v %v", b, ct, ok, err)
	}
	if _, _, ok, _ := s.mc.fetchLocal(context.Background(), "https://example.com/a.jpg"); ok {
		t.Fatal("an outside URL taken for the core's")
	}
}
