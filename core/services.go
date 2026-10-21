package core

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/companion"
	"github.com/LNA-DEV/HomePageCompanion/companion/wire"
	"github.com/LNA-DEV/HomePageCompanion/core/internal/conf"
	"github.com/gin-gonic/gin"
)

// Services are building blocks in their own containers, untrusted, spoken
// to over HTTP (docs/concepts/modular-architecture.md §5.3, §5.8, §5.9,
// docs/services/protocol.md). The core plans with the manifest hpc apply
// recorded in companion.lock, so a service that is down changes nothing
// but its own state: its instances start, calls to them fail as transient
// errors until it answers, and the core configures it again whenever it
// comes back.

// The health check: every serviceRetryEvery until the service answers and
// is configured, then every serviceCheckEvery. Tests shorten them.
var serviceCheckEvery, serviceRetryEvery atomic.Int64

func init() {
	serviceCheckEvery.Store(int64(10 * time.Second))
	serviceRetryEvery.Store(int64(2 * time.Second))
}

const (
	defaultServicePort    = 8080
	defaultServicesListen = ":3128"
	// maxForwardBody caps a request forwarded to a service's API.
	maxForwardBody = 32 << 20
)

// serviceInfo is the Info of a service block, from its manifest.
func serviceInfo(block string, m *wire.Manifest) companion.Info {
	info := companion.Info{ID: block, Name: m.Name, Version: m.Version, UI: m.UI}
	for _, o := range m.Provides {
		if a, ok := wire.Lookup(o.Capability); ok {
			info.Provides = append(info.Provides, a.Capability)
		}
	}
	for _, s := range m.Slots {
		a, _ := wire.Lookup(s.Needs)
		info.Slots = append(info.Slots, companion.Slot{Name: s.Name, Needs: a.Capability, Multiple: s.Multiple, Optional: s.Optional})
	}
	// Module-level jobs run once for the service, without an instance.
	for _, j := range m.Jobs {
		if !j.Module {
			continue
		}
		timeout, _ := time.ParseDuration(j.Timeout)
		name := j.Name
		info.Jobs = append(info.Jobs, companion.Job{Name: name, Schedule: j.Schedule, Timeout: timeout,
			RunModule: func(ctx context.Context, mc companion.ModuleContext) error {
				b := mc.(*modCtx).c.services.get(block)
				if b == nil {
					return errors.New("service not running")
				}
				if err := b.readyFor(""); err != nil {
					return err
				}
				return b.post(ctx, "/jobs/"+name, "", []byte("{}"), nil)
			}})
	}
	return info
}

// grantsOf are the rights the operator configured; the volume is on unless
// switched off (§5.8).
func grantsOf(g conf.Grants) wire.Grants {
	return wire.Grants{Egress: g.Egress, Events: g.Events, Public: g.Public, Volume: g.Volume == nil || *g.Volume}
}

// serviceInstance is an instance of a service.
type serviceInstance struct {
	b      *svcBlock
	name   string
	values map[string]any // config without the slots
	slots  map[string][]string
	// targets are the operations each connected instance offers, so the
	// service knows which optional ones it may call.
	targets map[string][]string
	locked  *wire.LockedService
	conf    *conf.ServiceBlock
}

func (s *serviceInstance) id() string { return companion.InstanceID(s.b.id, s.name) }

// adapter is the capability's client, calling the service.
func (s *serviceInstance) adapter(cap companion.Capability) (any, error) {
	a, ok := wire.Lookup(cap.String())
	if !ok {
		return nil, fmt.Errorf("%s: no adapter for %s", s.id(), cap)
	}
	return a.Client(serviceCaller{s: s, path: "/capabilities/" + cap.String()}, s.locked.Manifest.Operations(cap.String())), nil
}

func (s *serviceInstance) provideNames() []string {
	var out []string
	for _, o := range s.locked.Manifest.Provides {
		out = append(out, o.Capability)
	}
	return out
}

// granted reports whether the event goes to the service: it subscribes to
// it and the operator granted it.
func (s *serviceInstance) granted(eventType string) bool {
	g := s.b.currentGrants()
	return contains(s.locked.Manifest.Subscribes, eventType) && contains(g.Events, eventType)
}

// deliver posts an event; the service deduplicates by its ID.
func (s *serviceInstance) deliver(ctx context.Context, ev companion.Event) error {
	if err := s.b.readyFor(s.name); err != nil {
		return err
	}
	body, _ := json.Marshal(ev)
	return s.b.post(ctx, "/events", s.name, body, nil)
}

// Start and Stop add the instance to its service's configuration.
func (s *serviceInstance) Start() error {
	s.b.add(s)
	return nil
}

func (s *serviceInstance) Stop(ctx context.Context) error {
	s.b.remove(s, ctx)
	return nil
}

