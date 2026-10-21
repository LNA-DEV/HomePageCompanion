package core_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/LNA-DEV/HomePageCompanion/capability/social"
	"github.com/LNA-DEV/HomePageCompanion/companion"
	"github.com/LNA-DEV/HomePageCompanion/companiontest"
	"github.com/LNA-DEV/HomePageCompanion/core"
	"github.com/gin-gonic/gin"
)

// consumer is a test building block: one publisher slot, a list of them,
// a job, an event subscription and an alias.
type consumer struct {
	Greeting string                      `json:"greeting"`
	Target   social.Publisher            `companion:"slot=target"`
	TargetID string                      `companion:"slotid=target"`
	All      []social.Publisher          `companion:"slot=extra"`
	ByID     map[string]social.Publisher `companion:"slot=extra"`
	Token    string                      `json:"token" companion:"secret"`

	ctx      companion.Context
	mu       sync.Mutex
	events   []companion.Event
	failNext int
	ran      int
	stopped  bool
}

func (c *consumer) Provision(ctx companion.Context) error { c.ctx = ctx; return nil }

func (c *consumer) Validate() error {
	if c.Greeting == "invalid" {
		return errors.New("greeting is invalid")
	}
	return nil
}

func (c *consumer) Stop(context.Context) error { c.stopped = true; return nil }

func (c *consumer) Jobs() []companion.Job {
	return []companion.Job{
		{Name: "count", Run: func(context.Context) error { c.mu.Lock(); c.ran++; c.mu.Unlock(); return nil }},
		{Name: "panic", Run: func(context.Context) error { var m map[string]int; m["x"]++; return nil }},
	}
}

func (c *consumer) HandleEvent(_ context.Context, ev companion.Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failNext > 0 {
		c.failNext--
		return errors.New("not now")
	}
	c.events = append(c.events, ev)
	return nil
}

var moduleEvents []companion.Event

func init() {
	companion.Register(companion.Info{
		ID:  "t.consumer",
		New: func() companion.Instance { return new(consumer) },
		Slots: []companion.Slot{
			{Name: "target", Needs: social.PublisherV1},
			{Name: "extra", Needs: social.PublisherV1, Multiple: true, Optional: true},
		},
		Subscribes: []string{"t.ping/v1"},
		OnEvent: func(mc companion.ModuleContext, ev companion.Event) error {
			moduleEvents = append(moduleEvents, ev)
			return nil
		},
		Aliases: []string{"/api/legacy"},
		Routes: func(mc companion.ModuleContext, r *companion.Router) {
			r.Admin.GET("/hello", func(g *gin.Context) {
				var names []string
				for _, in := range mc.Instances() {
					names = append(names, in.Instance.(*consumer).Greeting)
				}
				g.String(http.StatusOK, strings.Join(names, ","))
			})
			r.Public.GET("/open", func(g *gin.Context) { g.String(http.StatusOK, "open") })
			r.Alias("/api/legacy").GET("/thing", func(g *gin.Context) { g.String(http.StatusOK, "legacy") })
			r.Alias("/api/legacy").POST("/admin-thing", r.RequireAdmin, func(g *gin.Context) { g.String(http.StatusOK, "ok") })
		},
	})
	companion.Register(companion.Info{ID: "t.clash", New: func() companion.Instance { return new(struct{}) },
		Aliases: []string{"/api/legacy/sub"}, Routes: func(companion.ModuleContext, *companion.Router) {}})
}

const baseConfig = `
modules:
  fake.publisher:
    instances:
      main: {}
      second: {}
  t.consumer:
    instances:
      a: { greeting: hello, target: fake.publisher:main, extra: [fake.publisher:main, fake.publisher:second], token: s3cret }
`

