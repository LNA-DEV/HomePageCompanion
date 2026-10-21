// Package world is the integration setup (docs/concepts/modular-architecture.md
// §6): the core with every standard module as one program, a local test
// feed and fake platforms that can fail the way real ones do. The
// integration tests use it, and so does the UI server the Playwright tests
// run against (cmd/uiserver -world).
package world

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/core"
	_ "github.com/LNA-DEV/HomePageCompanion/e2e" // every building block
	bskyapi "github.com/LNA-DEV/HomePageCompanion/modules/bluesky/api"
	"github.com/LNA-DEV/HomePageCompanion/modules/instagram"
	igapi "github.com/LNA-DEV/HomePageCompanion/modules/instagram/api"
	"github.com/LNA-DEV/HomePageCompanion/modules/interactions"
	"github.com/LNA-DEV/HomePageCompanion/modules/threads"
	thapi "github.com/LNA-DEV/HomePageCompanion/modules/threads/api"
)

// AdminKey is the setup's admin key.
const AdminKey = "world-admin-key"

// Options shape a world.
type Options struct {
	Photos, Articles int    // the size of the test feeds (60 and 8 when 0)
	Dir              string // where config and data go; a new temporary directory when empty
	Listen           string // the companion's address; a free local port when empty
	Seed             bool   // fill it with runs, failures, likes and posts (Seed)
	Logs             io.Writer
	// Core adjusts the core's options, e.g. the admin shell for the UI
	// server.
	Core func(*core.Options)
}

// World is a running setup.
type World struct {
	Feed      *Feed
	Mastodon  *Fedi
	Pixelfed  *Fedi
	Bluesky   *PDS
	Instagram *Graph
	Threads   *Graph
	Push      *Push

	URL    string // the companion
	Dir    string
	Config string
	Data   string
	Core   *core.Core

	opts    Options
	tempDir bool
	ln      net.Listener
	srv     *http.Server
	handler atomic.Pointer[http.Handler]
	restore []func()
}

// Start builds the fakes, writes companion.yaml and starts the core.
func Start(o Options) (*World, error) {
	if o.Photos == 0 {
		o.Photos = 60
	}
	if o.Articles == 0 {
		o.Articles = 8
	}
	if o.Logs == nil {
		o.Logs = io.Discard
	}
	w := &World{opts: o, Dir: o.Dir}
	if w.Dir == "" {
		d, err := os.MkdirTemp("", "companion-world-")
		if err != nil {
			return nil, err
		}
		w.Dir, w.tempDir = d, true
	}
	w.Config, w.Data = filepath.Join(w.Dir, "companion.yaml"), filepath.Join(w.Dir, "data")

	w.Feed = NewFeed(Photos(o.Photos), Articles(o.Articles))
	w.Mastodon, w.Pixelfed = NewFedi("masto-token"), NewFedi("pixel-token")
	w.Bluesky = newPDS("app-password")
	w.Instagram, w.Threads = newGraph("ig-token", "17841", false), newGraph("th-token", "5550", true)
	w.Push = newPush()

	// Instagram's and Threads' clients have their endpoints in package
	// variables; Bluesky's public profile lookups too.
	prevIG := []string{igapi.GraphURL, igapi.RefreshURL}
	prevTH := []string{thapi.GraphURL, thapi.RefreshURL}
	prevBsky := bskyapi.PublicURL
	igapi.GraphURL, igapi.RefreshURL = w.Instagram.URL+"/", w.Instagram.URL+"/refresh"
	thapi.GraphURL, thapi.RefreshURL = w.Threads.URL+"/", w.Threads.URL+"/refresh"
	bskyapi.PublicURL = w.Bluesky.URL
	w.restore = append(w.restore, func() {
		igapi.GraphURL, igapi.RefreshURL = prevIG[0], prevIG[1]
		thapi.GraphURL, thapi.RefreshURL = prevTH[0], prevTH[1]
		bskyapi.PublicURL = prevBsky
	})

	addr := o.Listen
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		w.Close()
		return nil, err
	}
	w.ln, w.URL = ln, "http://"+ln.Addr().String()
	if err := os.WriteFile(w.Config, []byte(w.config()), 0o600); err != nil {
		w.Close()
		return nil, err
	}
	os.Setenv("COMPANION_SECRET_KEY", "d29ybGQtc2VjcmV0LWtleS0zMi1ieXRlcy1sb25nISE=")
	if err := w.startCore(); err != nil {
		w.Close()
		return nil, err
	}
	w.srv = &http.Server{Handler: http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		(*w.handler.Load()).ServeHTTP(rw, r)
	})}
	go w.srv.Serve(ln)
	if o.Seed {
		if err := w.Seed(); err != nil {
			w.Close()
			return nil, fmt.Errorf("seed: %w", err)
		}
	}
	return w, nil
}