// Jobs are the manifest's instance jobs, run as POST /jobs/<name> for this
// instance. The answer to the configuration may replace them.
func (s *serviceInstance) Jobs() []companion.Job { return s.jobsFrom(s.locked.Manifest.Jobs) }

func (s *serviceInstance) jobsFrom(jobs []wire.Job) []companion.Job {
	var out []companion.Job
	for _, j := range jobs {
		if j.Module {
			continue
		}
		sched := j.Schedule
		if j.ScheduleFrom != "" {
			sched, _ = s.values[j.ScheduleFrom].(string)
		}
		timeout, _ := time.ParseDuration(j.Timeout)
		name := j.Name
		out = append(out, companion.Job{Name: name, Schedule: sched, Timeout: timeout, Run: func(ctx context.Context) error {
			if err := s.b.readyFor(s.name); err != nil {
				return err
			}
			return s.b.post(ctx, "/jobs/"+name, s.name, []byte("{}"), nil)
		}})
	}
	return out
}

// serviceCaller sends capability calls to a service instance.
type serviceCaller struct {
	s    *serviceInstance
	path string
}

func (sc serviceCaller) Call(ctx context.Context, op string, req wire.Request, out any) error {
	b := sc.s.b
	if err := b.readyFor(sc.s.name); err != nil {
		return err
	}
	h := wire.HTTP{Client: b.m.client, URL: b.baseURL() + sc.path, Prepare: b.signer(sc.s.name)}
	return b.noteLost(h.Call(ctx, op, req, out))
}

// noteLost notices a service that no longer knows its instances, because
// it restarted between two health checks, and configures it again at once.
func (b *svcBlock) noteLost(err error) error {
	var we *wire.Error
	if !errors.As(err, &we) || we.Code != wire.CodeUnknownInstance {
		return err
	}
	b.mu.Lock()
	b.configured = ""
	kick := b.kick
	b.mu.Unlock()
	if kick != nil {
		select {
		case kick <- struct{}{}:
		default:
		}
	}
	return &wire.Error{Code: wire.CodeUnavailable, Message: fmt.Sprintf("service %s lost its configuration (restarted?); configuring it again", b.id)}
}

// svcBlock is one service: its container, its token, its instances and
// what the core knows about its state.
type svcBlock struct {
	m     *serviceManager
	id    string
	token string

	mu         sync.Mutex
	locked     *wire.LockedService
	grants     wire.Grants
	url        string
	instances  map[string]*serviceInstance
	version    string // of the configuration the current instances make
	configured string // the version the service took
	healthy    bool
	everUp     bool
	problem    string
	instErr    map[string]string
	restarts   int
	addrs      []net.IP // its container's addresses, for the egress proxy
	addrsAt    time.Time
	looping    bool
	kick, stop chan struct{}
	done       chan struct{}
}

func (b *svcBlock) baseURL() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.TrimSuffix(b.url, "/")
}

func (b *svcBlock) currentGrants() wire.Grants {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.grants
}

// signer signs a request to the service for one of its instances.
func (b *svcBlock) signer(instance string) func(*http.Request, []byte) {
	return func(r *http.Request, body []byte) {
		if instance != "" {
			r.Header.Set(wire.InstanceHeader, instance)
		}
		wire.Sign(r, body, []byte(b.token), time.Now())
	}
}

// readyFor is nil when the service answers, took the current
// configuration and accepted the instance's.
func (b *svcBlock) readyFor(instance string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch {
	case !b.healthy:
		p := b.problem
		if p == "" {
			p = "not checked yet"
		}
		return &wire.Error{Code: wire.CodeUnavailable, Message: fmt.Sprintf("service %s is not reachable: %s", b.id, p)}
	case b.configured != b.version:
		return &wire.Error{Code: wire.CodeUnavailable, Message: fmt.Sprintf("service %s is not configured yet", b.id)}
	case b.instErr[instance] != "":
		return &wire.Error{Code: "not_configured", Message: fmt.Sprintf("%s:%s: %s", b.id, instance, b.instErr[instance]), Cause: "account"}
	}
	return nil
}

// state is what the instance views show.
func (b *svcBlock) state(instance string) (ready bool, problem string) {
	if err := b.readyFor(instance); err != nil {
		return false, err.Error()
	}
	return true, ""
}

func (b *svcBlock) add(s *serviceInstance) {
	b.mu.Lock()
	b.instances[s.name] = s
	b.locked = s.locked
	b.grants = grantsOf(s.conf.Grants).Intersect(s.locked.Grants)
	b.url = serviceURL(b.id, s.conf, s.locked)
	b.version = b.configVersion()
	start := !b.looping
	if start {
		b.looping = true
		b.kick, b.stop, b.done = make(chan struct{}, 1), make(chan struct{}), make(chan struct{})
	}
	kick := b.kick
	b.mu.Unlock()
	if start {
		go b.loop()
	}
	select {
	case kick <- struct{}{}:
	default:
	}
}