func TestStartsAndConnects(t *testing.T) {
	h := companiontest.New(t, baseConfig)
	c := companiontest.Instance[*consumer](h, "t.consumer:a")
	main := companiontest.Instance[*companiontest.FakePublisher](h, "fake.publisher:main")
	second := companiontest.Instance[*companiontest.FakePublisher](h, "fake.publisher:second")
	if c.Target != social.Publisher(main) || c.TargetID != "fake.publisher:main" {
		t.Fatalf("target slot: %v %q", c.Target, c.TargetID)
	}
	if len(c.All) != 2 || c.All[1] != social.Publisher(second) {
		t.Fatalf("multiple slot: %v", c.All)
	}
	if c.ByID["fake.publisher:second"] != social.Publisher(second) {
		t.Fatalf("map slot: %v", c.ByID)
	}
	if c.ctx.InstanceID() != "t.consumer:a" || c.ctx.ModuleID() != "t.consumer" {
		t.Fatalf("context: %s", c.ctx.InstanceID())
	}
	if w := h.Public("GET", "/health", nil); w.Code != 200 {
		t.Fatalf("health: %d", w.Code)
	}
}

func TestPlanErrors(t *testing.T) {
	for name, cfg := range map[string]string{
		"unknown module":   "modules:\n  nope: {}\n",
		"missing instance": "modules:\n  t.consumer:\n    instances:\n      a: { target: fake.publisher:main }\n",
		"wrong capability": "modules:\n  fake.source: { instances: { s: {} } }\n  t.consumer:\n    instances:\n      a: { target: fake.source:s }\n",
		"unconnected":      "modules:\n  t.consumer:\n    instances:\n      a: {}\n",
		"unknown field":    "modules:\n  fake.publisher: { instances: { main: { colour: red } } }\n",
		"invalid config":   "modules:\n  fake.publisher: { instances: { main: {} } }\n  t.consumer:\n    instances:\n      a: { greeting: invalid, target: fake.publisher:main }\n",
		"single slot, two": "modules:\n  fake.publisher: { instances: { main: {}, b: {} } }\n  t.consumer:\n    instances:\n      a: { target: [fake.publisher:main, fake.publisher:b] }\n",
		"aliases collide":  "modules:\n  fake.publisher: { instances: { main: {} } }\n  t.consumer: { instances: { a: { target: fake.publisher:main } } }\n  t.clash: {}\n",
		"instance name":    "modules:\n  fake.publisher: { instances: { Photo RSS: {} } }\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			p := dir + "/companion.yaml"
			full := strings.Replace(cfg, "modules:\n", "core: { database: test.sql, blobs: test.blob }\nmodules:\n  test.sql: {}\n  test.blob: {}\n", 1)
			if err := os.WriteFile(p, []byte(full), 0o600); err != nil {
				t.Fatal(err)
			}
			c := core.New(core.Options{ConfigPath: p, DataDir: dir + "/data", Logs: &bytes.Buffer{}})
			err := c.Start(context.Background())
			if err == nil {
				c.Stop(context.Background())
				t.Fatal("started")
			}
			t.Log(err)
			if name == "instance name" && !strings.Contains(err.Error(), `"photo-rss" would do`) {
				t.Errorf("no suggestion: %v", err)
			}
		})
	}
}

func TestAuth(t *testing.T) {
	h := companiontest.New(t, baseConfig)
	if w := h.Public("GET", "/api/b/t.consumer/hello", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous admin call: %d", w.Code)
	}
	if w := h.Admin("GET", "/api/b/t.consumer/hello", nil); w.Code != 200 || w.Body.String() != "hello" {
		t.Fatalf("admin call: %d %s", w.Code, w.Body)
	}
	if w := h.Public("GET", "/api/b/t.consumer/public/open", nil); w.Code != 200 {
		t.Fatalf("public call: %d", w.Code)
	}
	if w := h.Public("POST", "/api/core/login", []byte(`{"key":"wrong"}`)); w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong key: %d", w.Code)
	}
	w := h.Public("POST", "/api/core/login", []byte(`{"key":"`+companiontest.AdminKey+`"}`))
	if w.Code != 200 {
		t.Fatalf("login: %d", w.Code)
	}
	cookie := w.Result().Cookies()[0]
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie flags: %+v", cookie)
	}
	req := httptest.NewRequest("GET", "/api/b/t.consumer/hello", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.Core.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("cookie session: %d", rec.Code)
	}
	req = httptest.NewRequest("POST", "/api/core/logout", nil)
	req.AddCookie(cookie)
	h.Core.ServeHTTP(httptest.NewRecorder(), req)
	req = httptest.NewRequest("GET", "/api/b/t.consumer/hello", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	h.Core.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("after logout: %d", rec.Code)
	}
}

