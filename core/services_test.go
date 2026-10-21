package core_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/capability/notify"
	"github.com/LNA-DEV/HomePageCompanion/capability/social"
	"github.com/LNA-DEV/HomePageCompanion/companion"
	"github.com/LNA-DEV/HomePageCompanion/companion/wire"
	"github.com/LNA-DEV/HomePageCompanion/companiontest"
	"github.com/LNA-DEV/HomePageCompanion/core"
	"github.com/LNA-DEV/HomePageCompanion/service"
	"github.com/gin-gonic/gin"
)

// remote is a building block that runs as a service in these tests,
// through the Go service SDK: a publisher that notifies through its slot.
type remote struct {
	Platform string          `json:"platform"`
	Notify   notify.Notifier `companion:"slot=notify"`

	mu     sync.Mutex
	posted []social.Post
	synced int
	events []string
}

var (
	remotesMu sync.Mutex
	remotes   = map[string]*remote{}
)

func (r *remote) Provision(ctx companion.Context) error {
	if r.Platform == "" {
		return errors.New("platform is required")
	}
	remotesMu.Lock()
	remotes[ctx.InstanceName()] = r
	remotesMu.Unlock()
	return nil
}

func (r *remote) Describe(context.Context) (social.Limits, error) {
	return social.Limits{Platform: r.Platform, MaxLength: 500}, nil
}
func (r *remote) Preview(_ context.Context, p social.Post) (social.Rendered, error) {
	return social.Rendered{Text: p.Text}, nil
}
func (r *remote) Publish(ctx context.Context, p social.Post) (social.Result, error) {
	if p.Text == "slow down" {
		return social.Result{}, fmt.Errorf("remote: %w", social.ErrRateLimited)
	}
	if r.Notify != nil {
		if err := r.Notify.Broadcast(ctx, notify.Message{Title: "posted", Body: p.Text}); err != nil {
			return social.Result{}, err
		}
	}
	r.mu.Lock()
	r.posted = append(r.posted, p)
	r.mu.Unlock()
	return social.Result{ID: "r1", URL: "https://remote.example/post/r1"}, nil
}
func (r *remote) ResolveMention(context.Context, string) (social.Mention, error) {
	return social.Mention{}, social.ErrNotAProfile
}
func (r *remote) ListPosts(context.Context) ([]social.ListedPost, error) {
	return []social.ListedPost{{Ref: social.Result{ID: "old"}}}, nil
}
func (r *remote) Jobs() []companion.Job {
	return []companion.Job{{Name: "sync", Schedule: "0 0 3 * * *", Run: func(context.Context) error {
		r.mu.Lock()
		r.synced++
		r.mu.Unlock()
		return nil
	}}}
}
func (r *remote) HandleEvent(_ context.Context, ev companion.Event) error {
	r.mu.Lock()
	r.events = append(r.events, ev.Type)
	r.mu.Unlock()
	return nil
}

func remoteInstance(t *testing.T, name string) *remote {
	t.Helper()
	remotesMu.Lock()
	defer remotesMu.Unlock()
	r := remotes[name]
	if r == nil {
		t.Fatalf("no remote instance %s", name)
	}
	return r
}

var remoteInfo = companion.Info{
	ID:         "remote",
	Name:       "Remote",
	New:        func() companion.Instance { return new(remote) },
	Provides:   []companion.Capability{social.PublisherV1},
	Slots:      []companion.Slot{{Name: "notify", Needs: notify.NotifyV1, Optional: true}},
	Subscribes: []string{"t.ping/v1"},
	Routes: func(mc companion.ModuleContext, r *companion.Router) {
		r.Admin.GET("/hello", func(g *gin.Context) { g.String(http.StatusOK, "hello from %s%s", mc.ModuleID(), g.Query("uiToken")) })
		r.Public.GET("/open", func(g *gin.Context) { g.String(http.StatusOK, "open") })
	},
	UI: &companion.UI{
		Files: fstest.MapFS{"index.html": {Data: []byte("<p>remote ui</p>")}, "app.js": {Data: []byte("console.log(1)")}},
		Pages: []companion.Page{{Path: "", Title: "Remote"}},
	},
}

