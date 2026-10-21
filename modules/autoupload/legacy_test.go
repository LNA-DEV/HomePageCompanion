package autoupload_test

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/capability/feed"
	"github.com/LNA-DEV/HomePageCompanion/capability/social"
	"github.com/LNA-DEV/HomePageCompanion/companion"
	"github.com/LNA-DEV/HomePageCompanion/companiontest"
	"github.com/LNA-DEV/HomePageCompanion/modules/autoupload"
)

// The old tables of a production companion.db, shaped as GORM created them.
var oldSchema = []string{
	`CREATE TABLE feeds (id integer primary key, created_at datetime, updated_at datetime, deleted_at datetime, feed_name text, title text, description text, link text, feed_url text, language text, copyright text, generator text, item_types text)`,
	`CREATE TABLE feed_items (id integer primary key, created_at datetime, updated_at datetime, deleted_at datetime, feed_id integer, title text, description text, link text, item_type text, image_url text, published datetime, guid text)`,
	`CREATE TABLE auto_upload_items (id integer primary key, platform text, item_id text, post_url text, version_id text, post_id text, created_at datetime)`,
	`CREATE TABLE upload_attempts (id integer primary key, source text, connection_name text, item_id text, platform text, target_name text, success numeric, error_code text, error_message text, http_status integer, created_at datetime)`,
	`CREATE TABLE interactions (id integer primary key, source text, item_id text, platform text, target_name text, like_count integer, created_at datetime, updated_at datetime)`,
	`INSERT INTO feeds (id, feed_name, title, feed_url) VALUES (1, 'photos', 'Gallery', 'https://example.com/index.xml')`,
	`INSERT INTO feed_items (id, feed_id, title, link, image_url, published, guid) VALUES (1, 1, 'Eisvogel', 'https://example.com/#a', 'https://example.com/a.jpg', '2024-10-19 09:00:00', 'a')`,
	`INSERT INTO feed_items (id, feed_id, title, link, image_url, published, guid, deleted_at) VALUES (2, 1, 'Gone', 'https://example.com/#b', 'https://example.com/b.jpg', '2023-01-01 09:00:00', 'b', '2026-01-01 00:00:00')`,
	`INSERT INTO auto_upload_items (platform, item_id, post_url, version_id, post_id, created_at) VALUES
		('mastodon', 'a', 'https://m/1', NULL, '1', '2026-09-01 18:00:00'),
		('bluesky', 'a', 'at://did:plc:me/app.bsky.feed.post/3l5x', 'cid1', NULL, '2026-09-01 18:00:00'),
		('bluesky', 'b', 'at://did:plc:me/app.bsky.feed.post/3l5y', 'cid2', NULL, '2025-01-01 18:00:00'),
		('pixelfed', 'renamed-photo', 'https://px/p/9', NULL, '9', '2025-02-01 18:00:00')`,
	`INSERT INTO upload_attempts (source, connection_name, item_id, platform, target_name, success, error_code, error_message, http_status, created_at) VALUES
		('autouploader', 'photos-mastodon', 'a', 'mastodon', 'mastodon', 0, 'server', 'status: 503', 503, '2026-08-31 18:00:00'),
		('autouploader', 'photos-mastodon', 'a', 'mastodon', 'mastodon', 1, '', '', 0, '2026-09-01 18:00:00'),
		('autouploader', 'photos-bluesky', 'c', 'bluesky', 'bluesky', 0, 'auth', 'expired', 401, '2026-09-02 18:00:00')`,
	`INSERT INTO interactions (source, item_id, platform, target_name, like_count, updated_at) VALUES
		('rss', 'a', 'mastodon', 'mastodon', 18, '2026-10-01 00:00:00'),
		('rss', 'a', 'bluesky', 'bluesky', 12, '2026-10-01 00:00:00'),
		('rss', 'a', 'native', 'native', 7, '2026-10-01 00:00:00')`,
}