func (b *svcBlock) remove(s *serviceInstance, ctx context.Context) {
	b.mu.Lock()
	if b.instances[s.name] == s {
		delete(b.instances, s.name)
	}
	b.version = b.configVersion()
	last := len(b.instances) == 0 && b.looping
	var stop, done chan struct{}
	if last {
		b.looping = false
		stop, done = b.stop, b.done
	} else {
		select {
		case b.kick <- struct{}{}:
		default:
		}
	}
	b.mu.Unlock()
	if last {
		close(stop)
		select {
		case <-done:
		case <-ctx.Done():
		}
	}
}

// serviceURL is where the core reaches a service: the configured URL, the
// one hpc apply recorded, or http://<block>:<port> next to the companion.
func serviceURL(id string, sb *conf.ServiceBlock, ls *wire.LockedService) string {
	switch {
	case sb != nil && sb.URL != "":
		return sb.URL
	case ls.URL != "":
		return ls.URL
	}
	port := ls.Manifest.Port
	if port == 0 {
		port = defaultServicePort
	}
	return fmt.Sprintf("http://%s:%d", id, port)
}

// configureBody is POST /lifecycle/configure.
type configureBody struct {
	Version   string                       `json:"version"`
	Service   string                       `json:"service"`
	PublicURL string                       `json:"publicUrl,omitempty"`
	Instances map[string]configureInstance `json:"instances"`
}

type configureInstance struct {
	Config  map[string]any      `json:"config"`
	Slots   map[string][]string `json:"slots"`
	Targets map[string][]string `json:"targets"`
}

// configBody is the configuration of the current instances; b.mu held.
func (b *svcBlock) configBody() configureBody {
	body := configureBody{Service: b.id, PublicURL: b.m.c.cfg().Core.PublicURL, Instances: map[string]configureInstance{}}
	for name, s := range b.instances {
		ci := configureInstance{Config: s.values, Slots: s.slots, Targets: s.targets}
		if ci.Config == nil {
			ci.Config = map[string]any{}
		}
		if ci.Slots == nil {
			ci.Slots = map[string][]string{}
		}
		if ci.Targets == nil {
			ci.Targets = map[string][]string{}
		}
		body.Instances[name] = ci
	}
	return body
}

// configVersion identifies the configuration; b.mu held.
func (b *svcBlock) configVersion() string {
	raw, _ := json.Marshal(b.configBody())
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:8])
}

func (b *svcBlock) loop() {
	b.mu.Lock()
	kick, stop, done := b.kick, b.stop, b.done
	b.mu.Unlock()
	defer close(done)
	t := time.NewTimer(0)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-kick:
		case <-t.C:
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		b.check(ctx)
		cancel()
		next := time.Duration(serviceCheckEvery.Load())
		if b.readyFor("") != nil {
			next = time.Duration(serviceRetryEvery.Load())
		}
		if !t.Stop() {
			select {
			case <-t.C:
			default:
			}
		}
		t.Reset(next)
	}
}

// health is GET /health.
type health struct {
	Status        string `json:"status"`
	ConfigVersion string `json:"configVersion"`
}

// check asks the service whether it is up and has the current
// configuration, and configures it if not.
func (b *svcBlock) check(ctx context.Context) {
	var h health
	hctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	err := b.get(hctx, "/health", &h)
	cancel()
	b.mu.Lock()
	if err != nil {
		if b.healthy {
			b.m.c.log.Printf("services: %s stopped answering: %v", b.id, err)
		}
		b.healthy, b.configured, b.problem = false, "", err.Error()
		b.mu.Unlock()
		return
	}
	if !b.healthy {
		if b.everUp {
			b.restarts++
		}
		b.healthy, b.everUp = true, true
		b.configured = ""
	}
	version := b.version
	// The service reports the version of the configuration it holds, ""
	// before the first one; it is configured again whenever that differs,
	// also after a restart between two checks.
	if h.ConfigVersion == version {
		b.configured, b.problem = version, ""
		b.mu.Unlock()
		return
	}
	body := b.configBody()
	body.Version = version
	b.mu.Unlock()

	raw, _ := json.Marshal(body)
	var res struct {
		Errors map[string]string `json:"errors"`
		// Jobs per instance, as the configured instances name them
		// (their schedules often come from their config).
		Jobs map[string][]wire.Job `json:"jobs"`
	}
	err = b.post(ctx, "/lifecycle/configure", "", raw, &res)
	b.mu.Lock()
	if err != nil {
		b.problem = "configure: " + err.Error()
		b.m.c.log.Printf("services: %s: %s", b.id, b.problem)
		b.mu.Unlock()
		return
	}
	if b.version != version {
		b.mu.Unlock()
		return // the instances changed meanwhile; the next check configures again
	}
	b.configured, b.problem, b.instErr = version, "", res.Errors
	var jobs []*serviceInstance
	for name := range res.Jobs {
		if s := b.instances[name]; s != nil {
			jobs = append(jobs, s)
		}
	}
	b.mu.Unlock()
	for name, e := range res.Errors {
		b.m.c.log.Printf("services: %s:%s refused its configuration: %s", b.id, name, e)
	}
	for _, s := range jobs {
		b.m.c.jobs.removeOwner(s.id())
		for _, j := range s.jobsFrom(res.Jobs[s.name]) {
			b.m.c.jobs.add(s.id(), j)
		}
	}
	b.m.c.log.Printf("services: %s configured (%d instance(s))", b.id, len(body.Instances))
}

