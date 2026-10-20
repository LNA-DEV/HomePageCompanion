// Package companiontest runs a building block against a test core: a real
// core with its own SQLite area per module in a temp directory, an
// in-memory blob store, fakes in the slots, events delivered on demand and
// jobs run by hand.
//
//	h := companiontest.New(t, `
//	modules:
//	  fake.publisher: { instances: { main: {} } }
//	  autoupload:
//	    instances:
//	      photos: { source: fake.source:photos, target: fake.publisher:main }
//	`)
//	h.RunJob("autoupload:photos/publish")
package companiontest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LNA-DEV/HomePageCompanion/companion"
	"github.com/LNA-DEV/HomePageCompanion/core"
	"gopkg.in/yaml.v3"
)

// AdminKey is the test core's admin key.
const AdminKey = "test-admin-key"

// Harness is a running test core.
type Harness struct {
	T       *testing.T
	Core    *core.Core
	Dir     string // the data directory
	Config  string // companion.yaml's path
	handler http.Handler
}

// New starts a core with the given companion.yaml (core.database and
// core.blobs default to the in-memory test storage). It stops with the test.
func New(t *testing.T, config string) *Harness {
	t.Helper()
	registerStorage()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "companion.yaml")
	if err := os.WriteFile(cfgPath, []byte(withTestStorage(t, config)), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COMPANION_SECRET_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	t.Setenv("TRUSTED_PROXIES", "")
	c := core.New(core.Options{ConfigPath: cfgPath, DataDir: filepath.Join(dir, "data"), Logs: testWriter{t}})
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("core: %v", err)
	}
	t.Cleanup(func() { c.Stop(context.Background()) })
	return &Harness{T: t, Core: c, Dir: filepath.Join(dir, "data"), Config: cfgPath, handler: c.Handler()}
}

// withTestStorage adds the test storage unless the config names its own.
func withTestStorage(t *testing.T, config string) string {
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(config), &doc); err != nil {
		t.Fatalf("companion.yaml: %v", err)
	}
	if doc == nil {
		doc = map[string]any{}
	}
	coreBlock, _ := doc["core"].(map[string]any)
	if coreBlock == nil {
		coreBlock = map[string]any{}
	}
	if _, ok := coreBlock["adminKey"]; !ok {
		coreBlock["adminKey"] = AdminKey
	}
	if _, ok := coreBlock["publicUrl"]; !ok {
		coreBlock["publicUrl"] = "http://companion.test"
	}
	if _, ok := coreBlock["domain"]; !ok {
		coreBlock["domain"] = "example.com"
	}
	modules, _ := doc["modules"].(map[string]any)
	if modules == nil {
		modules = map[string]any{}
	}
	if _, ok := coreBlock["database"]; !ok {
		coreBlock["database"] = SQLID
		modules[SQLID] = map[string]any{}
	}
	if _, ok := coreBlock["blobs"]; !ok {
		coreBlock["blobs"] = BlobID
		modules[BlobID] = map[string]any{}
	}
	doc["core"], doc["modules"] = coreBlock, modules
	out, err := yaml.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Helper()
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

// Instance returns a running instance, failing the test if there is none.
func Instance[T any](h *Harness, id string) T {
	h.T.Helper()
	v, ok := h.Core.Instance(id)
	if !ok {
		h.T.Fatalf("no running instance %s", id)
	}
	t, ok := v.(T)
	if !ok {
		h.T.Fatalf("%s is %T", id, v)
	}
	return t
}

// Request sends a request to the gateway. With admin, it carries the admin
// key. body is JSON-encoded unless it is a string, []byte or io.Reader.
func (h *Harness) Request(method, path string, body any, admin bool) *httptest.ResponseRecorder {
	h.T.Helper()
	var r io.Reader
	ct := "application/json"
	switch b := body.(type) {
	case nil:
	case string:
		r = strings.NewReader(b)
		ct = "application/x-www-form-urlencoded"
	case []byte:
		r = bytes.NewReader(b)
	case io.Reader:
		r = b
	default:
		j, err := json.Marshal(b)
		if err != nil {
			h.T.Fatal(err)
		}
		r = bytes.NewReader(j)
	}
	req := httptest.NewRequest(method, path, r)
	if body != nil {
		req.Header.Set("Content-Type", ct)
	}
	if admin {
		req.Header.Set("Authorization", "ApiKey "+AdminKey)
	}
	w := httptest.NewRecorder()
	h.handler.ServeHTTP(w, req)
	return w
}

// Admin sends an admin request.
func (h *Harness) Admin(method, path string, body any) *httptest.ResponseRecorder {
	h.T.Helper()
	return h.Request(method, path, body, true)
}

// Public sends an anonymous request.
func (h *Harness) Public(method, path string, body any) *httptest.ResponseRecorder {
	h.T.Helper()
	return h.Request(method, path, body, false)
}

// JSON decodes a response body, failing the test on error.
func JSON[T any](h *Harness, w *httptest.ResponseRecorder) T {
	h.T.Helper()
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		h.T.Fatalf("decode %q: %v", w.Body.String(), err)
	}
	return v
}

// Deliver delivers every pending event now.
func (h *Harness) Deliver() { h.Core.DeliverNow() }

// RunJob runs a job ("<instance or block>/<name>") and waits for it.
func (h *Harness) RunJob(key string) error { return h.Core.RunJob(key) }

// MustRunJob runs a job and fails the test on error.
func (h *Harness) MustRunJob(key string) {
	h.T.Helper()
	if err := h.RunJob(key); err != nil {
		h.T.Fatalf("job %s: %v", key, err)
	}
}

// Rewrite replaces companion.yaml and reloads.
func (h *Harness) Rewrite(config string) error {
	h.T.Helper()
	if err := os.WriteFile(h.Config, []byte(withTestStorage(h.T, config)), 0o600); err != nil {
		h.T.Fatal(err)
	}
	return h.Core.Reload(context.Background())
}

// Module returns the module context of a building block, for direct access
// to its database area in assertions.
func (h *Harness) Module(id string) companion.ModuleContext {
	h.T.Helper()
	mc, ok := h.Core.ModuleContext(id)
	if !ok {
		h.T.Fatalf("no building block %s", id)
	}
	return mc
}
