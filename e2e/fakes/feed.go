package fakes

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"strings"
	"time"
)

// Feed is a photo feed of n items, as the website's gallery serves it:
// /gallery.xml and the images under /img/.
func Feed(base string, n int) http.Handler {
	base = strings.TrimSuffix(base, "/")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /gallery.xml", func(w http.ResponseWriter, r *http.Request) {
		var b strings.Builder
		b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:media="http://search.yahoo.com/mrss/"><channel><title>Gallery</title><link>https://example.com/</link>
`)
		start := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
		for i := 1; i <= n; i++ {
			img := fmt.Sprintf("%s/img/photo-%d.jpg", base, i)
			fmt.Fprintf(&b, `<item><title>Lake at dawn %[1]d</title><guid>photo-%[1]d</guid><link>https://example.com/gallery/%[1]d</link>
<pubDate>%[2]s</pubDate><category>lakes</category><description>&lt;img src="%[3]s" alt="A lake"&gt;</description>
<media:content url="%[3]s" medium="image"/><enclosure url="%[3]s" type="image/jpeg" length="1"/></item>
`, i, start.Add(time.Duration(i)*24*time.Hour).Format(time.RFC1123Z), img)
		}
		b.WriteString("</channel></rss>\n")
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Write([]byte(b.String()))
	})
	mux.HandleFunc("GET /img/{name}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(JPEG(r.PathValue("name")))
	})
	return mux
}

// JPEG is a small landscape-looking JPEG, its colour from the name.
func JPEG(name string) []byte {
	var h uint32
	for _, c := range name {
		h = h*31 + uint32(c)
	}
	img := image.NewRGBA(image.Rect(0, 0, 160, 120))
	base := color.RGBA{uint8(60 + h%140), uint8(80 + (h/7)%120), uint8(90 + (h/49)%130), 255}
	for y := 0; y < 120; y++ {
		for x := 0; x < 160; x++ {
			c := base
			if y > 70 {
				c = color.RGBA{base.R / 2, base.G / 2, base.B / 3, 255}
			}
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80})
	return buf.Bytes()
}
