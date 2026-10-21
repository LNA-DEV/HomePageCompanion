// Command legacyfixture writes an installation of this (pre-module)
// companion with a little of everything in it: the fixture the migration's
// end-to-end test moves over and compares (e2e/). The rows go through the
// old models, so the schema is exactly what an installation has.
//
//	GOWORK=off go run ./cmd/legacyfixture -out ../e2e/testdata/legacy
package main

import (
	"bytes"
	"compress/gzip"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"log"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/models"
	"github.com/protomaps/go-pmtiles/pmtiles"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// tokenKey is the fixture's TOKEN_ENCRYPTION_KEY (32 bytes).
var tokenKey = base64.StdEncoding.EncodeToString([]byte("fixture-token-key-0123456789abcd"))

func main() {
	out := flag.String("out", "", "directory to write the installation into (data/ of the old companion)")
	flag.Parse()
	if *out == "" {
		log.Fatal("-out is required")
	}
	if err := os.RemoveAll(*out); err != nil {
		log.Fatal(err)
	}
	for _, d := range []string{"microblog", "trips", "bucket/home-page-companion/maps/basemap", "gbif/density/1/0"} {
		must(os.MkdirAll(filepath.Join(*out, d), 0o755))
	}
	db, err := gorm.Open(sqlite.Open(filepath.Join(*out, "companion.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	must(err)
	must(db.AutoMigrate(&models.Webmention{}, &models.AutoUploadItem{}, &models.VAPIDKey{}, &models.NotificationSubscription{}, &models.Feed{}, &models.FeedItem{},
		&models.Author{}, &models.Category{}, &models.Interaction{}, &models.NativeLike{}, &models.UploadAttempt{}, &models.MicroblogPost{},
		&models.MicroblogPublication{}, &models.MicroblogComment{}, &models.Trip{}, &models.TripStop{}, &models.TripPhoto{}, &models.RouteGeometry{},
		&models.BasemapVersion{}, &models.BasemapPart{}, &models.PlatformToken{}))

	t0 := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	at := func(days int) time.Time { return t0.AddDate(0, 0, days) }
	s := func(v string) *string { return &v }

	// The feed and its items; one of them no longer in the feed.
	mountains, skip := models.Category{Name: "mountains"}, models.Category{Name: "meta_skip:threads"}
	must(db.Create(&mountains).Error)
	must(db.Create(&skip).Error)
	feed := models.Feed{FeedName: "Photo RSS", Title: "Gallery", Description: "Photos", Link: "https://example.com/gallery/",
		FeedURL: "http://127.0.0.1:9/gallery/index.xml", Language: "en", ItemTypes: "image"}
	must(db.Create(&feed).Error)
	items := []models.FeedItem{
		{FeedID: feed.ID, Title: "Watzmann", Description: `<img src="https://example.com/watzmann.jpg" alt="The Watzmann at dawn">`, Link: "https://example.com/gallery/#watzmann",
			ItemType: "image", ImageUrl: "https://example.com/watzmann.jpg", Published: at(-30), GUID: "watzmann", Categories: []models.Category{mountains, skip}},
		{FeedID: feed.ID, Title: "Königssee", Description: `<img src="https://example.com/koenigssee.jpg" alt="Boats on the lake">`, Link: "https://example.com/gallery/#koenigssee",
			ItemType: "image", ImageUrl: "https://example.com/koenigssee.jpg", Published: at(-20), GUID: "koenigssee", Categories: []models.Category{mountains}},
		{FeedID: feed.ID, Title: "Old photo", Link: "https://example.com/gallery/#old", ItemType: "image", ImageUrl: "https://example.com/old.jpg",
			Published: at(-60), GUID: "old-photo"},
	}
	must(db.Create(&items).Error)
	must(db.Delete(&items[2]).Error)

	// Autoposting: publications, attempts (one failure, later fixed).
	must(db.Create(&[]models.AutoUploadItem{
		{Platform: "mastodon", ItemID: "watzmann", PostUrl: s("https://mastodon.example/@me/111"), PostId: s("111"), CreatedAt: at(-29)},
		{Platform: "bluesky", ItemID: "watzmann", PostUrl: s("https://bsky.app/profile/me.example/post/3abc"), PostId: s("at://did:plc:me/app.bsky.feed.post/3abc"), VersionId: s("bafycid"), CreatedAt: at(-29)},
		{Platform: "pixelfed", ItemID: "watzmann", PostUrl: s("https://pixelfed.example/p/me/222"), PostId: s("222"), CreatedAt: at(-28)},
		{Platform: "mastodon", ItemID: "koenigssee", PostUrl: s("https://mastodon.example/@me/333"), PostId: s("333"), CreatedAt: at(-19)},
		{Platform: "mastodon", ItemID: "old-photo", PostUrl: s("https://mastodon.example/@me/99"), PostId: s("99"), CreatedAt: at(-59)},
	}).Error)
	must(db.Create(&[]models.UploadAttempt{
		{Source: "autouploader", ConnectionName: "mastodon", ItemID: "watzmann", Platform: "mastodon", TargetName: "main-mastodon", Success: true, CreatedAt: at(-29)},
		{Source: "autouploader", ConnectionName: "pixelfed", ItemID: "watzmann", Platform: "pixelfed", TargetName: "main-pixelfed", Success: false,
			ErrorCode: "rate_limited", ErrorMessage: "status: 429", HTTPStatus: 429, CreatedAt: at(-29)},
		{Source: "autouploader", ConnectionName: "pixelfed", ItemID: "watzmann", Platform: "pixelfed", TargetName: "main-pixelfed", Success: true, CreatedAt: at(-28)},
		{Source: "microblog", ItemID: "hello", Platform: "mastodon", TargetName: "main-mastodon", Success: true, CreatedAt: at(-10)},
	}).Error)

	// Likes: per platform, and the website's own.
	must(db.Create(&[]models.Interaction{
		{Source: "rss", ItemID: "watzmann", Platform: "mastodon", TargetName: "main-mastodon", LikeCount: 12, CreatedAt: at(-28), UpdatedAt: at(-1)},
		{Source: "rss", ItemID: "watzmann", Platform: "bluesky", TargetName: "main-bluesky", LikeCount: 5, CreatedAt: at(-28), UpdatedAt: at(-1)},
		{Source: "rss", ItemID: "watzmann", Platform: "native", TargetName: "native", LikeCount: 2, CreatedAt: at(-27), UpdatedAt: at(-2)},
		{Source: "rss", ItemID: "koenigssee", Platform: "mastodon", TargetName: "main-mastodon", LikeCount: 4, CreatedAt: at(-18), UpdatedAt: at(-1)},
		{Source: "microblog", ItemID: "hello", Platform: "mastodon", TargetName: "main-mastodon", LikeCount: 7, CreatedAt: at(-9), UpdatedAt: at(-1)},
	}).Error)
	must(db.Create(&[]models.NativeLike{
		{ItemID: "watzmann", IPHash: hash("198.51.100.7"), Token: "tok-a", CreatedAt: at(-27)},
		{ItemID: "watzmann", IPHash: hash("198.51.100.8"), Token: "tok-b", CreatedAt: at(-26)},
	}).Error)

	// The microblog: a post with an uploaded image, published to Mastodon,
	// with a reply.
	img := jpegBytes(color.RGBA{R: 200, G: 120, B: 40, A: 255})
	sum := sha256.Sum256(img)
	mbName := hex.EncodeToString(sum[:]) + ".jpg"
	must(os.WriteFile(filepath.Join(*out, "microblog", mbName), img, 0o644))
	post := models.MicroblogPost{Slug: "hello", Body: "Hello from the mountains", ImageURL: "/api/microblog/media/" + mbName, ImageAltText: "A summit cross",
		CreatedAt: at(-10), UpdatedAt: at(-10)}
	must(db.Create(&post).Error)
	plain := models.MicroblogPost{Slug: "second", Body: "Just text", ContentWarning: "weather", CreatedAt: at(-5), UpdatedAt: at(-5)}
	must(db.Create(&plain).Error)
	must(db.Create(&[]models.MicroblogPublication{
		{PostID: post.ID, TargetName: "main-mastodon", Platform: "mastodon", PostUrl: s("https://mastodon.example/@me/444"), ExternalID: s("444"), Success: true,
			LikesRefreshedAt: ptr(at(-1)), CommentsRefreshedAt: ptr(at(-1)), CreatedAt: at(-10), UpdatedAt: at(-1)},
		{PostID: post.ID, TargetName: "main-bluesky", Platform: "bluesky", Success: false, ErrorMessage: `microblog: unsupported platform "bluesky"`, CreatedAt: at(-10), UpdatedAt: at(-10)},
		{PostID: plain.ID, TargetName: "main-mastodon", Platform: "mastodon", PostUrl: s("https://mastodon.example/@me/555"), ExternalID: s("555"), Success: true, CreatedAt: at(-5), UpdatedAt: at(-5)},
	}).Error)
	must(db.Create(&models.MicroblogComment{PostID: post.ID, Platform: "mastodon", ExternalID: "r1", Author: "Jane", AuthorURL: "https://mastodon.example/@jane",
		AvatarURL: "https://mastodon.example/a.png", Body: "<p>Lovely!</p>", PostedAt: at(-9), ImportedAt: at(-8)}).Error)

	// A trip in the past (so its day count is fixed) with a routed leg.
	tripImg := jpegBytes(color.RGBA{R: 30, G: 90, B: 160, A: 255})
	tsum := sha256.Sum256(tripImg)
	tripName := hex.EncodeToString(tsum[:]) + ".jpg"
	must(os.WriteFile(filepath.Join(*out, "trips", tripName), tripImg, 0o644))
	days, dist := 9, 650
	trip := models.Trip{Slug: "scandinavia", Title: "Scandinavia", Published: true, DaysTotal: &days, CreatedAt: at(-40), UpdatedAt: at(-30)}
	must(db.Create(&trip).Error)
	stops := []models.TripStop{
		{TripID: trip.ID, Position: 0, StopKey: "munich", Name: "Munich", StartDate: "2026-07-01", EndDate: "2026-07-02", Lat: 48.137, Lng: 11.575, Status: "visited", Country: "Germany"},
		{TripID: trip.ID, Position: 1, StopKey: "malmo", Name: "Malmö", StartDate: "2026-07-03", EndDate: "2026-07-05", Lat: 55.605, Lng: 13.0, Status: "visited", Country: "Sweden",
			TransportMode: "car", TransportLabel: "Drive", TransportDuration: "9h", DistanceKm: &dist, TransportCountries: []string{"Denmark"},
			TransportWaypoints: []models.Waypoint{{Lat: 53.55, Lng: 10.0}}},
		{TripID: trip.ID, Position: 2, StopKey: "gotland", Name: "Gotland", StartDate: "2026-07-06", Lat: 57.64, Lng: 18.29, Status: "visited", Country: "Sweden",
			TransportMode: "flight", TransportLabel: "Fly"},
	}
	must(db.Create(&stops).Error)
	must(db.Create(&[]models.TripPhoto{
		{StopID: stops[0].ID, Kind: "stop", Position: 0, URL: "/api/trips/media/" + tripName, Caption: "Marienplatz", Alt: "A square"},
		{StopID: stops[1].ID, Kind: "transport", Position: 0, URL: "", Caption: "On the bridge", Tint: "#336699"},
	}).Error)
	// The old key of the Munich → Malmö leg (routing.Key).
	must(db.Create(&models.RouteGeometry{Key: "0cc75712e58d178ba964af9a39b89030", Mode: "car", Status: "ok", Source: "osrm",
		Points: [][2]float64{{48.137, 11.575}, {53.55, 10.0}, {55.605, 13.0}}, FetchedAt: at(-30)}).Error)

	// The basemap: one active version in the file bucket.
	arch := pmtilesArchive()
	must(os.WriteFile(filepath.Join(*out, "bucket/home-page-companion/maps/basemap/20261005.pmtiles"), arch, 0o644))
	must(db.Create(&models.BasemapVersion{Version: "20261005", Key: "home-page-companion/maps/basemap/20261005.pmtiles", Size: int64(len(arch)),
		SourceURL: "https://build.protomaps.com/20261005.pmtiles", SchemaVersion: "4.15.2", OSMTime: "2026-10-05T04:00:00Z", Status: models.BasemapActive,
		PartSize: 64 << 20, PartsTotal: 1, CreatedAt: at(35), UpdatedAt: at(35), ActivatedAt: ptr(at(35))}).Error)

	// Web push, webmentions, a refreshed Instagram token.
	must(db.Create(&models.VAPIDKey{PublicKey: "BFixturePublicKey0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789ab",
		PrivateKey: "fixture-private-key", CreatedAt: at(-100)}).Error)
	must(db.Create(&models.NotificationSubscription{Endpoint: "https://push.example/sub/1", Auth: "auth-1", P256dh: "p256dh-1"}).Error)
	must(db.Create(&models.Webmention{Source: "https://other.example/post", Target: "https://example.com/gallery/", CreatedAt: at(-3)}).Error)
	sealed, err := seal("main-instagram", "IGQ-refreshed-token")
	must(err)
	must(db.Create(&models.PlatformToken{TargetName: "main-instagram", Platform: "instagram", AccessToken: sealed, SeedHash: hash("IGQ-seed"),
		RefreshedAt: ptr(at(-7)), ExpiresAt: ptr(at(53)), LastAttemptAt: ptr(at(-7))}).Error)

	// The GBIF upstream the test serves: one tile.
	must(os.WriteFile(filepath.Join(*out, "gbif/density/1/0/0@1x.png"), []byte("\x89PNG fixture tile"), 0o644))

	must(os.WriteFile(filepath.Join(*out, "config.yaml"), []byte(config), 0o644))
	must(os.WriteFile(filepath.Join(*out, ".env"), []byte(fmt.Sprintf(env, tokenKey)), 0o600))
	sqlDB, _ := db.DB()
	sqlDB.Close()
	fmt.Println("wrote", *out)
}

const config = `security:
  apiKey: ${ADMIN_API_KEY}
  ipHashSalt: ${IP_HASH_SALT}
  domain: example.com

datasources:
  rss:
    - name: "Photo RSS"
      feedUrl: "http://127.0.0.1:9/gallery/index.xml"
      itemType: image

targets:
  - name: main-pixelfed
    platform: pixelfed
    pat: ${PIXELFED_PAT}
    instance: https://pixelfed.example
  - name: main-bluesky
    platform: bluesky
    username: me.example
    pat: ${BLUESKY_PAT}
  - name: main-instagram
    platform: instagram
    accessToken: ${INSTAGRAM_ACCESS_TOKEN}
    accountId: "1789"
  - name: main-mastodon
    platform: mastodon
    instance: https://mastodon.example
    pat: ${MASTODON_PAT}

connections:
  - name: mastodon
    sourceName: Photo RSS
    targetName: main-mastodon
    caption: "More at https://example.com/gallery"
    cron: "0 0 18 * * *"
  - name: pixelfed
    sourceName: Photo RSS
    targetName: main-pixelfed
    includeAltText: true

microblog:
  publishTo: [main-mastodon, main-bluesky]

webpush:
  subscriberMail: mailto:admin@example.com

storage:
  bucketUrl: "file://${FIXTURE_BUCKET}"
  prefix: home-page-companion

basemap:
  enabled: true
  schedule: ""
  publicUrl: https://companion.test/api/tiles/basemap

gbif:
  baseUrl: ${FIXTURE_GBIF}
`

const env = `ADMIN_API_KEY=fixture-admin-key
IP_HASH_SALT=fixture-salt
PIXELFED_PAT=px-token
BLUESKY_PAT=bsky-app-password
INSTAGRAM_ACCESS_TOKEN=IGQ-seed
MASTODON_PAT=masto-token
TOKEN_ENCRYPTION_KEY=%s
`

func hash(s string) string {
	sum := sha256.Sum256([]byte(s + "fixture-salt"))
	return hex.EncodeToString(sum[:])
}

func ptr(t time.Time) *time.Time { return &t }

func seal(target, token string) (string, error) {
	key, _ := base64.StdEncoding.DecodeString(tokenKey)
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	return base64.StdEncoding.EncodeToString(gcm.Seal(nonce, nonce, []byte(token), []byte(target))), nil
}

func jpegBytes(c color.RGBA) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for x := 0; x < 8; x++ {
		for y := 0; y < 8; y++ {
			img.Set(x, y, c)
		}
	}
	var b bytes.Buffer
	must(jpeg.Encode(&b, img, nil))
	return b.Bytes()
}

// pmtilesArchive is a tiny PMTiles v3 file with gzip tiles at z0 and z1.
func pmtilesArchive() []byte {
	gz := func(s string) []byte {
		var b bytes.Buffer
		w := gzip.NewWriter(&b)
		w.Write([]byte(s))
		w.Close()
		return b.Bytes()
	}
	type tile struct {
		id   uint64
		data []byte
	}
	var tiles []tile
	for _, a := range [][3]uint32{{0, 0, 0}, {1, 0, 0}, {1, 1, 1}} {
		tiles = append(tiles, tile{pmtiles.ZxyToID(uint8(a[0]), a[1], a[2]), gz(fmt.Sprintf("tile %d/%d/%d", a[0], a[1], a[2]))})
	}
	sort.Slice(tiles, func(i, j int) bool { return tiles[i].id < tiles[j].id })
	var data bytes.Buffer
	var entries []pmtiles.EntryV3
	for _, t := range tiles {
		entries = append(entries, pmtiles.EntryV3{TileID: t.id, Offset: uint64(data.Len()), Length: uint32(len(t.data)), RunLength: 1})
		data.Write(t.data)
	}
	root := pmtiles.SerializeEntries(entries, pmtiles.Gzip)
	meta, err := pmtiles.SerializeMetadata(map[string]interface{}{"version": "4.15.2", "attribution": "© OpenStreetMap"}, pmtiles.Gzip)
	must(err)
	h := pmtiles.HeaderV3{
		SpecVersion: 3, RootOffset: pmtiles.HeaderV3LenBytes, RootLength: uint64(len(root)),
		MetadataOffset: pmtiles.HeaderV3LenBytes + uint64(len(root)), MetadataLength: uint64(len(meta)),
		TileDataOffset: pmtiles.HeaderV3LenBytes + uint64(len(root)) + uint64(len(meta)), TileDataLength: uint64(data.Len()),
		AddressedTilesCount: uint64(len(tiles)), TileEntriesCount: uint64(len(tiles)), TileContentsCount: uint64(len(tiles)),
		Clustered: true, InternalCompression: pmtiles.Gzip, TileCompression: pmtiles.Gzip, TileType: pmtiles.Mvt,
		MinZoom: 0, MaxZoom: 15, MinLonE7: -1800000000, MinLatE7: -850511287, MaxLonE7: 1800000000, MaxLatE7: 850511287,
	}
	h.LeafDirectoryOffset = h.TileDataOffset
	var out bytes.Buffer
	out.Write(pmtiles.SerializeHeader(h))
	out.Write(root)
	out.Write(meta)
	out.Write(data.Bytes())
	return out.Bytes()
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