// get and post talk to the service, signed.
func (b *svcBlock) get(ctx context.Context, path string, out any) error {
	return b.do(ctx, http.MethodGet, path, "", nil, out)
}

func (b *svcBlock) post(ctx context.Context, path, instance string, body []byte, out any) error {
	return b.do(ctx, http.MethodPost, path, instance, body, out)
}

func (b *svcBlock) do(ctx context.Context, method, path, instance string, body []byte, out any) error {
	r, err := http.NewRequestWithContext(ctx, method, b.baseURL()+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	b.signer(instance)(r, body)
	resp, err := b.m.client.Do(r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return b.noteLost(wire.ReadError(resp))
	}
	if out == nil {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil && method == http.MethodPost {
		return fmt.Errorf("%s: answer does not decode: %w", path, err)
	}
	return nil
}

// serviceManager starts and talks to the services.
type serviceManager struct {
	c      *Core
	client *http.Client

	mu      sync.Mutex
	lockRaw []byte
	blocks  map[string]*svcBlock
	srv     *http.Server
	addr    string
}

func newServiceManager(c *Core) *serviceManager {
	// Services are reached directly, never through a proxy of the
	// companion's own environment.
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	return &serviceManager{c: c, client: &http.Client{Transport: tr}, blocks: map[string]*svcBlock{}}
}

func (m *serviceManager) lockPath() string {
	if m.c.opts.LockPath != "" {
		return m.c.opts.LockPath
	}
	return filepath.Join(filepath.Dir(m.c.opts.ConfigPath), wire.LockFile)
}

func (m *serviceManager) readLock() (*wire.Lock, []byte, error) { return wire.ReadLock(m.lockPath()) }

// instance builds a service instance; Start adds it to the service.
// resolve finds the instances its slots connect to.
func (m *serviceManager) instance(p *planned, resolve func(id string) *instance) (*serviceInstance, error) {
	m.mu.Lock()
	b := m.blocks[p.block]
	m.mu.Unlock()
	if b == nil {
		token, err := m.tokenFor(p.block)
		if err != nil {
			return nil, err
		}
		b = &svcBlock{m: m, id: p.block, token: token, instances: map[string]*serviceInstance{}}
		m.mu.Lock()
		if have := m.blocks[p.block]; have != nil {
			b = have
		} else {
			m.blocks[p.block] = b
		}
		m.mu.Unlock()
	}
	// Settings before the first start too, for a backup of a companion
	// that only prepared its instances (the CLI).
	b.mu.Lock()
	if b.locked == nil {
		b.locked, b.grants, b.url = p.locked, grantsOf(p.service.Grants).Intersect(p.locked.Grants), serviceURL(b.id, p.service, p.locked)
	}
	volume := b.grants.Volume
	b.mu.Unlock()
	if volume {
		m.prepareVolume(p.block, p.locked.User)
	}
	targets := map[string][]string{}
	for _, sl := range p.info.Slots {
		for _, id := range p.slots[sl.Name] {
			t := resolve(id)
			if t == nil {
				return nil, fmt.Errorf("no instance %s", id)
			}
			targets[id] = operationsOf(t, sl.Needs)
		}
	}
	return &serviceInstance{b: b, name: p.name, values: p.values, slots: p.slots, targets: targets, locked: p.locked, conf: p.service}, nil
}

// operationsOf lists the operations an instance offers of a capability.
func operationsOf(in *instance, cap companion.Capability) []string {
	if in.service != nil {
		return in.service.locked.Manifest.Operations(cap.String())
	}
	a, ok := wire.Lookup(cap.String())
	if !ok {
		return nil
	}
	if srv := a.Server(in.value); srv != nil {
		return srv.Operations()
	}
	return nil
}

// tokenFor is the service's token: it authenticates the service to the
// core and keys the core's signatures. It is kept in the secret store and
// handed to the container as a file, data/services/<id>/run/token, which
// hpc apply mounts read-only at /run/companion/.
func (m *serviceManager) tokenFor(id string) (string, error) {
	sec := m.c.secrets.scope("core")
	name := "service-token:" + id
	token, ok, err := sec.Get(name)
	if err != nil {
		return "", fmt.Errorf("service token: %w", err)
	}
	if !ok {
		b := make([]byte, 32)
		rand.Read(b)
		token = hex.EncodeToString(b)
		if err := sec.Put(name, token); err != nil {
			return "", fmt.Errorf("service token: %w", err)
		}
	}
	dir := filepath.Join(m.c.opts.DataDir, "services", id, "run")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "token"), []byte(token+"\n"), 0o644); err != nil {
		return "", err
	}
	return token, nil
}

