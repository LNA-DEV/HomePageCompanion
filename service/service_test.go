package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/capability/feed"
	"github.com/LNA-DEV/HomePageCompanion/capability/social"
	"github.com/LNA-DEV/HomePageCompanion/companion"
	"github.com/LNA-DEV/HomePageCompanion/companion/wire"
	"github.com/LNA-DEV/HomePageCompanion/service"
)

// poster has a slot, a config field, a secret, a job and an event handler.
type poster struct {
	Server   string      `json:"server" help:"the instance's server"`
	Password string      `json:"password" companion:"secret"`
	Cron     string      `json:"cron"`
	Source   feed.Source `companion:"slot=source"`

	mu      sync.Mutex
	stopped bool
	runs    int
	events  int
}

func (p *poster) Validate() error {
	if p.Server == "" {
		return errors.New("server is required")
	}
	return nil
}
func (p *poster) Stop(context.Context) error { p.stopped = true; return nil }
func (p *poster) Describe(context.Context) (social.Limits, error) {
	return social.Limits{Platform: p.Server}, nil
}
func (p *poster) Preview(context.Context, social.Post) (social.Rendered, error) {
	return social.Rendered{}, nil
}
func (p *poster) Publish(ctx context.Context, post social.Post) (social.Result, error) {
	items, err := p.Source.ListItems(ctx)
	if err != nil {
		return social.Result{}, err
	}
	return social.Result{ID: items[0].ID}, nil
}
func (p *poster) ResolveMention(context.Context, string) (social.Mention, error) {
	return social.Mention{}, social.ErrNotAProfile
}
func (p *poster) Jobs() []companion.Job {
	return []companion.Job{{Name: "publish", Schedule: p.Cron, Timeout: time.Minute, Run: func(context.Context) error {
		p.mu.Lock()
		p.runs++
		p.mu.Unlock()
		return nil
	}}}
}
func (p *poster) HandleEvent(context.Context, companion.Event) error {
	p.mu.Lock()
	p.events++
	p.mu.Unlock()
	return nil
}

var (
	built   []*poster
	builtMu sync.Mutex
)

var info = companion.Info{
	ID: "poster", Name: "Poster", Version: "1.2.3",
	New: func() companion.Instance {
		p := new(poster)
		builtMu.Lock()
		built = append(built, p)
		builtMu.Unlock()
		return p
	},
	Provides:   []companion.Capability{social.PublisherV1},
	Slots:      []companion.Slot{{Name: "source", Needs: feed.SourceV1}},
	Subscribes: []string{"feed.item.discovered/v1"},
}

const key = "the-token"

// fakeCore answers the slot calls a service makes.
func fakeCore(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+key || r.Header.Get(wire.InstanceHeader) != "main" || r.Header.Get(wire.TargetHeader) != "rss:photos" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/core/v1/slots/source/listItems" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode([]feed.Item{{ID: "photo-7", Kind: "photo"}})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newService(t *testing.T) (*service.Service, *httptest.Server) {
	t.Helper()
	core := fakeCore(t)
	s, err := service.New(service.Options{CoreURL: core.URL, Token: key, DataDir: t.TempDir(), Logs: io.Discard}, info)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close(context.Background()) })
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return s, srv
}

// call sends a signed request, as the core does.
func call(t *testing.T, srv *httptest.Server, path, instance, body string, out any) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if instance != "" {
		req.Header.Set(wire.InstanceHeader, instance)
	}
	wire.Sign(req, []byte(body), []byte(key), time.Now())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func TestManifest(t *testing.T) {
	s, _ := newService(t)
	m := s.Manifest()
	if m.ID != "poster" || m.Version != "1.2.3" || len(m.Provides) != 1 || m.Provides[0].Capability != "social.publisher/v1" {
		t.Fatalf("manifest %+v", m)
	}
	if strings.Join(m.Provides[0].Operations, ",") != "describe,preview,publish,resolveMention" {
		t.Fatalf("operations %v", m.Provides[0].Operations)
	}
	if len(m.Slots) != 1 || m.Slots[0].Needs != "feed.source/v1" || len(m.Jobs) != 1 || m.Jobs[0].Name != "publish" {
		t.Fatalf("slots %+v jobs %+v", m.Slots, m.Jobs)
	}
	var secret, help bool
	for _, f := range m.Config {
		secret = secret || (f.Name == "password" && f.Secret)
		help = help || (f.Name == "server" && f.Help != "")
	}
	if !secret || !help {
		t.Fatalf("config %+v", m.Config)
	}
	if err := m.Check(); err != nil {
		t.Fatal(err)
	}
}