// swappable lets a test replace the service behind a URL (a restart).
type swappable struct {
	mu sync.Mutex
	h  http.Handler
}

func (s *swappable) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	h := s.h
	s.mu.Unlock()
	if h == nil {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}
	h.ServeHTTP(w, r)
}

func (s *swappable) set(h http.Handler) { s.mu.Lock(); s.h = h; s.mu.Unlock() }

type remoteSetup struct {
	h       *companiontest.Harness
	dataDir atomic.Pointer[string] // set once the core started
	svc     *service.Service
	srv     *httptest.Server
	handler *swappable
	addr    string
}

// startRemote runs the remote service and a core it is applied to, with
// the grants in the lock and the config.
func startRemote(t *testing.T, locked wire.Grants, config string) *remoteSetup {
	t.Helper()
	restore := core.SetServiceIntervals(200*time.Millisecond, 50*time.Millisecond)
	t.Cleanup(restore)
	rs := &remoteSetup{handler: &swappable{}, addr: companiontest.FreeAddr(t)}
	rs.srv = httptest.NewServer(rs.handler)
	t.Cleanup(rs.srv.Close)
	rs.svc = rs.newService(t)
	rs.handler.set(rs.svc)

	lock := wire.Lock{Services: map[string]wire.LockedService{"remote": {From: "path:test", Manifest: rs.svc.Manifest(), Grants: locked}}}
	raw, _ := json.Marshal(lock)
	config = strings.ReplaceAll(config, "SERVICE_URL", rs.srv.URL)
	rs.h = companiontest.NewWith(t, config, companiontest.Options{Files: map[string][]byte{wire.LockFile: raw}, ServicesListen: rs.addr})
	rs.dataDir.Store(&rs.h.Dir)
	return rs
}

func (rs *remoteSetup) newService(t *testing.T) *service.Service {
	t.Helper()
	// The core writes the token into its data directory, known once it
	// started.
	token := func() (string, error) {
		dir := rs.dataDir.Load()
		if dir == nil {
			return "", errors.New("core not started")
		}
		b, err := os.ReadFile(filepath.Join(*dir, "services", "remote", "run", "token"))
		return strings.TrimSpace(string(b)), err
	}
	s, err := service.New(service.Options{CoreURL: "http://" + rs.addr, TokenSource: token, DataDir: t.TempDir(), Logs: io.Discard}, remoteInfo)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close(context.Background()) })
	return s
}

