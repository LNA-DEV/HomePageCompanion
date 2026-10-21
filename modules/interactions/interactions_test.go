package interactions_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/capability/social"
	"github.com/LNA-DEV/HomePageCompanion/companion"
	"github.com/LNA-DEV/HomePageCompanion/companiontest"
	"github.com/LNA-DEV/HomePageCompanion/modules/interactions"
)

type statsListener struct{ got []social.StatsUpdated }

func (l *statsListener) HandleEvent(_ context.Context, ev companion.Event) error {
	var s social.StatsUpdated
	ev.Decode(&s)
	l.got = append(l.got, s)
	return nil
}

func init() {
	companion.Register(companion.Info{ID: "t.stats", New: func() companion.Instance { return new(statsListener) },
		Subscribes: []string{social.EventStatsUpdated}})
}

const config = `
modules:
  fake.publisher: { instances: { mastodon: { platform: mastodon }, bluesky: { platform: bluesky } } }
  t.stats: {}
  interactions:
    config: { ipHashSalt: salt, platforms: [fake.publisher:mastodon, fake.publisher:bluesky] }
`

func setup(t *testing.T) (*companiontest.Harness, *interactions.Counter) {
	h := companiontest.New(t, config)
	c := companiontest.Instance[*interactions.Counter](h, "interactions:default")
	c.NoPacing()
	return h, c
}