// ServiceToken returns a service's token; for tests and for running a
// service by hand.
func (c *Core) ServiceToken(id string) (string, bool) {
	c.services.mu.Lock()
	defer c.services.mu.Unlock()
	b, ok := c.services.blocks[id]
	if !ok {
		return "", false
	}
	return b.token, true
}

// active are the services with running instances.
func (m *serviceManager) active() []*svcBlock {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*svcBlock
	for _, b := range m.blocks {
		b.mu.Lock()
		n := len(b.instances)
		b.mu.Unlock()
		if n > 0 {
			out = append(out, b)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}

func (m *serviceManager) get(id string) *svcBlock {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.blocks[id]
}

func (m *serviceManager) isService(id string) bool {
	b := m.get(id)
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.instances) > 0
}

// stopAll stops the health checks and the services' listener.
func (m *serviceManager) stopAll(ctx context.Context) {
	m.mu.Lock()
	var blocks []*svcBlock
	for _, b := range m.blocks {
		blocks = append(blocks, b)
	}
	srv := m.srv
	m.srv, m.addr = nil, ""
	m.mu.Unlock()
	for _, b := range blocks {
		b.mu.Lock()
		insts := make([]*serviceInstance, 0, len(b.instances))
		for _, s := range b.instances {
			insts = append(insts, s)
		}
		b.mu.Unlock()
		for _, s := range insts {
			b.remove(s, ctx)
		}
	}
	if srv != nil {
		srv.Shutdown(ctx)
	}
}

// listen opens the services' listener once a service is configured.
func (m *serviceManager) listen() error {
	if len(m.active()) == 0 {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.srv != nil {
		return nil
	}
	addr := m.c.opts.ServicesListen
	if addr == "" {
		addr = m.c.cfg().Core.ServicesListen
	}
	if addr == "" {
		addr = defaultServicesListen
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("services listener: %w", err)
	}
	m.srv = &http.Server{Handler: m, ReadHeaderTimeout: 10 * time.Second}
	m.addr = ln.Addr().String()
	go m.srv.Serve(ln)
	m.c.log.Printf("services: listening on %s", m.addr)
	return nil
}

// ServicesAddr is the address the services' listener is bound to, "" when
// it is not open.
func (c *Core) ServicesAddr() string {
	c.services.mu.Lock()
	defer c.services.mu.Unlock()
	return c.services.addr
}

// ServeHTTP is the services' listener: the core's API for services, and
// (CONNECT or an absolute URL) the egress proxy.
func (m *serviceManager) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect || r.URL.IsAbs() {
		m.egress(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/core/v1/") {
		m.coreAPI(w, r)
		return
	}
	http.NotFound(w, r)
}

func apiError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]*wire.Error{"error": {Code: code, Message: msg}})
}

// byToken finds the service a token belongs to.
func (m *serviceManager) byToken(token string) *svcBlock {
	if token == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var found *svcBlock
	for _, b := range m.blocks {
		if subtle.ConstantTimeCompare([]byte(b.token), []byte(token)) == 1 {
			found = b
		}
	}
	return found
}

