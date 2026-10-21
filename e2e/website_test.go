package e2e

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/core"
)

// The gate "the website does not notice" (docs/concepts/modular-architecture.md
// §6): the old companion's answers to every request the website makes are
// recorded once from testdata/legacy (scripts/record-golden.sh); the test
// migrates the same installation and asks the new companion.
var record = flag.String("record", "", "path of the old companion's binary: record testdata/golden from it")

// request is one call the website makes.
type request struct {
	name   string
	method string
	path   string
	header map[string]string
	form   url.Values
	json   string
}

const photo = "watzmann"

// adminKey is how scripts (bruno/) authenticate.
var adminKey = map[string]string{"Authorization": "ApiKey fixture-admin-key"}

var requests = []request{
	{name: "health", path: "/health"},
	{name: "likes-all", path: "/api/interactions/post/all/" + photo},
	{name: "likes-target", path: "/api/interactions/post/main-mastodon/" + photo},
	{name: "likes-other-photo", path: "/api/interactions/post/all/koenigssee"},
	{name: "likes-unknown", path: "/api/interactions/post/all/nope"},
	{name: "native-status", path: "/api/interactions/native/" + photo + "/status"},
	{name: "microblog-posts", path: "/api/microblog/posts"},
	{name: "microblog-post", path: "/api/microblog/posts/hello"},
	{name: "microblog-post-missing", path: "/api/microblog/posts/nope"},
	{name: "microblog-comments", path: "/api/microblog/posts/hello/comments"},
	{name: "microblog-likes", path: "/api/microblog/posts/hello/likes"},
	{name: "microblog-media", path: "/api/microblog/media/1f815a9799a61fb212f3996e292731b24f6199f43431b0b60e9e16be44e9976d.jpg"},
	{name: "trips", path: "/api/trips"},
	{name: "trip", path: "/api/trips/scandinavia"},
	{name: "trip-missing", path: "/api/trips/nope"},
	{name: "trip-media", path: "/api/trips/media/82c112ba48dc1e44cc1cf2eec6990dcd22c0e02d765b0478dcfea18d356ce53e.jpg"},
	{name: "vapidkey", path: "/api/webpush/vapidkey"},
	{name: "basemap-tilejson", path: "/api/tiles/basemap.json"},
	{name: "basemap-tile", path: "/api/tiles/basemap/20261005/1/1/1.mvt"},
	{name: "basemap-empty-tile", path: "/api/tiles/basemap/20261005/1/0/1.mvt"},
	{name: "basemap-unknown-version", path: "/api/tiles/basemap/20200101/1/1/1.mvt"},
	{name: "gbif-tile", path: "/api/tiles/gbif/5219243/1/0/0.png"},
	{name: "gbif-bad-address", path: "/api/tiles/gbif/abc/1/0/0.png"},
	{name: "cors-preflight", method: "OPTIONS", path: "/api/interactions/native/" + photo + "/like",
		header: map[string]string{"Origin": "https://www.example.com", "Access-Control-Request-Method": "POST"}},
	{name: "cors-foreign", path: "/api/trips", header: map[string]string{"Origin": "https://evil.example"}},
	{name: "admin-without-key", method: "POST", path: "/api/webpush/broadcast"},
	// Requests that change something come last.
	{name: "native-like", method: "POST", path: "/api/interactions/native/" + photo + "/like"},
	{name: "native-status-after-like", path: "/api/interactions/native/" + photo + "/status"},
	{name: "webmention", method: "POST", path: "/api/webmention",
		form: url.Values{"source": {"https://another.example/note"}, "target": {"https://example.com/posts/x/"}}},
	{name: "webmention-invalid", method: "POST", path: "/api/webmention",
		form: url.Values{"source": {"javascript:alert(1)"}, "target": {"https://example.com/"}}},
	// The ways in for scripts: the key in the header, a wrong one, none,
	// and ?token= on the client log ingest of the old admin UI.
	{name: "admin-wrong-key", method: "POST", path: "/api/webpush/broadcast", header: map[string]string{"Authorization": "ApiKey wrong"}, json: `{"title":"Hi","body":"there"}`},
	{name: "broadcast-with-key", method: "POST", path: "/api/webpush/broadcast", header: adminKey, json: `{"title":"Hi","body":"there"}`},
	{name: "interactions-fetch-with-key", method: "POST", path: "/api/interactions/fetch", header: adminKey},
	{name: "interactions-fetch-without-key", method: "POST", path: "/api/interactions/fetch"},
	{name: "backfill-with-key", method: "POST", path: "/api/backfill", header: adminKey},
	{name: "backfill-without-key", method: "POST", path: "/api/backfill"},
	{name: "upload-with-key", method: "POST", path: "/api/upload/mastodon", header: adminKey},
	{name: "upload-without-key", method: "POST", path: "/api/upload/mastodon"},
}

// accepted are the differences made on purpose, with the reason.
var accepted = map[string]struct {
	header, value, why string
}{
	"microblog-media": {"Cache-Control", "public, max-age=31536000, immutable", "media names are content hashes, so they never change"},
	"trip-media":      {"Cache-Control", "public, max-age=31536000, immutable", "media names are content hashes, so they never change"},
}

