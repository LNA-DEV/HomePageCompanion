package mastodon_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/LNA-DEV/HomePageCompanion/capability/social"
	"github.com/LNA-DEV/HomePageCompanion/companiontest"
	"github.com/LNA-DEV/HomePageCompanion/modules/mastodon"
)

// fakeMastodon answers the API calls the module makes, like a recorded
// instance would.
type fakeMastodon struct {
	mu       sync.Mutex
	statuses []url.Values
	media    []string
	limited  bool
	*httptest.Server
}

func newFake(t *testing.T) *fakeMastodon {
	f := &fakeMastodon{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/media", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		r.ParseMultipartForm(1 << 20)
		f.mu.Lock()
		f.media = append(f.media, r.FormValue("description"))
		n := len(f.media)
		f.mu.Unlock()
		fmt.Fprintf(w, `{"id":"m%d","type":"image"}`, n)
	})
	mux.HandleFunc("POST /api/v1/statuses", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.limited {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		r.ParseForm()
		f.statuses = append(f.statuses, r.PostForm)
		fmt.Fprintf(w, `{"id":"%d","url":"https://social.example/@lna/%d"}`, len(f.statuses), len(f.statuses))
	})
	mux.HandleFunc("DELETE /api/v1/statuses/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") == "gone" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		io.WriteString(w, `{}`)
	})
	mux.HandleFunc("GET /api/v1/statuses/{id}/favourited_by", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `[{"id":"1","acct":"a"},{"id":"2","acct":"b"},{"id":"3","acct":"c"}]`)
	})
	mux.HandleFunc("GET /api/v1/statuses/{id}/context", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"ancestors":[],"descendants":[{"id":"r1","url":"https://x/r1","content":"<p>Wow</p>","created_at":"2026-10-01T10:00:00Z","account":{"acct":"jane@other.example","url":"https://other.example/@jane","avatar":"a.png"}}]}`)
	})
	mux.HandleFunc("GET /api/v1/accounts/verify_credentials", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"id":"1","username":"lna"}`)
	})
	mux.HandleFunc("GET /.well-known/webfinger", func(w http.ResponseWriter, r *http.Request) {
		host := strings.TrimPrefix(f.URL, "http://")
		switch r.URL.Query().Get("resource") {
		case "acct:lna@" + host:
			io.WriteString(w, `{"subject":"acct:lna@social.example"}`)
		case "acct:jane@" + host:
			io.WriteString(w, `{"subject":"acct:jane@social.example"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	mux.HandleFunc("GET /image.jpg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write([]byte("not really a jpeg"))
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

func setup(t *testing.T) (*companiontest.Harness, *fakeMastodon, *mastodon.Account) {
	f := newFake(t)
	h := companiontest.New(t, fmt.Sprintf("modules:\n  mastodon:\n    instances:\n      main: { instance: %q, token: tok }\n", f.URL))
	return h, f, companiontest.Instance[*mastodon.Account](h, "mastodon:main")
}

func TestConformance(t *testing.T) {
	_, f, a := setup(t)
	companiontest.PublisherConformance(t, a, f.URL+"/image.jpg")
}

func TestPublishSendsImagesAltTextAndCW(t *testing.T) {
	_, f, a := setup(t)
	res, err := a.Publish(context.Background(), social.Post{Text: "Eisvogel", ContentWarning: "bird",
		Images: []social.Image{{URL: f.URL + "/image.jpg", Alt: "a kingfisher"}}})
	if err != nil {
		t.Fatal(err)
	}
	if res.ID != "1" || res.URL != "https://social.example/@lna/1" {
		t.Fatalf("result %+v", res)
	}
	st := f.statuses[0]
	if st.Get("status") != "Eisvogel" || st.Get("spoiler_text") != "bird" || st["media_ids[]"][0] != "m1" || f.media[0] != "a kingfisher" {
		t.Fatalf("status %v media %v", st, f.media)
	}
	// A link the text lacks is appended, so Mastodon builds the card.
	a.Publish(context.Background(), social.Post{Text: "New post", Link: &social.LinkCard{URL: "https://example.com/p/"}})
	if got := f.statuses[1].Get("status"); got != "New post\n\nhttps://example.com/p/" {
		t.Fatalf("link: %q", got)
	}
}

func TestRateLimitIsTransient(t *testing.T) {
	_, f, a := setup(t)
	f.limited = true
	_, err := a.Publish(context.Background(), social.Post{Text: "x"})
	if !errors.Is(err, social.ErrRateLimited) || social.Classify(err).Cause != social.CauseTransient {
		t.Fatalf("err %v", err)
	}
}

func TestStatsRepliesDelete(t *testing.T) {
	_, _, a := setup(t)
	ctx := context.Background()
	s, err := a.GetStats(ctx, social.Result{ID: "1"})
	if err != nil || s.Likes != 3 {
		t.Fatalf("stats %+v %v", s, err)
	}
	r, err := a.ListReplies(ctx, social.Result{ID: "1"})
	if err != nil || len(r) != 1 || r[0].Author != "jane@other.example" || r[0].Body != "<p>Wow</p>" {
		t.Fatalf("replies %+v %v", r, err)
	}
	if err := a.Delete(ctx, social.Result{ID: "gone"}); err != nil {
		t.Fatalf("deleting a gone status: %v", err)
	}
}

func TestMentions(t *testing.T) {
	_, f, a := setup(t)
	ctx := context.Background()
	m, err := a.ResolveMention(ctx, f.URL+"/@jane")
	if err != nil || m.Text != "@jane@social.example" || m.Account != "jane@social.example" {
		t.Fatalf("mention %+v %v", m, err)
	}
	if _, err := a.ResolveMention(ctx, f.URL+"/@lna"); !errors.Is(err, social.ErrSelf) {
		t.Fatalf("self: %v", err)
	}
	if _, err := a.ResolveMention(ctx, "https://bsky.app/profile/jane.example"); !errors.Is(err, social.ErrNotAProfile) {
		t.Fatalf("bluesky URL: %v", err)
	}
	if _, err := a.ResolveMention(ctx, f.URL+"/@nobody"); err == nil {
		t.Fatal("unknown account resolved")
	}
}

func TestCheckAction(t *testing.T) {
	h, _, _ := setup(t)
	w := h.Admin("POST", "/api/b/mastodon/check", map[string]string{"instance": "main"})
	var body map[string]any
	json.Unmarshal(w.Body.Bytes(), &body)
	if w.Code != 200 || body["account"] != "@lna" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}