func (w *World) startCore() error {
	opts := core.Options{ConfigPath: w.Config, DataDir: w.Data, Logs: w.opts.Logs}
	if w.opts.Core != nil {
		w.opts.Core(&opts)
	}
	c := core.New(opts)
	if err := c.Start(context.Background()); err != nil {
		return err
	}
	w.Core = c
	h := c.Handler()
	w.handler.Store(&h)
	return nil
}

// Restart stops the core and starts a new one on the same config and data,
// at the same address: what a container restart does.
func (w *World) Restart() error {
	w.Core.Stop(context.Background())
	return w.startCore()
}

// Close stops everything and removes a temporary directory.
func (w *World) Close() {
	if w.srv != nil {
		w.srv.Close()
	}
	if w.Core != nil {
		w.Core.Stop(context.Background())
	}
	for _, s := range []interface{ Close() }{w.Feed, w.Mastodon, w.Pixelfed, w.Bluesky, w.Instagram, w.Threads, w.Push} {
		if s != nil && !isNil(s) {
			s.Close()
		}
	}
	for _, r := range w.restore {
		r()
	}
	if w.tempDir {
		os.RemoveAll(w.Dir)
	}
}

func isNil(v any) bool {
	switch s := v.(type) {
	case *Feed:
		return s == nil
	case *Fedi:
		return s == nil
	case *PDS:
		return s == nil
	case *Graph:
		return s == nil
	case *Push:
		return s == nil
	}
	return false
}

// Connections are the autoupload connections, one per network.
var Connections = []string{"photos-mastodon", "photos-pixelfed", "photos-bluesky", "photos-instagram", "photos-threads"}

func (w *World) config() string {
	return fmt.Sprintf(`core:
  domain: example.com
  adminKey: %s
  publicUrl: %s
  database: db.sqlite
  blobs: blob.fs
modules:
  db.sqlite: {}
  blob.fs: {}
  media: {}
  rss:
    instances:
      photos: { url: "%s/gallery.xml" }
      blog: { url: "%s/blog.xml" }
  mastodon:
    instances:
      main: { instance: %q, token: masto-token }
  pixelfed:
    instances:
      main: { instance: %q, token: pixel-token }
  bluesky:
    instances:
      main: { username: lna.example, password: app-password, service: %q }
  instagram:
    instances:
      main: { accountId: "17841", accessToken: ig-token }
  threads:
    instances:
      main: { userId: "5550", accessToken: th-token }
  autoupload:
    instances:
      photos-mastodon: { source: rss:photos, target: mastodon:main, media: media, cron: "0 0 18 * * *", caption: "More at https://example.com/gallery" }
      photos-pixelfed: { source: rss:photos, target: pixelfed:main, media: media, cron: "0 30 18 * * *" }
      photos-bluesky: { source: rss:photos, target: bluesky:main, media: media, cron: "0 0 19 * * *" }
      photos-instagram: { source: rss:photos, target: instagram:main, media: media, cron: "0 30 19 * * *" }
      photos-threads: { source: rss:photos, target: threads:main, media: media, cron: "0 0 20 * * *" }
      blog-mastodon: { source: rss:blog, target: mastodon:main, strategy: announce, delay: 1h }
  interactions:
    config:
      ipHashSalt: world-salt
      platforms: [mastodon:main, pixelfed:main, bluesky:main, instagram:main, threads:main]
  microblog:
    config:
      publishTo: [mastodon:main, bluesky:main, threads:main]
  webpush:
    config:
      subscriber: "mailto:admin@example.com"
  webmention: {}
  routing: {}
  trips:
    config:
      routing: routing
      media: media
`, AdminKey, w.URL, w.Feed.URL, w.Feed.URL, w.Mastodon.URL, w.Pixelfed.URL, w.Bluesky.URL)
}

// Do sends a request with the admin key; body is JSON unless it is
// url.Values (a form).
func (w *World) Do(method, path string, body any) (int, []byte, error) {
	return w.request(method, path, body, true)
}

// Public sends a request without the key.
func (w *World) Public(method, path string, body any) (int, []byte, error) {
	return w.request(method, path, body, false)
}

