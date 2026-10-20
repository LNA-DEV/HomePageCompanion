package companion

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDuration(t *testing.T) {
	for in, want := range map[string]time.Duration{
		`"10m"`: 10 * time.Minute, `"36h"`: 36 * time.Hour, `"7d"`: 7 * 24 * time.Hour,
		`"1.5d"`: 36 * time.Hour, `90`: 90 * time.Second, `""`: 0, `null`: 0,
	} {
		var d Duration
		if err := json.Unmarshal([]byte(in), &d); err != nil || d.D() != want {
			t.Errorf("%s: %v %v", in, d.D(), err)
		}
	}
	var d Duration
	if err := json.Unmarshal([]byte(`"soon"`), &d); err == nil {
		t.Error("a word is a duration")
	}
}

func TestInstanceIDs(t *testing.T) {
	if b, n := SplitInstanceID("rss:photos"); b != "rss" || n != "photos" {
		t.Fatal(b, n)
	}
	if b, n := SplitInstanceID("gbif"); b != "gbif" || n != DefaultInstance {
		t.Fatal(b, n)
	}
	if InstanceID("a", "b") != "a:b" {
		t.Fatal("InstanceID")
	}
}

func TestRegister(t *testing.T) {
	Register(Info{ID: "x.test-module"})
	defer Unregister("x.test-module")
	if _, ok := Lookup("x.test-module"); !ok {
		t.Fatal("not registered")
	}
	for _, bad := range []string{"", "Upper", "with space", ".dot", "x.test-module"} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%q accepted", bad)
				}
			}()
			Register(Info{ID: bad})
		}()
	}
}

func TestFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("png"))
	}))
	defer srv.Close()
	b, ct, err := Fetch(context.Background(), nil, srv.URL+"/a.png")
	if err != nil || string(b) != "png" || ct != "image/png" {
		t.Fatalf("%q %q %v", b, ct, err)
	}
	if _, _, err := Fetch(context.Background(), nil, srv.URL+"/missing"); err == nil {
		t.Fatal("404 accepted")
	}
	SetLocalFetcher(func(ctx context.Context, url string) ([]byte, string, bool, error) {
		if url == "local://x" {
			return []byte("local"), "text/plain", true, nil
		}
		return nil, "", false, nil
	})
	defer SetLocalFetcher(nil)
	if b, _, err := Fetch(context.Background(), nil, "local://x"); err != nil || string(b) != "local" {
		t.Fatalf("local: %q %v", b, err)
	}
}

func TestEventDecode(t *testing.T) {
	e := Event{Data: json.RawMessage(`{"a":1}`)}
	var v struct{ A int }
	if err := e.Decode(&v); err != nil || v.A != 1 {
		t.Fatal(v, err)
	}
}