// tokenFile is where the core put the service's token.
func (rs *remoteSetup) token(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(rs.h.Dir, "services", "remote", "run", "token"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

func waitRunning(t *testing.T, h *companiontest.Harness, id string, want bool) core.InstanceView {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		v := companiontest.JSON[core.InstanceView](h, h.Admin("GET", "/api/core/instances/"+id, nil))
		if v.Running == want || time.Now().After(deadline) {
			if v.Running != want {
				t.Fatalf("%s running=%v: %s", id, v.Running, v.Error)
			}
			return v
		}
		time.Sleep(20 * time.Millisecond)
	}
}

const remoteConfig = `
modules:
  fake.notifier: { instances: { main: {} } }
  t.consumer:
    instances:
      a: { greeting: hello, target: remote:main }
services:
  remote:
    url: SERVICE_URL
    grants: { events: [t.ping/v1], public: true }
    instances:
      main: { platform: lemmy, notify: fake.notifier:main }
`

var allGrants = wire.Grants{Events: []string{"t.ping/v1"}, Public: true, Volume: true}

// A service offers social.publisher/v1 and a module connects to it like to
// any module; the service calls a module through its own slot.
func TestServiceAsProviderAndCaller(t *testing.T) {
	rs := startRemote(t, allGrants, remoteConfig)
	h := rs.h
	v := waitRunning(t, h, "remote:main", true)
	if !v.Service || len(v.Provides) != 1 || v.Provides[0] != "social.publisher/v1" {
		t.Fatalf("view %+v", v)
	}
	c := companiontest.Instance[*consumer](h, "t.consumer:a")
	ctx := context.Background()

	lim, err := c.Target.Describe(ctx)
	if err != nil || lim.Platform != "lemmy" {
		t.Fatalf("describe through the service: %+v %v", lim, err)
	}
	res, err := c.Target.Publish(ctx, social.Post{Text: "hello lemmy", Images: []social.Image{{URL: "http://companion.test/a.jpg"}}})
	if err != nil || res.ID != "r1" {
		t.Fatalf("publish: %+v %v", res, err)
	}
	if got := remoteInstance(t, "main").posted; len(got) != 1 || got[0].Images[0].URL != "http://companion.test/a.jpg" {
		t.Fatalf("service got %+v", got)
	}
	// The service's own slot reached the notifier module through the core.
	if msgs := companiontest.Instance[*companiontest.FakeNotifier](h, "fake.notifier:main").Messages(); len(msgs) != 1 || msgs[0].Body != "hello lemmy" {
		t.Fatalf("notifier got %+v", msgs)
	}
	if _, err := c.Target.Publish(ctx, social.Post{Text: "slow down"}); !errors.Is(err, social.ErrRateLimited) {
		t.Fatalf("sentinel lost on the way: %v", err)
	}
	if _, ok := c.Target.(social.Lister); !ok {
		t.Fatal("the service lists, the adapter does not")
	}
	if _, ok := c.Target.(social.Deleter); ok {
		t.Fatal("the service does not delete, the adapter does")
	}

	// Its API: admin only under /api/b/remote/, public under …/public/.
	if w := h.Admin("GET", "/api/b/remote/hello", nil); w.Code != 200 || w.Body.String() != "hello from remote" {
		t.Fatalf("admin API: %d %q", w.Code, w.Body.String())
	}
	if w := h.Public("GET", "/api/b/remote/hello", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("admin API without the key: %d", w.Code)
	}
	if w := h.Public("GET", "/api/b/remote/public/open", nil); w.Code != 200 || w.Body.String() != "open" {
		t.Fatalf("public API: %d %q", w.Code, w.Body.String())
	}

	// Its job, by hand, and an event it subscribes to and was granted,
	// delivered once although the bus may send it twice.
	h.MustRunJob("remote:main/sync")
	if remoteInstance(t, "main").synced != 1 {
		t.Fatal("job did not run in the service")
	}
	if err := c.ctx.Emit(ctx, "t.ping/v1", map[string]string{"x": "y"}); err != nil {
		t.Fatal(err)
	}
	h.Deliver()
	r := remoteInstance(t, "main")
	r.mu.Lock()
	events := append([]string(nil), r.events...)
	r.mu.Unlock()
	if len(events) != 1 || events[0] != "t.ping/v1" {
		t.Fatalf("events %v", events)
	}

	// /metrics counts the calls per capability and instance, and the
	// service's state.
	m := h.Admin("GET", "/metrics", nil).Body.String()
	for _, want := range []string{
		`companion_capability_calls_total{capability="social.publisher/v1",instance="remote:main",operation="publish",result="ok"} 1`,
		`companion_capability_calls_total{capability="notify/v1",instance="fake.notifier:main",operation="broadcast",result="ok"} 1`,
		`companion_service_up{service="remote"} 1`,
	} {
		if !strings.Contains(m, want) {
			t.Errorf("/metrics lacks %s", want)
		}
	}
}

// Nobody but the core talks to a service, and only a service's own token
// opens the core's API for it, for the slots it was connected to.
func TestServiceAuthentication(t *testing.T) {
	rs := startRemote(t, allGrants, remoteConfig)
	waitRunning(t, rs.h, "remote:main", true)

	// Unsigned or wrongly signed requests to the service are refused.
	resp, err := http.Post(rs.srv.URL+"/capabilities/social.publisher/v1/describe", "application/json", strings.NewReader("{}"))
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unsigned call: %v %v", resp.StatusCode, err)
	}
	req, _ := http.NewRequest("POST", rs.srv.URL+"/capabilities/social.publisher/v1/describe", strings.NewReader("{}"))
	req.Header.Set(wire.InstanceHeader, "main")
	wire.Sign(req, []byte("{}"), []byte("not the key"), time.Now())
	if resp, _ := http.DefaultClient.Do(req); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("forged signature: %d", resp.StatusCode)
	}

	call := func(token, instance, target, path string) int {
		req, _ := http.NewRequest("POST", "http://"+rs.addr+path, strings.NewReader(`{"message":{"title":"x","body":"y"}}`))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		req.Header.Set(wire.InstanceHeader, instance)
		req.Header.Set(wire.TargetHeader, target)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	tok := rs.token(t)
	if got := call("", "main", "", "/core/v1/slots/notify/broadcast"); got != http.StatusUnauthorized {
		t.Fatalf("no token: %d", got)
	}
	if got := call("wrong", "main", "", "/core/v1/slots/notify/broadcast"); got != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d", got)
	}
	if got := call(tok, "main", "", "/core/v1/slots/notify/broadcast"); got != http.StatusOK {
		t.Fatalf("own slot: %d", got)
	}
	// Not connected: another instance, a slot it does not have.
	if got := call(tok, "main", "t.consumer:a", "/core/v1/slots/notify/broadcast"); got != http.StatusForbidden {
		t.Fatalf("unconnected target: %d", got)
	}
	if got := call(tok, "main", "", "/core/v1/slots/database/open"); got != http.StatusNotFound {
		t.Fatalf("unknown slot: %d", got)
	}
	if got := call(tok, "nobody", "", "/core/v1/slots/notify/broadcast"); got != http.StatusBadRequest {
		t.Fatalf("unknown instance: %d", got)
	}
	// Its own secrets, apart from any module's.
	secret := func(method, body string) (int, string) {
		req, _ := http.NewRequest(method, "http://"+rs.addr+"/core/v1/secrets/oauth:main", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, _ := secret("GET", ""); code != http.StatusNotFound {
		t.Fatalf("missing secret: %d", code)
	}
	if code, _ := secret("PUT", `{"value":"tok-123"}`); code != http.StatusNoContent {
		t.Fatalf("put secret: %d", code)
	}
	if code, body := secret("GET", ""); code != 200 || !strings.Contains(body, "tok-123") {
		t.Fatalf("get secret: %d %s", code, body)
	}
	if _, ok, _ := rs.h.Module("remote").Secrets().Get("oauth:main"); ok {
		t.Fatal("a service's secret is readable as a module's")
	}
	if code, _ := secret("DELETE", ""); code != http.StatusNoContent {
		t.Fatalf("delete secret: %d", code)
	}

	// The core's admin API does not listen there.
	if resp, _ := http.Get("http://" + rs.addr + "/api/core/instances"); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("admin API on the services listener: %d", resp.StatusCode)
	}
}