// The headers the website or a cache in front of it acts on.
var keptHeaders = []string{"Content-Type", "Cache-Control", "Content-Encoding", "Access-Control-Allow-Origin", "Access-Control-Allow-Credentials"}

// golden is one recorded answer.
type golden struct {
	Status int               `json:"status"`
	Header map[string]string `json:"header,omitempty"`
	JSON   any               `json:"json,omitempty"`
	Text   string            `json:"text,omitempty"`
	Base64 string            `json:"base64,omitempty"`
}

func TestWebsiteUnchanged(t *testing.T) {
	gbif := httptest.NewServer(http.FileServer(http.Dir("testdata/legacy/gbif")))
	defer gbif.Close()
	dir := t.TempDir()
	data := filepath.Join(dir, "data")
	if err := copyDir("testdata/legacy", data); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FIXTURE_BUCKET", filepath.Join(data, "bucket"))
	t.Setenv("FIXTURE_GBIF", gbif.URL+"/density")
	t.Setenv("TRUSTED_PROXIES", "")

	var base string
	if *record != "" {
		base = startOld(t, *record, dir)
	} else {
		base = startNew(t, dir, data)
	}
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, rq := range requests {
		got := do(t, client, base, rq)
		file := filepath.Join("testdata", "golden", rq.name+".json")
		if *record != "" {
			b, _ := json.MarshalIndent(got, "", "  ")
			os.MkdirAll(filepath.Dir(file), 0o755)
			if err := os.WriteFile(file, append(b, '\n'), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("%s: %v (record with scripts/record-golden.sh)", rq.name, err)
		}
		var want golden
		if err := json.Unmarshal(raw, &want); err != nil {
			t.Fatal(err)
		}
		// Through JSON once, so both sides have the same types.
		var norm golden
		b, _ := json.Marshal(got)
		json.Unmarshal(b, &norm)
		if a, ok := accepted[rq.name]; ok && norm.Header[a.header] == a.value && want.Header[a.header] == "" {
			delete(norm.Header, a.header)
		}
		if !reflect.DeepEqual(normalize(want), normalize(norm)) {
			w, _ := json.MarshalIndent(want, "", "  ")
			g, _ := json.MarshalIndent(norm, "", "  ")
			t.Errorf("%s %s: the old companion answered\n%s\nthe new one\n%s", method(rq), rq.path, w, g)
		}
	}
}

func method(rq request) string {
	if rq.method == "" {
		return "GET"
	}
	return rq.method
}

func do(t *testing.T, client *http.Client, base string, rq request) golden {
	t.Helper()
	var body io.Reader
	if rq.form != nil {
		body = strings.NewReader(rq.form.Encode())
	}
	if rq.json != "" {
		body = strings.NewReader(rq.json)
	}
	req, err := http.NewRequest(method(rq), base+rq.path, body)
	if err != nil {
		t.Fatal(err)
	}
	if rq.form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if rq.json != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range rq.header {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s: %v", rq.name, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	g := golden{Status: resp.StatusCode, Header: map[string]string{}}
	for _, h := range keptHeaders {
		if v := resp.Header.Get(h); v != "" {
			g.Header[h] = v
		}
	}
	var v any
	switch {
	case len(b) == 0:
	case json.Unmarshal(b, &v) == nil:
		g.JSON = v
	case isText(b):
		g.Text = string(b)
	default:
		g.Base64 = base64.StdEncoding.EncodeToString(b)
	}
	return g
}

func isText(b []byte) bool {
	for _, c := range b {
		if c < 0x09 || c > 0x7e && c < 0xa0 {
			return false
		}
	}
	return true
}

// normalize drops what differs by design between two runs: the random
// token of a website like.
func normalize(g golden) golden {
	if m, ok := g.JSON.(map[string]any); ok {
		if _, has := m["token"]; has {
			m["token"] = "<token>"
		}
	}
	return g
}

// startNew migrates the installation and starts the new companion on it.
func startNew(t *testing.T, dir, data string) string {
	t.Helper()
	cfg := filepath.Join(dir, "companion.yaml")
	var log bytes.Buffer
	err := core.Migrate(context.Background(), core.Options{ConfigPath: cfg, DataDir: data}, core.MigrateOptions{From: data, Out: &log})
	t.Logf("migrate:\n%s", log.String())
	if err != nil {
		t.Fatal(err)
	}
	c := core.New(core.Options{ConfigPath: cfg, DataDir: data, Logs: io.Discard})
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(c.Handler())
	t.Cleanup(func() {
		srv.Close()
		c.Stop(context.Background())
	})
	return srv.URL
}

// startOld runs the old companion on the installation; it listens on 8080.
func startOld(t *testing.T, binary, dir string) string {
	t.Helper()
	abs, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(abs)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
		t.Logf("old companion:\n%s", out.String())
	})
	base := "http://127.0.0.1:8080"
	deadline := time.Now().Add(30 * time.Second)
	for {
		resp, err := http.Get(base + "/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return base
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the old companion did not come up: %v\n%s", err, out.String())
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		info, _ := d.Info()
		return os.WriteFile(target, b, info.Mode().Perm())
	})
}
