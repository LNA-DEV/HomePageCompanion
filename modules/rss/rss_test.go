package rss_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/capability/feed"
	"github.com/LNA-DEV/HomePageCompanion/companion"
	"github.com/LNA-DEV/HomePageCompanion/companiontest"
	"github.com/LNA-DEV/HomePageCompanion/modules/rss"
)

// A served feed whose items the test changes.
type feedServer struct {
	mu    sync.Mutex
	items []string
	down  bool
	*httptest.Server
}

func newFeedServer(t *testing.T) *feedServer {
	f := &feedServer{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.down {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/rss+xml")
		fmt.Fprintf(w, `<?xml version="1.0"?>
<rss version="2.0" xmlns:media="http://search.yahoo.com/mrss/" xmlns:lna="https://lna-dev.net/ns/lna">
<channel><title>Gallery</title><link>https://example.com/</link><copyright>Jane Doe</copyright>%s</channel></rss>`, strings.Join(f.items, ""))
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *feedServer) set(items ...string) {
	f.mu.Lock()
	f.items = items
	f.mu.Unlock()
}

const photoA = `<item><title>Eisvogel</title><guid>a</guid><link>https://example.com/gallery/#a</link>
<pubDate>Mon, 02 Jan 2006 15:04:05 +0000</pubDate><category>Vögel</category><category>meta_skip:threads</category>
<description>&lt;img src="x.jpg" alt="Ein Eisvogel am Ast"&gt;</description>
<media:content url="https://example.com/a-small.jpg" medium="image"/>
<enclosure url="https://example.com/a-full.jpg" type="image/jpeg" length="1"/>
<lna:model slug="jane" name="Jane  Doe" autotag="true"><lna:profile platform="Mastodon" url="https://mastodon.social/@jane"/></lna:model>
<lna:model slug="nameless" name=""/>
</item>`

const articleB = `<item><title>Lofoten</title><guid>b</guid><link>https://example.com/posts/lofoten/</link>
<pubDate>Tue, 03 Jan 2006 15:04:05 +0000</pubDate><description>&lt;p&gt;Two weeks &lt;b&gt;north&lt;/b&gt; of the circle.&lt;/p&gt;</description></item>`

const photoC = `<item><title>Salamander</title><guid>c</guid><link>https://example.com/gallery/#c</link>
<pubDate>Wed, 04 Jan 2006 15:04:05 +0000</pubDate><media:content url="https://example.com/c.jpg" medium="image"/></item>`

type listener struct {
	mu     sync.Mutex
	events []feed.ItemDiscovered
}

func (l *listener) HandleEvent(_ context.Context, ev companion.Event) error {
	var d feed.ItemDiscovered
	if err := ev.Decode(&d); err != nil {
		return err
	}
	l.mu.Lock()
	l.events = append(l.events, d)
	l.mu.Unlock()
	return nil
}

func init() {
	companion.Register(companion.Info{ID: "t.listener", New: func() companion.Instance { return new(listener) },
		Subscribes: []string{feed.EventItemDiscovered}})
}

func setup(t *testing.T) (*companiontest.Harness, *feedServer) {
	srv := newFeedServer(t)
	srv.set(photoA, articleB)
	h := companiontest.New(t, fmt.Sprintf(`
modules:
  rss:
    instances:
      photos: { url: %q }
  t.listener: {}
`, srv.URL))
	src := companiontest.Instance[*rss.Source](h, "rss:photos")
	if err := src.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	return h, srv
}

func TestMirrorsTheFeed(t *testing.T) {
	h, _ := setup(t)
	src := companiontest.Instance[*rss.Source](h, "rss:photos")
	items, err := src.ListItems(context.Background())
	if err != nil || len(items) != 2 {
		t.Fatalf("items %v %v", items, err)
	}
	// Newest first: the article of the 3rd, then the photo of the 2nd.
	art, photo := items[0], items[1]
	if art.Kind != feed.KindArticle || art.Image != nil || art.Summary != "Two weeks north of the circle." {
		t.Fatalf("article: %+v", art)
	}
	if photo.Kind != feed.KindPhoto || photo.Image.URL != "https://example.com/a-small.jpg" || photo.Image.OriginalURL != "https://example.com/a-full.jpg" {
		t.Fatalf("photo image: %+v", photo.Image)
	}
	if photo.Image.Alt != "Ein Eisvogel am Ast" || photo.Image.MetadataURL() != "https://example.com/a-full.jpg" {
		t.Fatalf("alt/metadata: %+v", photo.Image)
	}
	if len(photo.People) != 1 || photo.People[0].Name != "Jane Doe" || !photo.People[0].Autotag ||
		photo.People[0].Profiles[0].Platform != "mastodon" {
		t.Fatalf("people: %+v", photo.People)
	}
	if photo.Copyright != "Jane Doe" || photo.FeedTitle != "Gallery" || len(photo.Categories) != 2 {
		t.Fatalf("feed fields: %+v", photo)
	}
	if !photo.Published.Equal(time.Date(2006, 1, 2, 15, 4, 5, 0, time.UTC)) || !photo.HasDate {
		t.Fatalf("published %v", photo.Published)
	}
}

func TestAnnouncesOnlyNewItems(t *testing.T) {
	h, srv := setup(t)
	src := companiontest.Instance[*rss.Source](h, "rss:photos")
	l := companiontest.Instance[*listener](h, "t.listener:default")
	h.Deliver()
	if len(l.events) != 0 {
		t.Fatalf("the first mirror announced %d item(s)", len(l.events))
	}
	srv.set(photoA, articleB, photoC)
	if err := src.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.Deliver()
	if len(l.events) != 1 || l.events[0].Item.ID != "c" || l.events[0].Source != "rss:photos" {
		t.Fatalf("events %+v", l.events)
	}
}

func TestKeepsItemsThatLeftTheFeed(t *testing.T) {
	h, srv := setup(t)
	src := companiontest.Instance[*rss.Source](h, "rss:photos")
	srv.set(articleB)
	src.Refresh(context.Background())
	items, _ := src.ListItems(context.Background())
	if len(items) != 1 {
		t.Fatalf("listed %d", len(items))
	}
	gone, err := src.GetItem(context.Background(), "a")
	if err != nil || gone.Title != "Eisvogel" {
		t.Fatalf("removed item: %+v %v", gone, err)
	}
	if _, err := src.GetItem(context.Background(), "nope"); err != feed.ErrNotFound {
		t.Fatalf("unknown item: %v", err)
	}
	// It comes back when the feed has it again.
	srv.set(photoA, articleB)
	src.Refresh(context.Background())
	if items, _ := src.ListItems(context.Background()); len(items) != 2 {
		t.Fatalf("restored: %d", len(items))
	}
}

func TestOutageKeepsTheMirror(t *testing.T) {
	h, srv := setup(t)
	src := companiontest.Instance[*rss.Source](h, "rss:photos")
	srv.mu.Lock()
	srv.down = true
	srv.mu.Unlock()
	if err := src.Refresh(context.Background()); err == nil {
		t.Fatal("an outage is not an error")
	}
	w := h.Admin("GET", "/api/b/rss/feeds", nil)
	if !strings.Contains(w.Body.String(), "503") || !strings.Contains(w.Body.String(), `"items":2`) {
		t.Fatalf("feeds: %s", w.Body)
	}
}

func TestAdminSearchesAllPages(t *testing.T) {
	h, srv := setup(t)
	var items []string
	for i := 0; i < 60; i++ {
		title := fmt.Sprintf("Photo %d", i)
		if i%20 == 0 {
			title = fmt.Sprintf("Libelle %d", i)
		}
		items = append(items, fmt.Sprintf(`<item><title>%s</title><guid>p%d</guid><pubDate>Mon, 02 Jan 2006 15:%02d:05 +0000</pubDate><media:content url="https://example.com/%d.jpg"/></item>`, title, i, i%60, i))
	}
	srv.set(items...)
	companiontest.Instance[*rss.Source](h, "rss:photos").Refresh(context.Background())

	type page struct {
		Items []struct{ Title string } `json:"items"`
		Total int64                    `json:"total"`
	}
	p := companiontest.JSON[page](h, h.Admin("GET", "/api/b/rss/feeds/photos/items?q=libelle&limit=20", nil))
	if p.Total != 3 || len(p.Items) != 3 {
		t.Fatalf("search found %d of 3 (total %d)", len(p.Items), p.Total)
	}
	p = companiontest.JSON[page](h, h.Admin("GET", "/api/b/rss/feeds/photos/items?page=3&limit=25", nil))
	if p.Total != 60 || len(p.Items) != 10 {
		t.Fatalf("page 3: %d of %d", len(p.Items), p.Total)
	}
	p = companiontest.JSON[page](h, h.Admin("GET", "/api/b/rss/feeds/photos/items?state=removed", nil))
	if p.Total != 2 {
		t.Fatalf("removed: %d", p.Total)
	}
}