// The ways in of the pre-module companion keep working: the
// "Authorization: ApiKey <key>" header on every admin route, and ?token=<key>
// on the client log ingest only, under its old path.
func TestOldAuthWays(t *testing.T) {
	h := companiontest.New(t, baseConfig)
	key := "ApiKey " + companiontest.AdminKey
	call := func(method, path, auth string, body []byte) int {
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		h.Core.ServeHTTP(rec, req)
		return rec.Code
	}
	for _, path := range []string{"/api/core/overview", "/api/b/t.consumer/hello"} {
		if got := call("GET", path, key, nil); got != 200 {
			t.Errorf("%s with the key header: %d", path, got)
		}
		if got := call("GET", path, "ApiKey wrong", nil); got != http.StatusUnauthorized {
			t.Errorf("%s with a wrong key: %d", path, got)
		}
	}
	if got := call("POST", "/api/legacy/admin-thing", key, nil); got != 200 {
		t.Errorf("legacy admin alias with the key: %d", got)
	}
	logs := []byte(`{"clientId":"old-ui","entries":[{"level":"error","source":"console","message":"boom"}]}`)
	for _, c := range []struct {
		path, auth string
		want       int
	}{
		{"/api/admin/client-logs", key, http.StatusNoContent},
		{"/api/admin/client-logs?token=" + companiontest.AdminKey, "", http.StatusNoContent},
		{"/api/admin/client-logs?token=wrong", "", http.StatusUnauthorized},
		{"/api/admin/client-logs", "", http.StatusUnauthorized},
		{"/api/core/client-logs", key, http.StatusNoContent},
	} {
		if got := call("POST", c.path, c.auth, logs); got != c.want {
			t.Errorf("POST %s (auth %q): %d, want %d", c.path, c.auth, got, c.want)
		}
	}
	// As before, the token in the URL is for the log ingest only.
	if got := call("GET", "/api/core/overview?token="+companiontest.AdminKey, "", nil); got != http.StatusUnauthorized {
		t.Errorf("?token= on another route: %d", got)
	}
}

func TestAliases(t *testing.T) {
	h := companiontest.New(t, baseConfig)
	if w := h.Public("GET", "/api/legacy/thing", nil); w.Code != 200 || w.Body.String() != "legacy" {
		t.Fatalf("alias: %d %s", w.Code, w.Body)
	}
	if w := h.Public("POST", "/api/legacy/admin-thing", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("alias with admin check: %d", w.Code)
	}
	if w := h.Admin("POST", "/api/legacy/admin-thing", nil); w.Code != 200 {
		t.Fatalf("alias with admin key: %d", w.Code)
	}
}

