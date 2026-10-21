package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/companion"
	"github.com/LNA-DEV/HomePageCompanion/companion/wire"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// ErrUnavailable is returned by the parts of the context a service does
// not have.
var ErrUnavailable = errors.New("not available to a service")

// modCtx is the building block's companion.ModuleContext inside a service:
// its own database in the volume, everything else through the core.
type modCtx struct {
	s    *Service
	core *http.Client // to the core directly, never through the egress proxy
	log  *log.Logger

	mu        sync.Mutex
	publicURL string
	dbOnce    sync.Once
	db        *gorm.DB

	lines chan logLine
	quit  chan struct{}
	once  sync.Once
}

type logLine struct {
	Level   string `json:"level,omitempty"`
	Message string `json:"message"`
}

func newModCtx(s *Service) *modCtx {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	m := &modCtx{s: s, core: &http.Client{Transport: tr, Timeout: time.Minute}, lines: make(chan logLine, 1000), quit: make(chan struct{})}
	m.log = log.New(logWriter{m}, "", 0)
	return m
}

func (m *modCtx) setPublicURL(u string) {
	m.mu.Lock()
	m.publicURL = u
	m.mu.Unlock()
}

func (m *modCtx) ModuleID() string   { return m.s.info.ID }
func (m *modCtx) Log() *log.Logger   { return m.log }
func (m *modCtx) DataDir() string    { return m.s.opts.DataDir }
func (m *modCtx) HTTP() *http.Client { return m.s.opts.HTTP }

func (m *modCtx) PublicURL() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.publicURL
}

// DB is a SQLite file in the volume: services get no handle to the core's
// database (§5.10).
func (m *modCtx) DB() *gorm.DB {
	m.dbOnce.Do(func() {
		dir := filepath.Join(m.s.opts.DataDir, "db")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			m.log.Printf("database: %v", err)
			return
		}
		db, err := gorm.Open(sqlite.Open(filepath.Join(dir, m.s.info.ID+".db")+"?_busy_timeout=5000&_journal_mode=WAL"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
		if err != nil {
			m.log.Printf("database: %v", err)
			return
		}
		m.db = db
	})
	return m.db
}

func (m *modCtx) Blobs() companion.Bucket   { return unavailableBucket{} }
func (m *modCtx) Temp() companion.TempStore { return unavailableTemp{} }

// Secrets is the service's part of the core's secret store.
func (m *modCtx) Secrets() companion.Secrets { return coreSecrets{m} }

type coreSecrets struct{ m *modCtx }

func (c coreSecrets) Get(name string) (string, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var out struct {
		Value string `json:"value"`
	}
	err := c.m.coreJSON(ctx, http.MethodGet, "/core/v1/secrets/"+name, "", nil, &out)
	var we *wire.Error
	if errors.As(err, &we) && we.Code == "not_found" {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return out.Value, true, nil
}

func (c coreSecrets) Put(name, value string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return c.m.coreJSON(ctx, http.MethodPut, "/core/v1/secrets/"+name, "", map[string]string{"value": value}, nil)
}

func (c coreSecrets) Delete(name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return c.m.coreJSON(ctx, http.MethodDelete, "/core/v1/secrets/"+name, "", nil, nil)
}

func (m *modCtx) CacheDir() string {
	dir := filepath.Join(m.s.opts.DataDir, "cache")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		dir = filepath.Join(os.TempDir(), "companion-cache")
		os.MkdirAll(dir, 0o755)
	}
	return dir
}

// Emit hands the event to the core, which delivers it like a module's.
func (m *modCtx) Emit(ctx context.Context, eventType string, data any) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return m.coreJSON(ctx, http.MethodPost, "/core/v1/events", "", map[string]any{"type": eventType, "data": json.RawMessage(raw)}, nil)
}

