package microblog_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/capability/social"
	"github.com/LNA-DEV/HomePageCompanion/companion"
	"github.com/LNA-DEV/HomePageCompanion/companiontest"
	"github.com/LNA-DEV/HomePageCompanion/modules/microblog"
)

const config = `
modules:
  fake.publisher: { instances: { main: { platform: mastodon }, bsky: { platform: bluesky } } }
  microblog:
    config: { publishTo: [fake.publisher:main, fake.publisher:bsky] }
`

func upload(t *testing.T, h *companiontest.Harness, name string, data []byte) string {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	f, _ := w.CreateFormFile("file", name)
	f.Write(data)
	w.Close()
	req := httptest.NewRequest("POST", "/api/b/microblog/upload", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Authorization", "ApiKey "+companiontest.AdminKey)
	rec := httptest.NewRecorder()
	h.Core.ServeHTTP(rec, req)
	var out struct{ URL string }
	json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != 200 || !strings.HasPrefix(out.URL, "/api/microblog/media/") {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	return out.URL
}

func TestCreatePublishesEverywhere(t *testing.T) {
	h := companiontest.New(t, config)
	main := companiontest.Instance[*companiontest.FakePublisher](h, "fake.publisher:main")
	bsky := companiontest.Instance[*companiontest.FakePublisher](h, "fake.publisher:bsky")
	bsky.FailOnce(errors.New("status: 503"))

	img := upload(t, h, "photo.JPG", []byte("jpeg bytes"))
	w := h.Admin("POST", "/api/b/microblog/posts", map[string]string{"body": "Hello fediverse\nsecond line", "contentWarning": "cw", "imageUrl": img, "imageAltText": "alt"})
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	var created struct {
		ID           uint
		Slug         string
		Publications []struct {
			ID      uint
			Success bool
			Error   string `json:"errorOnAttempt"`
		}
	}
	json.Unmarshal(w.Body.Bytes(), &created)
	if len(created.Publications) != 2 || !created.Publications[0].Success || created.Publications[1].Success {
		t.Fatalf("publications %+v", created.Publications)
	}
	// The uploaded image reached the network: a temp URL the core resolves.
	p := main.Posted()[0]
	if p.ContentWarning != "cw" || len(p.Images) != 1 || p.Images[0].Alt != "alt" {
		t.Fatalf("posted %+v", p)
	}
	data, _, err := companion.Fetch(context.Background(), nil, p.Images[0].URL)
	if err != nil || string(data) != "jpeg bytes" {
		t.Fatalf("image %q %v", data, err)
	}
	// The failed one can be retried.
	if w := h.Admin("POST", "/api/b/microblog/posts/1/retry/2", nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"success":true`) {
		t.Fatalf("retry: %d %s", w.Code, w.Body)
	}
	if w := h.Admin("POST", "/api/b/microblog/posts/1/retry/2", nil); w.Code != 409 {
		t.Fatalf("retry twice: %d", w.Code)
	}

	// The website sees it with likes from the interactions module and the
	// image under its old URL.
	mc := h.Module("microblog")
	mc.Emit(context.Background(), social.EventStatsUpdated, social.StatsUpdated{ItemID: created.Slug, Source: "microblog", Target: "fake.publisher:main", Likes: 4, Time: time.Now()})
	mc.Emit(context.Background(), social.EventStatsUpdated, social.StatsUpdated{ItemID: created.Slug, Target: social.WebsiteTarget, Likes: 2, Time: time.Now()})
	h.Deliver()
	var view map[string]any
	json.Unmarshal(h.Public("GET", "/api/microblog/posts/"+created.Slug, nil).Body.Bytes(), &view)
	if view["likeCount"].(float64) != 6 || len(view["publications"].([]any)) != 2 || view["body"] != "Hello fediverse\nsecond line" {
		t.Fatalf("public view %v", view)
	}
	pubs := view["publications"].([]any)
	if pubs[0].(map[string]any)["targetName"] != "main" {
		t.Fatalf("targetName %v", pubs[0])
	}
	likes := h.Public("GET", "/api/microblog/posts/"+created.Slug+"/likes", nil).Body.String()
	if likes != `[{"platform":"mastodon","count":4},{"platform":"native","count":2}]` {
		t.Fatalf("likes %s", likes)
	}
	media := h.Public("GET", img, nil)
	if media.Code != 200 || media.Body.String() != "jpeg bytes" || media.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("media %d %q %s", media.Code, media.Body, media.Header().Get("Content-Type"))
	}
	if list := h.Public("GET", "/api/microblog/posts?limit=5", nil).Body.String(); !strings.Contains(list, created.Slug) {
		t.Fatalf("list %s", list)
	}
}

func TestRepliesAndDelete(t *testing.T) {
	h := companiontest.New(t, config)
	main := companiontest.Instance[*companiontest.FakePublisher](h, "fake.publisher:main")
	blog := companiontest.Instance[*microblog.Blog](h, "microblog:default")
	post, _, err := blog.Create(context.Background(), "A post", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	main.Replies = map[string][]social.Reply{"post-1": {{ID: "r1", Author: "jane@x", Body: "Nice", PostedAt: time.Now()}}}
	h.MustRunJob("microblog:default/replies")
	h.MustRunJob("microblog:default/replies") // idempotent
	comments := h.Public("GET", "/api/microblog/posts/"+post.Slug+"/comments", nil).Body.String()
	if strings.Count(comments, `"externalId":"r1"`) != 1 {
		t.Fatalf("comments %s", comments)
	}
	if w := h.Admin("DELETE", "/api/b/microblog/posts/1", nil); w.Code != 200 {
		t.Fatalf("delete %d", w.Code)
	}
	if w := h.Public("GET", "/api/microblog/posts/"+post.Slug, nil); w.Code != 404 {
		t.Fatalf("after delete %d", w.Code)
	}
}

func TestValidation(t *testing.T) {
	h := companiontest.New(t, config)
	if w := h.Admin("POST", "/api/b/microblog/posts", map[string]string{"body": " "}); w.Code != 400 {
		t.Fatalf("empty: %d", w.Code)
	}
	if w := h.Admin("POST", "/api/b/microblog/posts", map[string]string{"body": strings.Repeat("x", 501)}); w.Code != 400 {
		t.Fatalf("too long: %d", w.Code)
	}
	if w := h.Public("GET", "/api/microblog/media/..%2Fsecret", nil); w.Code != 400 && w.Code != 404 {
		t.Fatalf("traversal: %d", w.Code)
	}
}

func TestImportsTheOldMicroblog(t *testing.T) {
	h := companiontest.New(t, config)
	old := companiontest.LegacyDB(t,
		`CREATE TABLE microblog_posts (id integer primary key, slug text, body text, content_warning text, image_url text, image_alt_text text, created_at datetime, updated_at datetime)`,
		`CREATE TABLE microblog_publications (id integer primary key, post_id integer, target_name text, platform text, post_url text, external_id text, version_id text, success numeric, error_message text, likes_refreshed_at datetime, comments_refreshed_at datetime, created_at datetime, updated_at datetime)`,
		`CREATE TABLE microblog_comments (id integer primary key, post_id integer, platform text, external_id text, author text, author_url text, avatar_url text, body text, posted_at datetime, imported_at datetime)`,
		`CREATE TABLE interactions (id integer primary key, source text, item_id text, platform text, target_name text, like_count integer)`,
		`INSERT INTO microblog_posts VALUES (1, 's1', 'Old post', '', '/api/microblog/media/abc.jpg', 'alt', '2026-01-01', '2026-01-01')`,
		`INSERT INTO microblog_publications VALUES (1, 1, 'mastodon', 'mastodon', 'https://m/1', '1', NULL, 1, '', '2026-01-02', NULL, '2026-01-01', '2026-01-01')`,
		`INSERT INTO microblog_comments VALUES (1, 1, 'mastodon', 'r1', 'jane', '', '', 'hi', '2026-01-02', '2026-01-02')`,
		`INSERT INTO interactions (source, item_id, platform, target_name, like_count) VALUES ('microblog', 's1', 'mastodon', 'mastodon', 3)`)
	os.MkdirAll(filepath.Join(old.DataDir, "microblog"), 0o755)
	os.WriteFile(filepath.Join(old.DataDir, "microblog", "abc.jpg"), []byte("old image"), 0o644)
	old.Targets = []companion.LegacyTarget{{Name: "mastodon", Platform: "mastodon"}}
	info, _ := companion.Lookup("microblog")
	if err := info.Import(h.Module("microblog"), old); err != nil {
		t.Fatal(err)
	}
	var view map[string]any
	json.Unmarshal(h.Public("GET", "/api/microblog/posts/s1", nil).Body.Bytes(), &view)
	if view["likeCount"].(float64) != 3 || view["commentCount"].(float64) != 1 {
		t.Fatalf("view %v", view)
	}
	if m := h.Public("GET", "/api/microblog/media/abc.jpg", nil); m.Body.String() != "old image" {
		t.Fatalf("media %d", m.Code)
	}
}