func TestEventsAtLeastOnce(t *testing.T) {
	h := companiontest.New(t, baseConfig)
	c := companiontest.Instance[*consumer](h, "t.consumer:a")
	moduleEvents = nil
	c.failNext = 1
	if err := c.ctx.Emit(context.Background(), "t.ping/v1", map[string]string{"hello": "world"}); err != nil {
		t.Fatal(err)
	}
	h.Deliver()
	if len(c.events) != 0 || len(moduleEvents) != 1 {
		t.Fatalf("first delivery: instance %d, module %d", len(c.events), len(moduleEvents))
	}
	// The failed delivery comes back once its retry time has come.
	h.Core.DB().Exec("UPDATE core_deliveries SET next_at = '2000-01-01'")
	h.Deliver()
	if len(c.events) != 1 || len(moduleEvents) != 1 {
		t.Fatalf("retry: instance %d, module %d", len(c.events), len(moduleEvents))
	}
	var payload map[string]string
	if err := c.events[0].Decode(&payload); err != nil || payload["hello"] != "world" || c.events[0].Source != "t.consumer" {
		t.Fatalf("event: %+v %v", c.events[0], err)
	}
	// Nothing is delivered twice.
	h.Core.DB().Exec("UPDATE core_deliveries SET next_at = '2000-01-01'")
	h.Deliver()
	if len(c.events) != 1 {
		t.Fatalf("delivered twice: %d", len(c.events))
	}
}

func TestJobs(t *testing.T) {
	h := companiontest.New(t, baseConfig)
	c := companiontest.Instance[*consumer](h, "t.consumer:a")
	h.MustRunJob("t.consumer:a/count")
	h.MustRunJob("t.consumer:a/count")
	if c.ran != 2 {
		t.Fatalf("ran %d", c.ran)
	}
	err := h.RunJob("t.consumer:a/panic")
	if err == nil || !strings.Contains(err.Error(), "panic") {
		t.Fatalf("panic not turned into an error: %v", err)
	}
	w := h.Admin("GET", "/api/core/jobs", nil)
	if !strings.Contains(w.Body.String(), "t.consumer:a/count") {
		t.Fatalf("jobs list: %s", w.Body)
	}
	if w := h.Admin("POST", "/api/core/jobs/run", map[string]any{"key": "t.consumer:a/count", "wait": true}); w.Code != http.StatusAccepted || !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Fatalf("run now: %d %s", w.Code, w.Body)
	}
}

func TestSecrets(t *testing.T) {
	h := companiontest.New(t, baseConfig)
	c := companiontest.Instance[*consumer](h, "t.consumer:a")
	s := c.ctx.Secrets()
	if _, ok, err := s.Get("token"); ok || err != nil {
		t.Fatalf("empty store: %v %v", ok, err)
	}
	if err := s.Put("token", "very-secret-value"); err != nil {
		t.Fatal(err)
	}
	v, ok, err := s.Get("token")
	if err != nil || !ok || v != "very-secret-value" {
		t.Fatalf("get: %q %v %v", v, ok, err)
	}
	var stored []string
	h.Core.DB().Raw("SELECT value FROM core_secrets").Scan(&stored)
	if len(stored) != 1 || strings.Contains(stored[0], "very-secret") {
		t.Fatalf("stored in plain text: %v", stored)
	}
	// Another building block does not see it.
	if _, ok, _ := h.Module("fake.publisher").Secrets().Get("token"); ok {
		t.Fatal("secret visible to another building block")
	}
	if err := s.Delete("token"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Get("token"); ok {
		t.Fatal("deleted secret still there")
	}
}

func TestConfigShowsSecretsMasked(t *testing.T) {
	h := companiontest.New(t, baseConfig)
	w := h.Admin("GET", "/api/core/instances/t.consumer:a", nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), "s3cret") || !strings.Contains(w.Body.String(), "hello") {
		t.Fatalf("instance view: %d %s", w.Code, w.Body)
	}
}