func (w *World) request(method, path string, body any, admin bool) (int, []byte, error) {
	var rd io.Reader
	ct := ""
	switch b := body.(type) {
	case nil:
	case url.Values:
		rd, ct = bytes.NewReader([]byte(b.Encode())), "application/x-www-form-urlencoded"
	default:
		j, err := json.Marshal(b)
		if err != nil {
			return 0, nil, err
		}
		rd, ct = bytes.NewReader(j), "application/json"
	}
	req, err := http.NewRequest(method, w.URL+path, rd)
	if err != nil {
		return 0, nil, err
	}
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	if admin {
		req.Header.Set("Authorization", "ApiKey "+AdminKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return resp.StatusCode, b, err
}

// RunJob runs a job and waits for it; key as on the Jobs page.
func (w *World) RunJob(key string) error { return w.Core.RunJob(key) }

// Deliver delivers the pending events now.
func (w *World) Deliver() { w.Core.DeliverNow() }

// Index fetches both feeds and indexes them for autoupload.
func (w *World) Index() error {
	for _, k := range []string{"rss:photos/refresh", "rss:blog/refresh", "autoupload/index"} {
		if err := w.RunJob(k); err != nil {
			return fmt.Errorf("%s: %w", k, err)
		}
	}
	w.Deliver()
	return nil
}

// Rotate runs a connection's rotation once; the error is the run's.
func (w *World) Rotate(connection string) error {
	err := w.RunJob("autoupload:" + connection + "/publish")
	w.Deliver()
	return err
}

// Likes collects the likes of every post from the platforms.
func (w *World) Likes() error {
	if c, ok := w.Core.Instance("interactions:default"); ok {
		c.(*interactions.Counter).NoPacing()
	}
	err := w.RunJob("interactions:default/sweep")
	w.Deliver()
	return err
}

// Seed fills the world the way months of use would: posts on every
// network, a content failure on Instagram, a transient one on Threads that
// succeeded later, a paused Bluesky connection (its password stopped
// working), likes, microblog posts, webmentions and push subscribers.
func (w *World) Seed() error {
	if ig, ok := w.Core.Instance("instagram:main"); ok {
		ig.(*instagram.Account).SetPollGap(time.Millisecond)
	}
	if th, ok := w.Core.Instance("threads:main"); ok {
		th.(*threads.Account).SetPollGap(time.Millisecond)
	}
	if err := w.Index(); err != nil {
		return err
	}
	runs := map[string]int{"photos-mastodon": 24, "photos-pixelfed": 12, "photos-bluesky": 8, "photos-instagram": 6, "photos-threads": 5}
	for _, c := range Connections {
		for i := 0; i < runs[c]; i++ {
			switch {
			case c == "photos-instagram" && i == 2:
				w.Instagram.FailNext(1, http.StatusBadRequest, `{"error":{"message":"The image is too large to upload. It should be less than 8 MB.","type":"OAuthException","code":36000}}`)
			case c == "photos-threads" && i == 1:
				w.Threads.FailNext(1, http.StatusServiceUnavailable, `{"error":{"message":"Service temporarily unavailable","code":2}}`)
			case c == "photos-bluesky" && i == runs[c]-1:
				w.Bluesky.Expire()
			}
			_ = w.Rotate(c) // failures are part of the seed
		}
	}
	w.Bluesky.Renew() // the connection stays paused until resumed by hand

	for _, text := range []string{"Back from the mountains, photos coming soon.", "New trip report is up!", "Foggy morning at the lake."} {
		if code, b, err := w.Do("POST", "/api/b/microblog/posts", map[string]string{"body": text}); err != nil || code != http.StatusCreated {
			return fmt.Errorf("microblog post: %d %s %v", code, b, err)
		}
	}
	w.Deliver()
	if err := w.Likes(); err != nil {
		return err
	}
	for i, target := range []string{"https://example.com/posts/trip-report-1/", "https://example.com/gallery/", "https://example.com/posts/trip-report-2/"} {
		form := url.Values{"source": {fmt.Sprintf("https://friend%d.example/notes/%d", i+1, i+1)}, "target": {target}}
		if code, _, err := w.Public("POST", "/api/webmention", form); err != nil || code != http.StatusAccepted {
			return fmt.Errorf("webmention: %d %v", code, err)
		}
	}
	for _, name := range []string{"laptop", "phone", "tablet"} {
		if err := w.Subscribe(name); err != nil {
			return err
		}
	}
	trip := map[string]any{"title": "Scandinavia"}
	if code, b, err := w.Do("POST", "/api/b/trips/trips", trip); err != nil || code != http.StatusCreated {
		return fmt.Errorf("trip: %d %s %v", code, b, err)
	}
	return nil
}

// Subscribe subscribes a browser of the website to push notifications,
// with real P-256 keys, so the push library can encrypt for it.
func (w *World) Subscribe(name string) error {
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	auth := make([]byte, 16)
	rand.Read(auth)
	sub := map[string]any{"endpoint": w.Push.Endpoint(name), "keys": map[string]string{
		"p256dh": base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), "auth": base64.RawURLEncoding.EncodeToString(auth)}}
	code, b, err := w.Public("POST", "/api/webpush/subscribe", sub)
	if err == nil && code != http.StatusOK {
		err = errors.New(string(b))
	}
	return err
}
