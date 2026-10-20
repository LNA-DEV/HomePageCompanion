package inventory

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/LNA-DEV/HomePageCompanion/config"
	"github.com/LNA-DEV/HomePageCompanion/database"
	"github.com/LNA-DEV/HomePageCompanion/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func useDB(t *testing.T) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.Feed{}, &models.FeedItem{}, &models.Author{}, &models.Category{}); err != nil {
		t.Fatal(err)
	}
	prev := database.Db
	database.Db = db
	t.Cleanup(func() { database.Db = prev })
}

func useFeeds(t *testing.T, sources ...config.Datasource) {
	t.Helper()
	prev := config.Data
	config.Data.Datasources.Rss = sources
	t.Cleanup(func() { config.Data = prev })
}

// A feed that cannot be fetched must not stop the process: the next run
// tries again (autoposting-ui.md §3 #1).
func TestUnreachableFeedKeepsRunning(t *testing.T) {
	useDB(t)
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer down.Close()
	useFeeds(t, config.Datasource{Name: "photos", FeedURL: down.URL, ItemType: "image"})

	PopulateDatabase()

	var n int64
	database.Db.Model(&models.Feed{}).Count(&n)
	if n != 0 {
		t.Fatalf("feeds stored from a failed fetch: %d", n)
	}
}

const twoItems = `<?xml version="1.0"?>
<rss version="2.0" xmlns:media="http://search.yahoo.com/mrss/"><channel>
<title>Photos</title><link>https://example.com/</link>
<item><title>A</title><guid>a</guid><link>https://example.com/a</link><pubDate>Mon, 02 Jan 2006 15:04:05 +0000</pubDate><media:content url="https://example.com/a.jpg" medium="image"/></item>
<item><title>B</title><guid>b</guid><link>https://example.com/b</link><pubDate>Tue, 03 Jan 2006 15:04:05 +0000</pubDate></item>
</channel></rss>`

// An item without an image must not panic the inventory.
func TestItemWithoutImage(t *testing.T) {
	useDB(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Write([]byte(twoItems))
	}))
	defer srv.Close()
	useFeeds(t, config.Datasource{Name: "photos", FeedURL: srv.URL, ItemType: "image"})

	PopulateDatabase()

	var items []models.FeedItem
	database.Db.Order("guid").Find(&items)
	if len(items) != 2 {
		t.Fatalf("items: %d, want 2", len(items))
	}
	if items[0].ImageUrl != "https://example.com/a.jpg" || items[1].ImageUrl != "" {
		t.Fatalf("image urls: %q %q", items[0].ImageUrl, items[1].ImageUrl)
	}
}