func TestTempFiles(t *testing.T) {
	h := companiontest.New(t, baseConfig)
	mc := h.Module("t.consumer")
	url, err := mc.Temp().Put(context.Background(), []byte("image bytes"), "image/jpeg", 0)
	if err != nil {
		t.Fatal(err)
	}
	b, ct, err := companion.Fetch(context.Background(), nil, url)
	if err != nil || string(b) != "image bytes" || ct != "image/jpeg" {
		t.Fatalf("fetch in process: %q %q %v", b, ct, err)
	}
	path := strings.TrimPrefix(url, "http://companion.test")
	if w := h.Public("GET", path, nil); w.Code != 200 || w.Body.String() != "image bytes" {
		t.Fatalf("over HTTP: %d", w.Code)
	}
	if w := h.Public("GET", strings.Replace(path, "sig=", "sig=0", 1), nil); w.Code != 404 {
		t.Fatalf("bad signature: %d", w.Code)
	}
}

func TestReloadSwapsOnlyWhatChanged(t *testing.T) {
	h := companiontest.New(t, baseConfig+`  fake.source:
    instances:
      s: {}
`)
	oldConsumer := companiontest.Instance[*consumer](h, "t.consumer:a")
	oldMain := companiontest.Instance[*companiontest.FakePublisher](h, "fake.publisher:main")
	oldSource := companiontest.Instance[*companiontest.FakeSource](h, "fake.source:s")

	// Changing the publisher replaces it and the consumer connected to it,
	// not the unrelated source.
	err := h.Rewrite(strings.Replace(baseConfig, "main: {}", "main: { maxLength: 300 }", 1) + `  fake.source:
    instances:
      s: {}
`)
	if err != nil {
		t.Fatal(err)
	}
	newConsumer := companiontest.Instance[*consumer](h, "t.consumer:a")
	newMain := companiontest.Instance[*companiontest.FakePublisher](h, "fake.publisher:main")
	if newMain == oldMain || newMain.MaxLength != 300 {
		t.Fatal("publisher not replaced")
	}
	if newConsumer == oldConsumer || newConsumer.Target != social.Publisher(newMain) || !oldConsumer.stopped {
		t.Fatal("dependent consumer not replaced")
	}
	if companiontest.Instance[*companiontest.FakeSource](h, "fake.source:s") != oldSource {
		t.Fatal("unrelated instance replaced")
	}

	// A config that does not validate keeps the old instances.
	if err := h.Rewrite(strings.Replace(baseConfig, "greeting: hello", "greeting: invalid", 1)); err == nil {
		t.Fatal("invalid config accepted")
	}
	if companiontest.Instance[*consumer](h, "t.consumer:a") != newConsumer {
		t.Fatal("a failed reload replaced the instance")
	}
}

func TestSettingsEditKeepsCommentsAndSecrets(t *testing.T) {
	h := companiontest.New(t, baseConfig)
	raw, _ := os.ReadFile(h.Config)
	os.WriteFile(h.Config, append([]byte("# my comment\n"), raw...), 0o600)
	h.Core.Reload(context.Background())
	w := h.Admin("PUT", "/api/core/instances/t.consumer:a/config", map[string]any{"values": map[string]any{"greeting": "servus", "token": "••••••"}})
	if w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	out, _ := os.ReadFile(h.Config)
	if !strings.Contains(string(out), "# my comment") || !strings.Contains(string(out), "servus") || !strings.Contains(string(out), "s3cret") {
		t.Fatalf("saved config:\n%s", out)
	}
	if c := companiontest.Instance[*consumer](h, "t.consumer:a"); c.Greeting != "servus" || c.Token != "s3cret" {
		t.Fatalf("after reload: %+v", c)
	}
}