// coreAPI is /core/v1/, the core's API for services, authenticated by the
// service's token. A call through a slot needs the calling instance
// (Companion-Instance) and, when several are connected, the target
// (Companion-Target).
func (m *serviceManager) coreAPI(w http.ResponseWriter, r *http.Request) {
	token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	b := m.byToken(strings.TrimSpace(token))
	if b == nil {
		apiError(w, http.StatusUnauthorized, "unauthorized", "missing or unknown service token")
		return
	}
	name := r.Header.Get(wire.InstanceHeader)
	b.mu.Lock()
	si := b.instances[name]
	b.mu.Unlock()
	rest := strings.TrimPrefix(r.URL.Path, "/core/v1/")
	switch {
	case strings.HasPrefix(rest, "slots/") && r.Method == http.MethodPost:
		slot, op, ok := strings.Cut(strings.TrimPrefix(rest, "slots/"), "/")
		if !ok || si == nil {
			apiError(w, http.StatusBadRequest, wire.CodeBadRequest, "POST /core/v1/slots/<slot>/<operation> with "+wire.InstanceHeader+" naming one of the service's instances")
			return
		}
		m.callSlot(w, r, b, si, slot, op)
	case rest == "slots" && r.Method == http.MethodGet:
		if si == nil {
			apiError(w, http.StatusBadRequest, wire.CodeBadRequest, wire.InstanceHeader+" names no instance of "+b.id)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(si.slots)
	case rest == "logs" && r.Method == http.MethodPost:
		m.serviceLogs(w, r, b)
	case rest == "metrics" && r.Method == http.MethodPost:
		m.serviceMetrics(w, r, b)
	case strings.HasPrefix(rest, "secrets/"):
		m.serviceSecret(w, r, b, strings.TrimPrefix(rest, "secrets/"))
	case rest == "events" && r.Method == http.MethodPost:
		var ev struct {
			Type string          `json:"type"`
			Data json.RawMessage `json:"data"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&ev); err != nil || ev.Type == "" {
			apiError(w, http.StatusBadRequest, wire.CodeBadRequest, "body: {\"type\": \"…/v1\", \"data\": {…}}")
			return
		}
		if err := m.c.events.emit(r.Context(), b.id, ev.Type, ev.Data); err != nil {
			apiError(w, http.StatusInternalServerError, wire.CodeInternal, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		apiError(w, http.StatusNotFound, "not_found", "no such route: "+r.Method+" "+r.URL.Path)
	}
}

// callSlot runs an operation on an instance connected to one of the
// calling instance's slots, whether a module or another service.
func (m *serviceManager) callSlot(w http.ResponseWriter, r *http.Request, b *svcBlock, si *serviceInstance, slot, op string) {
	var ms *wire.Slot
	for i := range si.locked.Manifest.Slots {
		if si.locked.Manifest.Slots[i].Name == slot {
			ms = &si.locked.Manifest.Slots[i]
		}
	}
	if ms == nil {
		apiError(w, http.StatusNotFound, "not_found", fmt.Sprintf("%s has no slot %s", b.id, slot))
		return
	}
	ids := si.slots[slot]
	target := r.Header.Get(wire.TargetHeader)
	if target == "" {
		if len(ids) != 1 {
			apiError(w, http.StatusBadRequest, wire.CodeBadRequest, fmt.Sprintf("slot %s has %d connected instances, name one in %s", slot, len(ids), wire.TargetHeader))
			return
		}
		target = ids[0]
	}
	if !contains(ids, target) {
		apiError(w, http.StatusForbidden, "not_connected", fmt.Sprintf("%s is not connected to %s's slot %s", target, si.id(), slot))
		return
	}
	a, _ := wire.Lookup(ms.Needs)
	m.c.mu.RLock()
	in := m.c.instances[target]
	m.c.mu.RUnlock()
	if in == nil || !in.running {
		apiError(w, http.StatusServiceUnavailable, wire.CodeUnavailable, target+" is not running")
		return
	}
	impl, err := in.provide(a.Capability)
	if err != nil {
		apiError(w, http.StatusInternalServerError, wire.CodeInternal, err.Error())
		return
	}
	srv := a.Server(m.c.measured(a.Capability, target, impl))
	if srv == nil {
		apiError(w, http.StatusInternalServerError, wire.CodeInternal, fmt.Sprintf("%s does not implement %s", target, ms.Needs))
		return
	}
	srv.Serve(w, r, op)
}

var secretName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// serviceSecret is the service's part of the secret store (§5.10), apart
// from every module's: GET, PUT {"value": …} and DELETE
// /core/v1/secrets/<name>.
func (m *serviceManager) serviceSecret(w http.ResponseWriter, r *http.Request, b *svcBlock, name string) {
	if !secretName.MatchString(name) {
		apiError(w, http.StatusBadRequest, wire.CodeBadRequest, "secret names are letters, digits and . _ : -")
		return
	}
	sec := m.c.secrets.scope("service:" + b.id)
	switch r.Method {
	case http.MethodGet:
		v, ok, err := sec.Get(name)
		switch {
		case err != nil:
			apiError(w, http.StatusInternalServerError, wire.CodeInternal, err.Error())
		case !ok:
			apiError(w, http.StatusNotFound, "not_found", "no secret "+name)
		default:
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"value": v})
		}
	case http.MethodPut:
		var body struct {
			Value *string `json:"value"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil || body.Value == nil {
			apiError(w, http.StatusBadRequest, wire.CodeBadRequest, `body: {"value": "…"}`)
			return
		}
		if err := sec.Put(name, *body.Value); err != nil {
			apiError(w, http.StatusInternalServerError, wire.CodeInternal, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case http.MethodDelete:
		if err := sec.Delete(name); err != nil {
			apiError(w, http.StatusInternalServerError, wire.CodeInternal, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		apiError(w, http.StatusMethodNotAllowed, wire.CodeBadRequest, "GET, PUT or DELETE")
	}
}

// serviceLogs writes a service's log lines to the shared log, as module
// lines are: "module=<id> …".
func (m *serviceManager) serviceLogs(w http.ResponseWriter, r *http.Request, b *svcBlock) {
	var body struct {
		Lines []struct {
			Level    string `json:"level"`
			Message  string `json:"message"`
			Instance string `json:"instance"`
		} `json:"lines"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		apiError(w, http.StatusBadRequest, wire.CodeBadRequest, err.Error())
		return
	}
	m.c.mu.RLock()
	mc := m.c.mcs[b.id]
	m.c.mu.RUnlock()
	if mc == nil {
		apiError(w, http.StatusServiceUnavailable, wire.CodeUnavailable, "not running")
		return
	}
	for i, l := range body.Lines {
		if i >= 200 {
			break
		}
		msg := strings.ReplaceAll(l.Message, "\n", " ")
		if len(msg) > 4000 {
			msg = msg[:4000] + "…"
		}
		prefix := ""
		if l.Level != "" && l.Level != "info" {
			prefix = strings.ToUpper(l.Level) + " "
		}
		if l.Instance != "" {
			prefix += l.Instance + ": "
		}
		mc.log.Print(prefix + msg)
	}
	w.WriteHeader(http.StatusNoContent)
}

var metricName = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// serviceMetrics records a service's own values, labelled with it.
func (m *serviceManager) serviceMetrics(w http.ResponseWriter, r *http.Request, b *svcBlock) {
	var body struct {
		Values []struct {
			Name   string            `json:"name"`
			Value  float64           `json:"value"`
			Kind   string            `json:"kind"` // add | set
			Labels map[string]string `json:"labels"`
		} `json:"values"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		apiError(w, http.StatusBadRequest, wire.CodeBadRequest, err.Error())
		return
	}
	sm := m.c.metrics.scope(b.id)
	for _, v := range body.Values {
		if !metricName.MatchString(v.Name) {
			apiError(w, http.StatusBadRequest, wire.CodeBadRequest, fmt.Sprintf("metric name %q", v.Name))
			return
		}
		var labels []string
		keys := make([]string, 0, len(v.Labels))
		for k := range v.Labels {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if metricName.MatchString(k) {
				labels = append(labels, k, v.Labels[k])
			}
		}
		if v.Kind == "set" {
			sm.Set(v.Name, v.Value, labels...)
		} else {
			sm.Add(v.Name, v.Value, labels...)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// routes forwards /api/b/<service>/… to the service's /api/…, for the
// admin, and /api/b/<service>/public/… to its /public/… when it has the
// public grant.
func (m *serviceManager) routes(r *gin.Engine, admin gin.HandlerFunc) {
	for _, b := range m.active() {
		r.Any("/api/b/"+b.id+"/*path", m.forward(b, admin))
	}
}

func (m *serviceManager) forward(b *svcBlock, admin gin.HandlerFunc) gin.HandlerFunc {
	return func(g *gin.Context) {
		p := g.Param("path")
		target := "/api" + p
		if rest, ok := strings.CutPrefix(p, "/public"); ok && (rest == "" || strings.HasPrefix(rest, "/")) {
			if !b.currentGrants().Public {
				g.JSON(http.StatusNotFound, gin.H{"error": "not found"})
				return
			}
			g.Header("Access-Control-Allow-Origin", "*")
			target = "/public" + rest
		} else if !m.uiTokenFor(g, b.id) {
			// The admin, or the service's own UI through the shell.
			admin(g)
			if g.IsAborted() {
				return
			}
		}
		b.proxy(g, target)
	}
}

// forwarded are the headers passed on in both directions.
var forwardedRequest = []string{"Content-Type", "Accept", "Accept-Language", "Range", "If-None-Match", "If-Modified-Since"}
var forwardedResponse = []string{"Content-Type", "Content-Length", "Content-Disposition", "Content-Range", "Accept-Ranges", "Cache-Control", "ETag", "Last-Modified"}

func (b *svcBlock) proxy(g *gin.Context, target string) {
	body, err := io.ReadAll(io.LimitReader(g.Request.Body, maxForwardBody+1))
	if err != nil || len(body) > maxForwardBody {
		g.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "body too large"})
		return
	}
	u := b.baseURL() + target
	if q := g.Request.URL.RawQuery; q != "" {
		u += "?" + q
	}
	req, err := http.NewRequestWithContext(g.Request.Context(), g.Request.Method, u, bytes.NewReader(body))
	if err != nil {
		g.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	for _, h := range forwardedRequest {
		if v := g.Request.Header.Get(h); v != "" {
			req.Header.Set(h, v)
		}
	}
	b.signer(g.GetHeader(wire.InstanceHeader))(req, body)
	resp, err := b.m.client.Do(req)
	if err != nil {
		g.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("service %s: %v", b.id, err)})
		return
	}
	defer resp.Body.Close()
	for _, h := range forwardedResponse {
		if v := resp.Header.Get(h); v != "" {
			g.Header(h, v)
		}
	}
	g.Status(resp.StatusCode)
	io.Copy(g.Writer, resp.Body)
}

// serveUI answers /ui/b/<service>/…: a service's UI is reached only
// through a UI token, /ui/s/<token>/… (serviceui.go).
func (m *serviceManager) serveUI(g *gin.Context, id, file string) {
	g.Status(http.StatusNotFound)
}

// gauges reports every service's state.
func (m *serviceManager) gauges() {
	for _, b := range m.active() {
		ready, _ := b.state("")
		v := 0.0
		if ready {
			v = 1
		}
		b.mu.Lock()
		restarts := b.restarts
		b.mu.Unlock()
		m.c.metrics.set("companion_service_up", v, "service", b.id)
		m.c.metrics.set("companion_service_restarts", float64(restarts), "service", b.id)
	}
}

// prepareVolume creates a service's volume directory and, where the
// companion may (it runs as root in its container), hands it to the user
// the service runs as, so a container that is not root can write to it.
func (m *serviceManager) prepareVolume(id, user string) {
	dir := m.volumeDir(id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		m.c.log.Printf("services: %s: volume: %v", id, err)
		return
	}
	uid, gid, ok := parseUser(user)
	if !ok || os.Geteuid() != 0 {
		return
	}
	filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err == nil {
			os.Lchown(path, uid, gid)
		}
		return nil
	})
}

// parseUser reads a numeric "uid:gid" (or "uid").
func parseUser(s string) (uid, gid int, ok bool) {
	u, g, found := strings.Cut(s, ":")
	var err error
	if uid, err = strconv.Atoi(u); err != nil || uid < 0 {
		return 0, 0, false
	}
	gid = uid
	if found {
		if gid, err = strconv.Atoi(g); err != nil || gid < 0 {
			return 0, 0, false
		}
	}
	return uid, gid, true
}

// volumeDir is a service's volume on the companion's side.
func (m *serviceManager) volumeDir(id string) string {
	return filepath.Join(m.c.opts.DataDir, "services", id, "volume")
}

// backupVolumes adds every service volume to a backup, the service paused
// meanwhile where it supports that (POST /lifecycle/pause and /resume).
func (m *serviceManager) backupVolumes(ctx context.Context, tw *tar.Writer) ([]string, error) {
	var names []string
	m.mu.Lock()
	all := make([]*svcBlock, 0, len(m.blocks))
	for _, b := range m.blocks {
		all = append(all, b)
	}
	m.mu.Unlock()
	sort.Slice(all, func(i, j int) bool { return all[i].id < all[j].id })
	for _, b := range all {
		if !b.currentGrants().Volume {
			continue
		}
		dir := m.volumeDir(b.id)
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		paused := b.readyFor("") == nil && b.post(ctx, "/lifecycle/pause", "", []byte("{}"), nil) == nil
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			rel, _ := filepath.Rel(dir, path)
			return addFile(tw, path, "services/"+b.id+"/"+filepath.ToSlash(rel))
		})
		if paused {
			if rerr := b.post(context.Background(), "/lifecycle/resume", "", []byte("{}"), nil); rerr != nil {
				m.c.log.Printf("services: %s: resume after backup: %v", b.id, rerr)
			}
		}
		if err != nil {
			return nil, fmt.Errorf("backup %s volume: %w", b.id, err)
		}
		names = append(names, b.id)
	}
	return names, nil
}

// restoreVolume writes one file of a service volume back.
func (m *serviceManager) restoreVolume(ctx context.Context, name string, r io.Reader) error {
	rest := strings.TrimPrefix(name, "services/")
	id, file, ok := strings.Cut(rest, "/")
	if !ok || id == "" || file == "" || strings.Contains(file, "..") {
		return fmt.Errorf("restore: bad service file %s", name)
	}
	if err := writeFrom(r, filepath.Join(m.volumeDir(id), filepath.FromSlash(file))); err != nil {
		return err
	}
	// Back to the service's user, as the volume was.
	if b := m.get(id); b != nil {
		b.mu.Lock()
		locked := b.locked
		b.mu.Unlock()
		if locked != nil {
			m.prepareVolume(id, locked.User)
		}
	}
	return nil
}

// measured wraps a provider so that its calls count in /metrics, per
// capability, provider instance and result.
func (c *Core) measured(cap companion.Capability, instance string, impl any) any {
	a, ok := wire.Lookup(cap.String())
	if !ok || a.Measure == nil {
		return impl
	}
	return a.Measure(impl, func(op string, start time.Time, err error) {
		result := "ok"
		if err != nil {
			result = "error"
		}
		c.metrics.add("companion_capability_calls_total", 1, "capability", cap.String(), "operation", op, "instance", instance, "result", result)
		c.metrics.observe("companion_capability_call_seconds", time.Since(start).Seconds(), "capability", cap.String(), "instance", instance)
	})
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

var errServiceNotApplied = errors.New("not applied yet: run hpc apply")