func publish(t *testing.T, h *companiontest.Harness, item, target, platform, id string) {
	t.Helper()
	err := h.Module("autoupload-test").Emit(context.Background(), social.EventPostPublished, social.PostPublished{
		ItemID: item, Source: "autoupload", Target: target, Platform: platform, Ref: social.Result{ID: id}, Time: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	h.Deliver()
}

func init() {
	companion.Register(companion.Info{ID: "autoupload-test", New: func() companion.Instance { return new(struct{}) }})
}

func TestCountsLikesOfEveryPublication(t *testing.T) {
	h, c := setup(t)
	h.Rewrite(config + "  autoupload-test: {}\n")
	c = companiontest.Instance[*interactions.Counter](h, "interactions:default")
	c.NoPacing()
	masto := companiontest.Instance[*companiontest.FakePublisher](h, "fake.publisher:mastodon")
	bsky := companiontest.Instance[*companiontest.FakePublisher](h, "fake.publisher:bluesky")
	masto.SetLikes("m1", 18)
	bsky.SetLikes("b1", 12)
	publish(t, h, "photo-a", "fake.publisher:mastodon", "mastodon", "m1")
	publish(t, h, "photo-a", "fake.publisher:bluesky", "bluesky", "b1")
	if err := c.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.Deliver()
	l := companiontest.Instance[*statsListener](h, "t.stats:default")
	if len(l.got) != 2 || l.got[0].Likes+l.got[1].Likes != 30 {
		t.Fatalf("stats events %+v", l.got)
	}
	var resp []interactions.LikesResponse
	json.Unmarshal(h.Public("GET", "/api/interactions/post/all/photo-a", nil).Body.Bytes(), &resp)
	// Sorted by platform, as the old companion answered.
	if len(resp) != 2 || resp[0] != (interactions.LikesResponse{Platform: "bluesky", Likes: 12}) || resp[1].Likes != 18 {
		t.Fatalf("website response %+v", resp)
	}
	// One target by its old name.
	json.Unmarshal(h.Public("GET", "/api/interactions/post/bluesky/photo-a", nil).Body.Bytes(), &resp)
	if len(resp) != 1 || resp[0].Likes != 12 {
		t.Fatalf("one target %+v", resp)
	}
	// A released publication is no longer counted.
	h.Module("autoupload-test").Emit(context.Background(), social.EventPostRemoved, social.PostRemoved{ItemID: "photo-a", Target: "fake.publisher:bluesky"})
	h.Deliver()
	json.Unmarshal(h.Public("GET", "/api/interactions/post/all/photo-a", nil).Body.Bytes(), &resp)
	if len(resp) != 1 {
		t.Fatalf("after release %+v", resp)
	}
}

func TestPacing(t *testing.T) {
	for total, want := range map[int]int{0: 0, 1: 1, 360: 1, 361: 2, 3600: 10, 100000: 10} {
		if got := interactions.PerTick(total); got != want {
			t.Errorf("perTick(%d) = %d, want %d", total, got, want)
		}
	}
}

func like(h *companiontest.Harness, method, path, ip, body string) (*httptest.ResponseRecorder, interactions.NativeLikeResponse) {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = ip + ":1234"
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Core.ServeHTTP(w, req)
	var r interactions.NativeLikeResponse
	json.Unmarshal(w.Body.Bytes(), &r)
	return w, r
}

func TestWebsiteLikes(t *testing.T) {
	h, _ := setup(t)
	path := "/api/interactions/native/photo-a/like"
	w, r := like(h, "POST", path, "203.0.113.1", "")
	if w.Code != 200 || !r.Success || r.LikeCount != 1 || r.Token == "" {
		t.Fatalf("like: %d %+v", w.Code, r)
	}
	token := r.Token
	// The same visitor again, by IP or by token: refused.
	if w, r := like(h, "POST", path, "203.0.113.1", ""); w.Code != http.StatusConflict || !r.HasLiked {
		t.Fatalf("same IP: %d %+v", w.Code, r)
	}
	if w, _ := like(h, "POST", path, "203.0.113.9", fmt.Sprintf(`{"token":%q}`, token)); w.Code != http.StatusConflict {
		t.Fatalf("same token: %d", w.Code)
	}
	if _, r := like(h, "POST", path, "198.51.100.2", ""); r.LikeCount != 2 {
		t.Fatalf("second visitor: %+v", r)
	}
	if _, r := like(h, "GET", "/api/interactions/native/photo-a/status?token="+token, "203.0.113.1", ""); !r.HasLiked || r.LikeCount != 2 {
		t.Fatalf("status %+v", r)
	}
	var resp []interactions.LikesResponse
	json.Unmarshal(h.Public("GET", "/api/interactions/post/all/photo-a", nil).Body.Bytes(), &resp)
	if len(resp) != 1 || resp[0] != (interactions.LikesResponse{Platform: "native", Likes: 2}) {
		t.Fatalf("all %+v", resp)
	}
	if w, _ := like(h, "DELETE", path, "203.0.113.1", ""); w.Code != http.StatusBadRequest {
		t.Fatalf("unlike without token: %d", w.Code)
	}
	if w, r := like(h, "DELETE", path, "203.0.113.1", fmt.Sprintf(`{"token":%q}`, token)); w.Code != 200 || r.LikeCount != 1 {
		t.Fatalf("unlike: %d %+v", w.Code, r)
	}
	// Spoofing X-Forwarded-For from the internet does not make a new visitor.
	req := httptest.NewRequest("POST", path, nil)
	req.RemoteAddr = "198.51.100.2:5555"
	req.Header.Set("X-Forwarded-For", "192.0.2.77")
	rec := httptest.NewRecorder()
	h.Core.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("spoofed header: %d", rec.Code)
	}
	var ips []string
	h.Module("interactions").DB().Raw("SELECT ip_hash FROM interactions_native_likes").Scan(&ips)
	for _, ip := range ips {
		if strings.Contains(ip, "198.51") {
			t.Fatal("IP stored in plain text")
		}
	}
}

func TestImportsTheOldLikes(t *testing.T) {
	h, c := setup(t)
	old := companiontest.LegacyDB(t,
		`CREATE TABLE interactions (id integer primary key, source text, item_id text, platform text, target_name text, like_count integer, updated_at datetime)`,
		`CREATE TABLE auto_upload_items (id integer primary key, platform text, item_id text, post_url text, version_id text, post_id text)`,
		`CREATE TABLE native_likes (id integer primary key, item_id text, ip_hash text, token text, created_at datetime)`,
		`INSERT INTO interactions (source, item_id, platform, target_name, like_count) VALUES ('rss', 'a', 'mastodon', 'mastodon', 18), ('rss', 'a', 'native', 'native', 1)`,
		`INSERT INTO auto_upload_items (platform, item_id, post_url, post_id) VALUES ('mastodon', 'a', 'https://m/1', '1'), ('bluesky', 'a', 'at://did/app.bsky.feed.post/x', NULL)`,
		`INSERT INTO native_likes (item_id, ip_hash, token, created_at) VALUES ('a', 'hash', 'tok', '2026-01-01')`)
	old.Targets = []companion.LegacyTarget{{Name: "mastodon", Platform: "mastodon"}, {Name: "bluesky", Platform: "bluesky"}}
	info, _ := companion.Lookup("interactions")
	if err := info.Import(h.Module("interactions"), old); err != nil {
		t.Fatal(err)
	}
	var rows []interactions.Tracked
	h.Module("interactions").DB().Order("target").Find(&rows)
	if len(rows) != 2 || rows[0].Target != "bluesky:bluesky" || rows[0].PostID != "at://did/app.bsky.feed.post/x" || rows[1].Likes != 18 {
		t.Fatalf("tracked %+v", rows)
	}
	var resp []interactions.LikesResponse
	json.Unmarshal(h.Public("GET", "/api/interactions/post/all/a", nil).Body.Bytes(), &resp)
	// Bluesky's likes were never fetched: not listed, as before.
	if len(resp) != 2 || resp[0].Platform != "mastodon" || resp[1] != (interactions.LikesResponse{Platform: "native", Likes: 1}) {
		t.Fatalf("after import %+v", resp)
	}
	_ = c
}