func TestBackupAndRestore(t *testing.T) {
	h := companiontest.New(t, baseConfig)
	db := h.Module("t.consumer").DB()
	type Note struct {
		ID   uint
		Text string
	}
	db.AutoMigrate(&Note{})
	db.Create(&Note{Text: "before"})
	h.Module("t.consumer").Blobs().Put(context.Background(), "file.txt", strings.NewReader("blob"), 4, "text/plain")

	var buf bytes.Buffer
	m, err := h.Core.Backup(context.Background(), &buf, core.BackupOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !m.Blobs || len(m.Areas) < 2 {
		t.Fatalf("manifest: %+v", m)
	}
	db.Model(&Note{}).Where("1 = 1").Update("text", "after")
	h.Module("t.consumer").Blobs().Delete(context.Background(), "file.txt")

	if err := h.Core.Restore(context.Background(), bytes.NewReader(buf.Bytes()), core.RestoreOptions{Module: "t.consumer"}); err != nil {
		t.Fatal(err)
	}
	// The area was replaced on disk; a new handle sees the old row.
	sql := companiontest.Instance[*companiontest.SQL](h, "test.sql:default")
	fresh, err := sql.Open("t.consumer")
	if err != nil {
		t.Fatal(err)
	}
	var n Note
	fresh.First(&n)
	if n.Text != "before" {
		t.Fatalf("restored row: %q", n.Text)
	}
	if _, err := h.Module("t.consumer").Blobs().Stat(context.Background(), "file.txt"); err != nil {
		t.Fatalf("restored blob: %v", err)
	}
}

func TestCORS(t *testing.T) {
	h := companiontest.New(t, baseConfig)
	for origin, want := range map[string]bool{
		"https://example.com":      true,
		"https://www.example.com":  true,
		"http://localhost:5173":    true,
		"https://evil-example.com": false,
	} {
		req := httptest.NewRequest("OPTIONS", "/api/legacy/thing", nil)
		req.Host = "companion.test"
		req.Header.Set("Origin", origin)
		req.Header.Set("Access-Control-Request-Method", "GET")
		w := httptest.NewRecorder()
		h.Core.ServeHTTP(w, req)
		got := w.Header().Get("Access-Control-Allow-Origin") == origin
		if got != want {
			t.Errorf("%s: allowed=%v", origin, got)
		}
	}
}

func TestTrustedProxies(t *testing.T) {
	for env, want := range map[string]int{"": 6, "none": 0, "10.1.2.3, 172.18.0.0/16": 2} {
		got, err := core.TrustedProxies(env)
		if err != nil || len(got) != want {
			t.Errorf("%q: %v %v", env, got, err)
		}
	}
	if _, err := core.TrustedProxies("not-an-ip"); err == nil {
		t.Error("a malformed entry is accepted")
	}
}

func TestMetrics(t *testing.T) {
	h := companiontest.New(t, baseConfig)
	// A fresh companion has no history yet: an empty list, not null, which
	// the dashboard could not iterate.
	if w := h.Admin("GET", "/api/core/metrics/history", nil); strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("fresh history %d %s", w.Code, w.Body.String())
	}
	h.Public("GET", "/api/legacy/thing", nil)
	h.MustRunJob("t.consumer:a/count")
	w := h.Admin("GET", "/metrics", nil)
	body := w.Body.String()
	for _, want := range []string{`companion_http_requests_total{block="t.consumer",code="2xx"}`, `companion_job_runs_total{job="t.consumer:a/count",result="ok"} 1`, `companion_instance_up{instance="t.consumer:a"} 1`} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics lack %s:\n%s", want, body)
		}
	}
	if w := h.Public("GET", "/metrics", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("metrics are public: %d", w.Code)
	}
}

func TestSchemaFlattensEmbeddedStructsAndSkipsUntagged(t *testing.T) {
	type inner struct {
		Caption string `json:"caption"`
	}
	type cfg struct {
		inner
		Cron     string `json:"cron"`
		Token    string `json:"token" companion:"secret"`
		Internal string
		Hidden   string `json:"-"`
	}
	fields := core.SchemaOf(companion.Info{ID: "schema-probe", New: func() companion.Instance { return new(cfg) }})
	var names []string
	for _, f := range fields {
		names = append(names, f.Name)
		if f.Name == "token" && !f.Secret {
			t.Error("token is not secret")
		}
	}
	if strings.Join(names, ",") != "caption,cron,token" {
		t.Fatalf("fields %v", names)
	}
}