// Only what the lock confirmed counts: a grant added to companion.yaml
// without hpc apply has no effect.
func TestUnconfirmedGrantsDoNotCount(t *testing.T) {
	rs := startRemote(t, wire.Grants{Volume: true}, remoteConfig)
	h := rs.h
	waitRunning(t, h, "remote:main", true)
	if w := h.Public("GET", "/api/b/remote/public/open", nil); w.Code != http.StatusNotFound {
		t.Fatalf("public routes without a confirmed grant: %d", w.Code)
	}
	c := companiontest.Instance[*consumer](h, "t.consumer:a")
	c.ctx.Emit(context.Background(), "t.ping/v1", nil)
	h.Deliver()
	r := remoteInstance(t, "main")
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.events) != 0 {
		t.Fatalf("event delivered without a confirmed grant: %v", r.events)
	}
}

// A service that is down fails its calls as transient errors; once it
// answers it is configured, and again after a restart.
func TestServiceDownAndRestarted(t *testing.T) {
	rs := startRemote(t, allGrants, remoteConfig)
	h := rs.h
	waitRunning(t, h, "remote:main", true)
	c := companiontest.Instance[*consumer](h, "t.consumer:a")

	rs.handler.set(nil)
	waitRunning(t, h, "remote:main", false)
	_, err := c.Target.Publish(context.Background(), social.Post{Text: "x"})
	if err == nil || social.Classify(err).Cause != social.CauseTransient {
		t.Fatalf("down: %v (%+v)", err, social.Classify(err))
	}

	// It comes back as a fresh process without its configuration.
	fresh := rs.newService(t)
	rs.handler.set(fresh)
	waitRunning(t, h, "remote:main", true)
	if _, err := c.Target.Publish(context.Background(), social.Post{Text: "after the restart"}); err != nil {
		t.Fatalf("after the restart: %v", err)
	}
	m := h.Admin("GET", "/metrics", nil).Body.String()
	if !strings.Contains(m, `companion_service_restarts{service="remote"} 1`) {
		t.Error("restart not counted")
	}
}

