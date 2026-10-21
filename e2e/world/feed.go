package world

import (
	"bytes"
	"fmt"
	"html"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"
)

// Base is the date of the newest generated post; the others are a day
// apart before it. Fixed, so every run sees the same feed.
var Base = time.Date(2026, 9, 30, 18, 0, 0, 0, time.UTC)

var (
	places = []string{"Watzmann", "Königssee", "Eibsee", "Zugspitze", "Herzogstand", "Walchensee", "Karwendel", "Jochberg", "Tegernsee", "Wendelstein",
		"Geroldsee", "Partnach", "Hintersee", "Ramsau", "Kehlstein", "Obersee", "Funtensee", "Schrecksee", "Seealpsee", "Benediktenwand"}
	moods = []string{"at dawn", "in fog", "after rain", "at dusk", "in winter", "from above"}
	tags  = []string{"mountains", "lakes", "birds", "forest"}
)

// Item is one generated feed entry.
type Item struct {
	GUID      string
	Kind      string // photo | article
	Title     string
	Link      string
	Published time.Time
	Tags      []string
	Alt       string
	Model     string // a person with autotag, for some photos
}

// Photos are n photos, newest first: photo-001 … photo-n.
func Photos(n int) []Item {
	out := make([]Item, n)
	for i := range out {
		title := places[i%len(places)] + " " + moods[(i/len(places))%len(moods)]
		it := Item{GUID: fmt.Sprintf("photo-%03d", i+1), Kind: "photo", Title: title,
			Link: fmt.Sprintf("https://example.com/gallery/#photo-%03d", i+1), Published: Base.AddDate(0, 0, -i),
			Tags: []string{tags[i%len(tags)]}, Alt: "A photo of " + strings.ToLower(title)}
		if i%7 == 3 {
			it.Model = "Jane Doe"
		}
		out[i] = it
	}
	return out
}

// Articles are n blog posts, newest first, between the photos' dates.
func Articles(n int) []Item {
	out := make([]Item, n)
	for i := range out {
		out[i] = Item{GUID: fmt.Sprintf("post-%02d", i+1), Kind: "article", Title: fmt.Sprintf("Trip report %d", i+1),
			Link: fmt.Sprintf("https://example.com/posts/trip-report-%d/", i+1), Published: Base.AddDate(0, 0, -3*i).Add(-time.Hour),
			Tags: []string{"travel"}}
	}
	return out
}

// Feed serves the gallery feed, the blog feed and the photos' images.
type Feed struct {
	mu       sync.Mutex
	photos   []Item
	articles []Item
	gone     int // status while the feed is gone; 0 while it is there
	requests int
	*httptest.Server
}

func NewFeed(photos, articles []Item) *Feed {
	f := &Feed{photos: photos, articles: articles}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /gallery.xml", func(w http.ResponseWriter, r *http.Request) { f.serve(w, "Gallery", "gallery") })
	mux.HandleFunc("GET /blog.xml", func(w http.ResponseWriter, r *http.Request) { f.serve(w, "Blog", "blog") })
	mux.HandleFunc("GET /img/{name}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(photoJPEG(r.PathValue("name")))
	})
	f.Server = httptest.NewServer(mux)
	return f
}

// Gone makes both feeds answer status (404, 410, 503) until Back.
func (f *Feed) Gone(status int) { f.mu.Lock(); f.gone = status; f.mu.Unlock() }

// Back serves the feeds again.
func (f *Feed) Back() { f.mu.Lock(); f.gone = 0; f.mu.Unlock() }

// Remove takes an item out of its feed, as when a photo is deleted from
// the gallery.
func (f *Feed) Remove(guid string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.photos = without(f.photos, guid)
	f.articles = without(f.articles, guid)
}

// Requests is how often a feed was fetched.
func (f *Feed) Requests() int { f.mu.Lock(); defer f.mu.Unlock(); return f.requests }

func without(items []Item, guid string) []Item {
	var out []Item
	for _, it := range items {
		if it.GUID != guid {
			out = append(out, it)
		}
	}
	return out
}

// ImageURL is where the feed serves a photo.
func (f *Feed) ImageURL(guid string) string { return f.URL + "/img/" + guid + ".jpg" }

func (f *Feed) serve(w http.ResponseWriter, title, which string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests++
	if f.gone != 0 {
		http.Error(w, "gone", f.gone)
		return
	}
	items := f.photos
	if which == "blog" {
		items = f.articles
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:media="http://search.yahoo.com/mrss/" xmlns:lna="https://lna-dev.net/ns/lna">
<channel><title>%s</title><link>https://example.com/</link><copyright>Lukas Nagel</copyright>
`, title)
	for _, it := range items {
		fmt.Fprintf(&b, "<item><title>%s</title><guid>%s</guid><link>%s</link><pubDate>%s</pubDate>\n",
			html.EscapeString(it.Title), it.GUID, it.Link, it.Published.Format(time.RFC1123Z))
		for _, t := range it.Tags {
			fmt.Fprintf(&b, "<category>%s</category>", t)
		}
		if it.Kind == "photo" {
			fmt.Fprintf(&b, `<description>&lt;img src="%[1]s" alt="%[2]s"&gt;</description>
<media:content url="%[1]s" medium="image"/><enclosure url="%[1]s" type="image/jpeg" length="1"/>
`, f.ImageURL(it.GUID), html.EscapeString(it.Alt))
			if it.Model != "" {
				// No profile: resolving one would go out to the internet.
				fmt.Fprintf(&b, `<lna:model slug="jane" name="%s"/>`, it.Model)
			}
		} else {
			fmt.Fprintf(&b, "<description>&lt;p&gt;%s: two weeks on the road.&lt;/p&gt;</description>", html.EscapeString(it.Title))
		}
		b.WriteString("</item>\n")
	}
	b.WriteString("</channel></rss>\n")
	w.Header().Set("Content-Type", "application/rss+xml")
	w.Write([]byte(b.String()))
}

// photoJPEG is a small JPEG with a colour of its own per name.
func photoJPEG(name string) []byte {
	var h uint32
	for _, c := range name {
		h = h*31 + uint32(c)
	}
	img := image.NewRGBA(image.Rect(0, 0, 160, 120))
	base := color.RGBA{uint8(60 + h%140), uint8(80 + (h/7)%120), uint8(90 + (h/49)%130), 255}
	for y := 0; y < 120; y++ {
		for x := 0; x < 160; x++ {
			// A horizon, so it looks like a landscape in the UI.
			c := base
			if y > 70 {
				c = color.RGBA{base.R / 2, base.G / 2, base.B / 3, 255}
			}
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80})
	return buf.Bytes()
}