func TestImportsTheOldTables(t *testing.T) {
	h := companiontest.New(t, `
modules:
  fake.source: { instances: { photos: {} } }
  fake.publisher: { instances: { mastodon: { platform: mastodon }, bluesky: { platform: bluesky } } }
  autoupload:
    instances:
      photos-mastodon: { source: fake.source:photos, target: fake.publisher:mastodon }
`)
	old := companiontest.LegacyDB(t, oldSchema...)
	// The old targets became instances of the platform modules.
	old.Targets = []companion.LegacyTarget{{Name: "mastodon", Platform: "mastodon"}, {Name: "bluesky", Platform: "bluesky"}, {Name: "pixelfed", Platform: "pixelfed"}}
	old.Connections = []companion.LegacyConnection{{Name: "photos-mastodon", SourceName: "photos", TargetName: "mastodon"}}
	info, _ := companion.Lookup("autoupload")
	if err := info.Import(h.Module("autoupload"), old); err != nil {
		t.Fatal(err)
	}
	db := h.Module("autoupload").DB()
	var pubs []autoupload.Publication
	db.Order("item_id, target").Find(&pubs)
	if len(pubs) != 4 {
		t.Fatalf("publications %+v", pubs)
	}
	var bsky autoupload.Publication
	db.Where("item_id = ? AND target = ?", "a", "bluesky:bluesky").First(&bsky)
	if bsky.PostID != "at://did:plc:me/app.bsky.feed.post/3l5x" || bsky.Version != "cid1" || bsky.PostURL != "https://bsky.app/profile/did:plc:me/post/3l5x" || bsky.Likes != 12 {
		t.Fatalf("bluesky publication %+v", bsky)
	}
	var masto autoupload.Publication
	db.Where("item_id = ? AND target = ?", "a", "mastodon:mastodon").First(&masto)
	if masto.PostID != "1" || masto.Connection != "photos-mastodon" || masto.Likes != 18 {
		t.Fatalf("mastodon publication %+v", masto)
	}
	// The 503 was followed by a success: resolved. The expired token is
	// still open, an account problem.
	var open []autoupload.Attempt
	db.Where("success = 0 AND resolved_at IS NULL").Find(&open)
	if len(open) != 1 || open[0].Cause != social.CauseAccount || open[0].ItemID != "c" {
		t.Fatalf("open failures %+v", open)
	}
	// Photos no longer in the feed keep a row, and website likes count apart.
	var posts []autoupload.Post
	db.Order("item_id").Find(&posts)
	got := map[string]autoupload.Post{}
	for _, p := range posts {
		got[p.ItemID] = p
	}
	if !got["a"].InFeed || got["a"].WebsiteLikes != 7 || got["b"].InFeed || got["renamed-photo"].InFeed || got["renamed-photo"].ItemID == "" {
		t.Fatalf("posts %+v", posts)
	}
}

func jpegOf(seed uint8) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for x := 0; x < 64; x++ {
		for y := 0; y < 64; y++ {
			v := uint8(x*4) ^ seed
			if (x/8+y/8)%2 == 0 {
				v = uint8(y*4) + seed
			}
			img.Set(x, y, color.RGBA{v, v, v, 255})
		}
	}
	var b bytes.Buffer
	jpeg.Encode(&b, img, &jpeg.Options{Quality: 90})
	return b.Bytes()
}

func TestBackfillMatchesByImage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/feed-a.jpg", "/platform-a.jpg":
			w.Write(jpegOf(0))
		default:
			w.Write(jpegOf(120))
		}
	}))
	defer srv.Close()
	h := companiontest.New(t, `
modules:
  fake.source: { instances: { photos: {} } }
  fake.publisher: { instances: { px: { platform: pixelfed } } }
  autoupload:
    instances:
      photos-px: { source: fake.source:photos, target: fake.publisher:px }
`)
	src := companiontest.Instance[*companiontest.FakeSource](h, "fake.source:photos")
	src.SetItems(feed.Item{ID: "a", Kind: feed.KindPhoto, Image: &feed.Image{URL: srv.URL + "/feed-a.jpg"}, Published: time.Now(), HasDate: true})
	pub := companiontest.Instance[*companiontest.FakePublisher](h, "fake.publisher:px")
	pub.Posts = []social.ListedPost{
		{Ref: social.Result{ID: "other", URL: "https://px/other"}, ImageURLs: []string{srv.URL + "/other.jpg"}},
		{Ref: social.Result{ID: "p9", URL: "https://px/p9"}, ImageURLs: []string{srv.URL + "/platform-a.jpg"}},
	}
	db := h.Module("autoupload").DB()
	db.Create(&autoupload.Publication{ItemID: "a", Target: "fake.publisher:px", Platform: "pixelfed", Status: autoupload.StatusPublished, PublishedAt: time.Now()})
	h.MustRunJob("autoupload:photos-px/backfill")
	var p autoupload.Publication
	db.Where("item_id = ?", "a").First(&p)
	if p.PostID != "p9" || !strings.HasSuffix(p.PostURL, "/p9") {
		t.Fatalf("backfilled %+v", p)
	}
	_ = context.Background
}