// The service checks each instance's config; one it refuses fails alone.
func TestServiceRefusesAnInstanceConfig(t *testing.T) {
	rs := startRemote(t, allGrants, remoteConfig+"      broken: {}\n")
	h := rs.h
	waitRunning(t, h, "remote:main", true)
	v := waitRunning(t, h, "remote:broken", false)
	if !strings.Contains(v.Error, "platform is required") {
		t.Fatalf("error %q", v.Error)
	}
}

func TestServiceNotApplied(t *testing.T) {
	companiontest.RegisterStorage()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "companion.yaml")
	os.WriteFile(cfg, []byte("modules:\n  "+companiontest.SQLID+": {}\ncore: { database: "+companiontest.SQLID+" }\nservices:\n  lemmy: { from: oci://ghcr.io/x/lemmy:1, instances: { main: {} } }\n"), 0o600)
	t.Setenv("COMPANION_SECRET_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	c := core.New(core.Options{ConfigPath: cfg, DataDir: filepath.Join(dir, "data"), Logs: io.Discard})
	err := c.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "lemmy:main: service lemmy is not applied yet: run hpc apply") {
		t.Fatalf("start without a lock: %v", err)
	}
}

// hpc apply records a new manifest: the reload picks it up.
func TestReloadAfterApply(t *testing.T) {
	rs := startRemote(t, allGrants, remoteConfig)
	h := rs.h
	waitRunning(t, h, "remote:main", true)
	lockPath := filepath.Join(filepath.Dir(h.Config), wire.LockFile)
	m := rs.svc.Manifest()
	m.Version = "2.0.0"
	l := wire.Lock{Services: map[string]wire.LockedService{"remote": {From: "path:test", Manifest: m, Grants: allGrants}}}
	if err := l.Write(lockPath); err != nil {
		t.Fatal(err)
	}
	if err := h.Core.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitRunning(t, h, "remote:main", true)
	var mods struct {
		Modules []core.UIModule `json:"modules"`
	}
	json.Unmarshal(h.Admin("GET", "/api/core/ui", nil).Body.Bytes(), &mods)
	found := false
	for _, mod := range mods.Modules {
		if mod.ID == "remote" {
			found = mod.Service
		}
	}
	if !found {
		t.Fatalf("remote not listed as a service: %+v", mods.Modules)
	}
	c := companiontest.Instance[*consumer](h, "t.consumer:a")
	if _, err := c.Target.Describe(context.Background()); err != nil {
		t.Fatalf("after the reload: %v", err)
	}
}

// A backup carries the service's volume, the service paused meanwhile; a
// restore puts it back.
func TestServiceVolumeBackup(t *testing.T) {
	rs := startRemote(t, allGrants, remoteConfig)
	h := rs.h
	waitRunning(t, h, "remote:main", true)
	vol := filepath.Join(h.Dir, "services", "remote", "volume")
	os.MkdirAll(filepath.Join(vol, "db"), 0o755)
	os.WriteFile(filepath.Join(vol, "db", "state.json"), []byte(`{"n":1}`), 0o644)

	var buf bytes.Buffer
	m, err := h.Core.Backup(context.Background(), &buf, core.BackupOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Services) != 1 || m.Services[0] != "remote" {
		t.Fatalf("services in the backup: %v", m.Services)
	}
	os.WriteFile(filepath.Join(vol, "db", "state.json"), []byte(`{"n":2}`), 0o644)
	if err := h.Core.Restore(context.Background(), bytes.NewReader(buf.Bytes()), core.RestoreOptions{}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(vol, "db", "state.json")); string(b) != `{"n":1}` {
		t.Fatalf("restored volume: %s", b)
	}
}

