//go:build integration

package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/companion"
	"github.com/LNA-DEV/HomePageCompanion/companion/wire"
	"github.com/LNA-DEV/HomePageCompanion/core"
	"github.com/LNA-DEV/HomePageCompanion/e2e/world"
	"github.com/LNA-DEV/HomePageCompanion/service"
)

// The same module ships as a service (§5.12): the mastodon module runs
// through the service SDK, the core plans it from companion.lock, and
// autoupload posts through it as through the compiled-in one.
func TestModuleAsService(t *testing.T) {
	feed := world.NewFeed(world.Photos(5), nil)
	defer feed.Close()
	masto := world.NewFedi("masto-token")
	defer masto.Close()

	dir := t.TempDir()
	data := filepath.Join(dir, "data")
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	servicesAddr := ln.Addr().String()
	ln.Close()

	info, ok := companion.Lookup("mastodon")
	if !ok {
		t.Fatal("mastodon is not compiled in")
	}
	svc, err := service.New(service.Options{
		CoreURL:   "http://" + servicesAddr,
		TokenFile: filepath.Join(data, "services", "mastodon-svc", "run", "token"),
		DataDir:   t.TempDir(),
		Logs:      testLog{t},
	}, info)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close(context.Background())
	srv := httptest.NewServer(svc)
	defer srv.Close()

	lock := wire.Lock{Services: map[string]wire.LockedService{
		"mastodon-svc": {From: "path:modules/mastodon", Manifest: svc.Manifest(), Grants: wire.Grants{Volume: true}},
	}}
	raw, _ := json.Marshal(lock)
	os.WriteFile(filepath.Join(dir, wire.LockFile), raw, 0o600)
	cfg := fmt.Sprintf(`core:
  domain: example.com
  adminKey: k
  publicUrl: http://companion.test
modules:
  db.sqlite: {}
  blob.fs: {}
  media: {}
  rss:
    instances:
      photos: { url: "%s/gallery.xml" }
  autoupload:
    instances:
      photos: { source: rss:photos, target: mastodon-svc:main, media: media, cron: "0 0 18 * * *" }
services:
  mastodon-svc:
    url: %s
    instances:
      main: { instance: %q, token: masto-token }
`, feed.URL, srv.URL, masto.URL)
	os.WriteFile(filepath.Join(dir, "companion.yaml"), []byte(cfg), 0o600)
	t.Setenv("COMPANION_SECRET_KEY", "d29ybGQtc2VjcmV0LWtleS0zMi1ieXRlcy1sb25nISE=")
	c := core.New(core.Options{ConfigPath: filepath.Join(dir, "companion.yaml"), DataDir: data, Logs: testLog{t}, ServicesListen: servicesAddr})
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.Stop(context.Background())

	admin := func(method, path string) (int, string) {
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "ApiKey k")
		rec := httptest.NewRecorder()
		c.Handler().ServeHTTP(rec, req)
		b, _ := io.ReadAll(rec.Body)
		return rec.Code, string(b)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		var v core.InstanceView
		_, body := admin("GET", "/api/core/instances/mastodon-svc:main")
		json.Unmarshal([]byte(body), &v)
		if v.Running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("service not ready: %s", body)
		}
		time.Sleep(100 * time.Millisecond)
	}

	if err := c.RunJob("autoupload:photos/publish"); err != nil {
		t.Fatal(err)
	}
	posts := masto.Posts()
	if len(posts) != 1 || posts[0].Images != 1 {
		t.Fatalf("fake Mastodon got %+v", posts)
	}
	_, body := admin("GET", "/api/b/autoupload/items")
	if !strings.Contains(body, masto.URL) {
		t.Fatalf("the published post is not recorded with its URL: %s", body)
	}
	_, m := admin("GET", "/metrics")
	if !strings.Contains(m, `companion_capability_calls_total{capability="social.publisher/v1",instance="mastodon-svc:main",operation="publish",result="ok"} 1`) {
		t.Fatalf("publish through the service not measured:\n%s", m)
	}
}