func (m *modCtx) Instances() []companion.InstanceRef {
	m.s.mu.Lock()
	defer m.s.mu.Unlock()
	var out []companion.InstanceRef
	for name, in := range m.s.instances {
		out = append(out, companion.InstanceRef{ID: companion.InstanceID(m.s.info.ID, name), Name: name, Instance: in.value})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out
}

// Capability is another capability of an instance connected to one of
// this building block's slots, through the core.
func (m *modCtx) Capability(instanceID string, cap companion.Capability) (any, error) {
	m.s.mu.Lock()
	var from string
	var ops []string
	var slot string
	for name, in := range m.s.instances {
		for sl, ids := range in.slots {
			if contains(ids, instanceID) {
				from, slot, ops = name, sl, in.targets[instanceID]
			}
		}
	}
	m.s.mu.Unlock()
	if from == "" {
		return nil, fmt.Errorf("%s is not connected to %s", instanceID, m.s.info.ID)
	}
	return m.client(from, slot, cap, instanceID, ops)
}

// slotClient calls the instance connected to a slot through the core.
func (m *modCtx) slotClient(from string, sl companion.Slot, target string, ops []string) (any, error) {
	return m.client(from, sl.Name, sl.Needs, target, ops)
}

func (m *modCtx) client(from, slot string, cap companion.Capability, target string, ops []string) (any, error) {
	a, ok := wire.Lookup(cap.String())
	if !ok {
		return nil, fmt.Errorf("no adapter for %s", cap)
	}
	if len(ops) == 0 {
		ops = append(append([]string(nil), a.Required...), a.Optional...)
	}
	h := &wire.HTTP{Client: m.core, URL: m.coreURL() + "/core/v1/slots/" + slot, Prepare: func(r *http.Request, _ []byte) {
		m.authorize(r)
		r.Header.Set(wire.InstanceHeader, from)
		r.Header.Set(wire.TargetHeader, target)
	}}
	return a.Client(h, ops), nil
}

func (m *modCtx) coreURL() string { return strings.TrimSuffix(m.s.opts.CoreURL, "/") }

func (m *modCtx) authorize(r *http.Request) {
	if t, err := m.s.token(); err == nil {
		r.Header.Set("Authorization", "Bearer "+t)
	}
}

// coreJSON calls the core's API for services.
func (m *modCtx) coreJSON(ctx context.Context, method, path, instance string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	r, err := http.NewRequestWithContext(ctx, method, m.coreURL()+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if instance != "" {
		r.Header.Set(wire.InstanceHeader, instance)
	}
	m.authorize(r)
	resp, err := m.core.Do(r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return wire.ReadError(resp)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (m *modCtx) Metrics() companion.Metrics { return coreMetrics{m} }

// coreMetrics sends the building block's values to the core.
type coreMetrics struct{ m *modCtx }

func (c coreMetrics) send(kind, name string, v float64, labels []string) {
	l := map[string]string{}
	for i := 0; i+1 < len(labels); i += 2 {
		l[labels[i]] = labels[i+1]
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		c.m.coreJSON(ctx, http.MethodPost, "/core/v1/metrics", "", map[string]any{"values": []any{map[string]any{"name": name, "value": v, "kind": kind, "labels": l}}}, nil)
	}()
}

func (c coreMetrics) Add(name string, delta float64, labels ...string) {
	c.send("add", name, delta, labels)
}
func (c coreMetrics) Set(name string, v float64, labels ...string) { c.send("set", name, v, labels) }

// logWriter writes to stdout, which Docker keeps, and to the core's log,
// which the admin UI shows.
type logWriter struct{ m *modCtx }

func (w logWriter) Write(p []byte) (int, error) {
	fmt.Fprintf(w.m.s.opts.Logs, "%s %s", time.Now().Format("2006/01/02 15:04:05"), p)
	if w.m.s.opts.CoreURL == "" {
		return len(p), nil
	}
	w.m.once.Do(func() { go w.m.forward() })
	select {
	case w.m.lines <- logLine{Message: strings.TrimRight(string(p), "\n")}:
	default: // the core is not keeping up: stdout has it
	}
	return len(p), nil
}

// forward sends the log lines to the core, a batch a second.
func (m *modCtx) forward() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	var batch []logLine
	flush := func() {
		if len(batch) == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		m.coreJSON(ctx, http.MethodPost, "/core/v1/logs", "", map[string]any{"lines": batch}, nil)
		cancel()
		batch = nil
	}
	for {
		select {
		case l := <-m.lines:
			batch = append(batch, l)
			if len(batch) >= 100 {
				flush()
			}
		case <-t.C:
			flush()
		case <-m.quit:
			flush()
			return
		}
	}
}

func (m *modCtx) close() {
	select {
	case <-m.quit:
	default:
		close(m.quit)
	}
	if m.db != nil {
		if sqlDB, err := m.db.DB(); err == nil {
			sqlDB.Close()
		}
	}
}

// instCtx is an instance's companion.Context.
type instCtx struct {
	*modCtx
	name    string
	slots   map[string][]string
	targets map[string][]string
}

func (i *instCtx) InstanceName() string { return i.name }
func (i *instCtx) InstanceID() string   { return companion.InstanceID(i.s.info.ID, i.name) }

// Until the core's blob API for services exists, these parts are
// unavailable.
type unavailableBucket struct{}

func (unavailableBucket) Put(context.Context, string, io.Reader, int64, string) error {
	return ErrUnavailable
}
func (unavailableBucket) Get(context.Context, string) (io.ReadCloser, companion.ObjectInfo, error) {
	return nil, companion.ObjectInfo{}, ErrUnavailable
}
func (unavailableBucket) ReadRange(context.Context, string, int64, int64) ([]byte, error) {
	return nil, ErrUnavailable
}
func (unavailableBucket) Stat(context.Context, string) (companion.ObjectInfo, error) {
	return companion.ObjectInfo{}, ErrUnavailable
}
func (unavailableBucket) List(context.Context, string) ([]companion.ObjectInfo, error) {
	return nil, ErrUnavailable
}
func (unavailableBucket) Delete(context.Context, string) error { return ErrUnavailable }
func (unavailableBucket) CreateUpload(context.Context, string, string) (string, error) {
	return "", ErrUnavailable
}
func (unavailableBucket) UploadPart(context.Context, string, string, int32, []byte) (string, error) {
	return "", ErrUnavailable
}
func (unavailableBucket) CompleteUpload(context.Context, string, string, []companion.CompletedPart) error {
	return ErrUnavailable
}
func (unavailableBucket) AbortUpload(context.Context, string, string) error { return ErrUnavailable }
func (unavailableBucket) ListUploads(context.Context, string) ([]companion.Upload, error) {
	return nil, ErrUnavailable
}
func (unavailableBucket) Prefix() string { return "" }

type unavailableTemp struct{}

func (unavailableTemp) Put(context.Context, []byte, string, time.Duration) (string, error) {
	return "", ErrUnavailable
}