func TestEgressGrant(t *testing.T) {
	grant := []string{"lemmy.world", "*.example.com", "api.other.org:8443", "10.0.0.5"}
	for _, c := range []struct {
		host, port string
		want       bool
	}{
		{"lemmy.world", "443", true},
		{"lemmy.world", "80", true},
		{"lemmy.world", "22", false},
		{"evil-lemmy.world", "443", false},
		{"img.example.com", "443", true},
		{"example.com", "443", false},
		{"api.other.org", "8443", true},
		{"api.other.org", "443", false},
		{"10.0.0.5", "80", true},
		{"10.0.0.6", "80", false},
	} {
		if got := core.EgressGranted(grant, c.host, c.port); got != c.want {
			t.Errorf("%s:%s granted=%v, want %v", c.host, c.port, got, c.want)
		}
	}
}

// The egress proxy lets a service reach the hosts it was granted, and no
// others; HTTPS goes through CONNECT.
func TestEgressProxy(t *testing.T) {
	t.Cleanup(core.AllowLoopbackEgress())
	allowed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "allowed "+r.URL.Path) }))
	defer allowed.Close()
	tlsAllowed := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "tls") }))
	defer tlsAllowed.Close()
	denied := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("the denied host was reached") }))
	defer denied.Close()
	hostOf := func(u string) string { return strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://") }

	grants := wire.Grants{Egress: []string{hostOf(allowed.URL), hostOf(tlsAllowed.URL), "169.254.169.254"}, Volume: true}
	cfg := strings.Replace(remoteConfig, "grants: { events: [t.ping/v1], public: true }",
		fmt.Sprintf("grants: { egress: [%q, %q, 169.254.169.254] }", hostOf(allowed.URL), hostOf(tlsAllowed.URL)), 1)
	rs := startRemote(t, grants, cfg)
	waitRunning(t, rs.h, "remote:main", true)

	client := func(user, pass string) *http.Client {
		pu, _ := url.Parse("http://" + rs.addr)
		if user != "" {
			pu.User = url.UserPassword(user, pass)
		}
		tr := tlsAllowed.Client().Transport.(*http.Transport).Clone()
		tr.Proxy = http.ProxyURL(pu)
		return &http.Client{Transport: tr, Timeout: 10 * time.Second}
	}
	svc := client("remote", rs.token(t))
	get := func(c *http.Client, u string) (int, string) {
		resp, err := c.Get(u)
		if err != nil {
			return 0, err.Error()
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, body := get(svc, allowed.URL+"/x"); code != 200 || body != "allowed /x" {
		t.Fatalf("granted host: %d %q", code, body)
	}
	if code, body := get(svc, tlsAllowed.URL); code != 200 || body != "tls" {
		t.Fatalf("granted host over CONNECT: %d %q", code, body)
	}
	if code, body := get(svc, denied.URL); code != http.StatusForbidden || !strings.Contains(body, "not granted") {
		t.Fatalf("host not granted: %d %q", code, body)
	}
	// The cloud's metadata service, even when granted by mistake.
	if code, _ := get(svc, "http://169.254.169.254/latest/meta-data/"); code != http.StatusBadGateway {
		t.Fatalf("link-local address: %d", code)
	}
	// Anyone else: no service, no way out.
	for name, c := range map[string]*http.Client{"anonymous": client("", ""), "wrong token": client("remote", "nope"), "other name": client("t.consumer", rs.token(t))} {
		if code, _ := get(c, allowed.URL); code != http.StatusProxyAuthRequired {
			t.Errorf("%s: %d", name, code)
		}
	}
	m := rs.h.Admin("GET", "/metrics", nil).Body.String()
	if !strings.Contains(m, `companion_egress_requests_total{host="127.0.0.1",result="denied",service="remote"} 1`) {
		t.Errorf("denied egress not counted:\n%s", m)
	}
}

// A service that restarted between two health checks answers that it does
// not know the instance; the core configures it again at once instead of
// waiting for the next check.
func TestServiceLostItsConfiguration(t *testing.T) {
	rs := startRemote(t, allGrants, remoteConfig)
	h := rs.h
	waitRunning(t, h, "remote:main", true)
	t.Cleanup(core.SetServiceIntervals(time.Hour, time.Hour))
	time.Sleep(300 * time.Millisecond) // the running check picks up the long interval
	c := companiontest.Instance[*consumer](h, "t.consumer:a")

	rs.handler.set(rs.newService(t))
	_, err := c.Target.Publish(context.Background(), social.Post{Text: "x"})
	if err == nil || !strings.Contains(err.Error(), "lost its configuration") || social.Classify(err).Cause != social.CauseTransient {
		t.Fatalf("first call after the restart: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := c.Target.Publish(context.Background(), social.Post{Text: "y"}); err == nil {
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("not configured again: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A service's UI comes through a token valid only for it, sandboxed by the
// CSP even outside the shell's frame.
func TestServiceUI(t *testing.T) {
	rs := startRemote(t, allGrants, remoteConfig)
	h := rs.h
	waitRunning(t, h, "remote:main", true)

	if w := h.Public("POST", "/api/core/services/remote/ui-token", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("a token without the admin: %d", w.Code)
	}
	if w := h.Admin("POST", "/api/core/services/t.consumer/ui-token", nil); w.Code != http.StatusNotFound {
		t.Fatalf("a token for a module: %d", w.Code)
	}
	tok := companiontest.JSON[struct {
		Token string `json:"token"`
		Base  string `json:"base"`
	}](h, h.Admin("POST", "/api/core/services/remote/ui-token", nil))
	if tok.Base != "/ui/s/"+tok.Token+"/" {
		t.Fatalf("token %+v", tok)
	}

	w := h.Public("GET", tok.Base+"index.html", nil)
	if w.Code != 200 || w.Body.String() != "<p>remote ui</p>" {
		t.Fatalf("the UI: %d %q", w.Code, w.Body.String())
	}
	if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "sandbox allow-scripts allow-forms") {
		t.Fatalf("CSP %q", csp)
	}
	if w.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatal("the token could leak in a Referer")
	}
	if w := h.Public("GET", tok.Base+"app.js", nil); w.Code != 200 || w.Body.String() != "console.log(1)" {
		t.Fatalf("a UI file: %d", w.Code)
	}
	for name, path := range map[string]string{
		"forged":  "/ui/s/" + strings.Replace(tok.Token, ".", ".9", 1) + "/index.html",
		"expired": "/ui/s/" + core.UIToken(h.Core, "remote", time.Now().Add(-2*time.Hour)) + "/index.html",
	} {
		if w := h.Public("GET", path, nil); w.Code != http.StatusUnauthorized {
			t.Errorf("%s token: %d", name, w.Code)
		}
	}
	if w := h.Admin("GET", "/ui/b/remote/index.html", nil); w.Code != http.StatusNotFound {
		t.Fatalf("a service's UI without a token: %d", w.Code)
	}

	// The token opens the service's API and nothing else.
	bearer := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer "+tok.Token)
		rec := httptest.NewRecorder()
		h.Core.ServeHTTP(rec, req)
		return rec
	}
	if w := bearer("/api/b/remote/hello"); w.Code != 200 || w.Body.String() != "hello from remote" {
		t.Fatalf("the service's API with its UI token: %d %q", w.Code, w.Body.String())
	}
	for _, p := range []string{"/api/core/instances", "/api/b/t.consumer/hello", "/metrics"} {
		if w := bearer(p); w.Code != http.StatusUnauthorized {
			t.Errorf("%s with a service's UI token: %d", p, w.Code)
		}
	}
	// In the query (an <img> of the UI), taken out before the service.
	if w := h.Public("GET", "/api/b/remote/hello?uiToken="+tok.Token, nil); w.Code != 200 || w.Body.String() != "hello from remote" {
		t.Fatalf("?uiToken=: %d %q", w.Code, w.Body.String())
	}
}