func TestConfigureCallsJobsEvents(t *testing.T) {
	_, srv := newService(t)
	// Without the core's signature nothing but /health and /manifest.
	if resp, _ := http.Post(srv.URL+"/lifecycle/configure", "application/json", strings.NewReader("{}")); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unsigned configure: %d", resp.StatusCode)
	}
	var h struct{ ConfigVersion string }
	resp, _ := http.Get(srv.URL + "/health")
	json.NewDecoder(resp.Body).Decode(&h)
	if resp.StatusCode != 200 || h.ConfigVersion != "" {
		t.Fatalf("health before configure: %d %+v", resp.StatusCode, h)
	}

	cfg := `{"version":"v1","service":"poster","instances":{
		"main":{"config":{"server":"lemmy.world","cron":"0 0 18 * * *"},"slots":{"source":["rss:photos"]},"targets":{"rss:photos":["listItems","getItem"]}},
		"bad":{"config":{},"slots":{"source":["rss:photos"]}}}}`
	var res struct {
		Errors map[string]string
		Jobs   map[string][]wire.Job
	}
	if code := call(t, srv, "/lifecycle/configure", "", cfg, &res); code != 200 {
		t.Fatalf("configure: %d", code)
	}
	if !strings.Contains(res.Errors["bad"], "server is required") || len(res.Errors) != 1 {
		t.Fatalf("errors %+v", res.Errors)
	}
	if j := res.Jobs["main"]; len(j) != 1 || j[0].Schedule != "0 0 18 * * *" || j[0].Timeout != "1m0s" {
		t.Fatalf("jobs %+v", res.Jobs)
	}
	resp, _ = http.Get(srv.URL + "/health")
	json.NewDecoder(resp.Body).Decode(&h)
	if h.ConfigVersion != "v1" {
		t.Fatalf("health after configure: %+v", h)
	}

	// Publish reads the source through the core's slot API.
	var out social.Result
	if code := call(t, srv, "/capabilities/social.publisher/v1/publish", "main", `{"text":"x"}`, &out); code != 200 || out.ID != "photo-7" {
		t.Fatalf("publish: %d %+v", code, out)
	}
	if code := call(t, srv, "/capabilities/social.publisher/v1/publish", "bad", `{}`, nil); code == 200 {
		t.Fatal("an instance that failed its config answered")
	}
	if code := call(t, srv, "/jobs/publish", "main", "{}", nil); code != http.StatusNoContent {
		t.Fatalf("job: %d", code)
	}
	ev := `{"id":"e1","type":"feed.item.discovered/v1","data":{}}`
	call(t, srv, "/events", "main", ev, nil)
	call(t, srv, "/events", "main", ev, nil)
	builtMu.Lock()
	var main *poster
	for _, p := range built {
		if p.Server == "lemmy.world" {
			main = p
		}
	}
	builtMu.Unlock()
	if main.runs != 1 || main.events != 1 {
		t.Fatalf("job runs %d, events %d (want each once)", main.runs, main.events)
	}

	// The same configuration keeps the instance; a changed one replaces it.
	call(t, srv, "/lifecycle/configure", "", strings.Replace(cfg, `"v1"`, `"v2"`, 1), nil)
	if main.stopped {
		t.Fatal("unchanged instance replaced")
	}
	call(t, srv, "/lifecycle/configure", "", strings.Replace(cfg, "lemmy.world", "lemmy.ml", 1), nil)
	if !main.stopped {
		t.Fatal("changed instance kept")
	}
}
