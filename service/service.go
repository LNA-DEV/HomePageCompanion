// Package service runs building blocks written with the companion SDK as a
// service: its own container, spoken to over HTTP
// (docs/concepts/modular-architecture.md §5.12, docs/services/protocol.md).
// The same module ships as both:
//
//	func main() { service.Serve(autoupload.Info) }
//
// The service answers the protocol (manifest, health, configure,
// capabilities, jobs, events, its API) and gives the instances a
// companion.Context whose slots, secrets and blobs go through the core.
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
	"os/signal"
	"reflect"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/companion"
	"github.com/LNA-DEV/HomePageCompanion/companion/wire"
	"github.com/LNA-DEV/HomePageCompanion/internal/blocks"
	"github.com/gin-gonic/gin"
)

// Options configure a service; Serve reads them from the environment.
type Options struct {
	// CoreURL is the core's API for services (COMPANION_URL,
	// http://companion:3128 next to the companion).
	CoreURL string
	// Token authenticates the service; without it, TokenFile is read
	// (COMPANION_TOKEN_FILE, /run/companion/token).
	Token     string
	TokenFile string
	// TokenSource, when set, is asked instead (tests, other secret stores).
	TokenSource func() (string, error)
	// DataDir is the volume (COMPANION_DATA, /data).
	DataDir string
	// Listen is the address (":" + PORT, ":8080").
	Listen string
	// HTTP is the client for the instances' outgoing requests; the
	// default goes through HTTP_PROXY, the core's egress proxy.
	HTTP *http.Client
	// Logs is where log lines go besides the core (stdout).
	Logs io.Writer
	// Now is the clock for signatures; tests set it.
	Now func() time.Time
}

// FromEnv reads the options from the environment.
func FromEnv() Options {
	o := Options{CoreURL: os.Getenv("COMPANION_URL"), Token: os.Getenv("COMPANION_TOKEN"), TokenFile: os.Getenv("COMPANION_TOKEN_FILE"), DataDir: os.Getenv("COMPANION_DATA")}
	if o.CoreURL == "" {
		o.CoreURL = "http://companion:3128"
	}
	if o.TokenFile == "" {
		o.TokenFile = "/run/companion/token"
	}
	if o.DataDir == "" {
		o.DataDir = "/data"
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	o.Listen = ":" + port
	return o
}

// Serve runs the building block as a service until SIGTERM. Started with
// COMPANION_MANIFEST=print, it prints its manifest and exits (hpc apply).
func Serve(info companion.Info) {
	s, err := New(FromEnv(), info)
	if err != nil {
		log.Fatal(err)
	}
	if os.Getenv("COMPANION_MANIFEST") == "print" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(s.Manifest())
		return
	}
	srv := &http.Server{Addr: s.opts.Listen, Handler: s, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()
	s.log.Printf("serving %s on %s", info.ID, s.opts.Listen)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, os.Interrupt)
	<-sig
	// Docker waits 30 s after SIGTERM (§5.9).
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
	s.Close(ctx)
}

// Service is a running service.
type Service struct {
	opts   Options
	info   companion.Info
	log    *log.Logger
	router *gin.Engine
	mc     *modCtx

	mu        sync.Mutex
	version   string
	instances map[string]*inst
	started   bool
	seen      map[string]time.Time // event IDs, for deduplication
	tokenErr  error
}

// inst is one configured instance.
type inst struct {
	name    string
	raw     string // its configuration, to see whether it changed
	value   companion.Instance
	servers map[string]*wire.Server
	jobs    map[string]companion.Job
	slots   map[string][]string
	targets map[string][]string
}

// New prepares a service for one building block.
func New(opts Options, info companion.Info) (*Service, error) {
	if info.ID == "" || info.New == nil {
		return nil, errors.New("service: the building block needs an ID and New")
	}
	if opts.Logs == nil {
		opts.Logs = os.Stdout
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.HTTP == nil {
		opts.HTTP = &http.Client{}
	}
	s := &Service{opts: opts, info: info, instances: map[string]*inst{}, seen: map[string]time.Time{}}
	s.log = log.New(opts.Logs, "", log.LstdFlags)
	s.mc = newModCtx(s)
	gin.SetMode(gin.ReleaseMode)
	s.router = gin.New()
	s.router.Use(gin.Recovery())
	if info.Routes != nil {
		router := companion.NewRouter(s.router.Group("/api"), s.router.Group("/public"), func(*gin.Context) {}, map[string]*gin.RouterGroup{})
		info.Routes(s.mc, router)
	}
	return s, nil
}

// token is the service's token, read from the file when not given.
func (s *Service) token() (string, error) {
	if s.opts.TokenSource != nil {
		return s.opts.TokenSource()
	}
	if s.opts.Token != "" {
		return s.opts.Token, nil
	}
	b, err := os.ReadFile(s.opts.TokenFile)
	if err != nil {
		return "", fmt.Errorf("service token: %w", err)
	}
	return strings.TrimSpace(string(b)), nil
}

// Manifest describes the building block (§5.4, docs/services/protocol.md).
func (s *Service) Manifest() wire.Manifest {
	info := s.info
	m := wire.Manifest{ID: info.ID, Name: info.Name, Version: info.Version, Subscribes: info.Subscribes, UI: info.UI}
	sample := info.New()
	for _, c := range info.Provides {
		a, ok := wire.Lookup(c.String())
		if !ok {
			continue
		}
		o := wire.Offer{Capability: c.String()}
		if srv := a.Server(sample); srv != nil {
			o.Operations = srv.Operations()
		}
		m.Provides = append(m.Provides, o)
	}
	for _, sl := range info.Slots {
		m.Slots = append(m.Slots, wire.Slot{Name: sl.Name, Needs: sl.Needs.String(), Multiple: sl.Multiple, Optional: sl.Optional})
	}
	for _, j := range info.Jobs {
		m.Jobs = append(m.Jobs, wire.Job{Name: j.Name, Schedule: j.Schedule, Timeout: durationString(j.Timeout), Module: true})
	}
	// Instance jobs and their schedules come with the answer to the
	// configuration; their names are known only from an instance.
	if jp, ok := sample.(companion.JobProvider); ok {
		for _, j := range jp.Jobs() {
			m.Jobs = append(m.Jobs, wire.Job{Name: j.Name, Timeout: durationString(j.Timeout)})
		}
	}
	t := reflect.TypeOf(sample)
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t != nil && t.Kind() == reflect.Struct {
		for _, f := range blocks.ConfigFields(t) {
			m.Config = append(m.Config, wire.ConfigField{Name: f.Name, Type: f.Type, Secret: f.Secret, Help: f.Help})
		}
	}
	m.Requests.Volume = true
	return m
}

func durationString(d time.Duration) string {
	if d == 0 {
		return ""
	}
	return d.String()
}

// ServeHTTP answers the protocol. Everything but /health and /manifest
// must carry the core's signature.
func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/health":
		s.mu.Lock()
		v := s.version
		s.mu.Unlock()
		writeJSON(w, map[string]string{"status": "ok", "configVersion": v})
		return
	case "/manifest":
		writeJSON(w, s.Manifest())
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, wire.MaxBody+1))
	if err != nil || len(body) > wire.MaxBody {
		wire.WriteError(w, &wire.Error{Code: wire.CodeBadRequest, Message: "body unreadable or too large"})
		return
	}
	token, err := s.token()
	if err != nil {
		wire.WriteError(w, &wire.Error{Code: wire.CodeUnavailable, Message: err.Error()})
		return
	}
	if err := wire.Verify(r, body, []byte(token), s.opts.Now()); err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		writeJSON(w, map[string]*wire.Error{"error": {Code: "unauthorized", Message: err.Error()}})
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	name := r.Header.Get(wire.InstanceHeader)
	p := r.URL.Path
	switch {
	case p == "/lifecycle/configure" && r.Method == http.MethodPost:
		s.configure(w, body)
	case (p == "/lifecycle/pause" || p == "/lifecycle/resume") && r.Method == http.MethodPost:
		w.WriteHeader(http.StatusNoContent)
	case strings.HasPrefix(p, "/capabilities/") && r.Method == http.MethodPost:
		rest := strings.TrimPrefix(p, "/capabilities/")
		i := strings.LastIndexByte(rest, '/')
		if i < 0 {
			wire.WriteError(w, &wire.Error{Code: wire.CodeBadRequest, Message: "POST /capabilities/<capability>/v<n>/<operation>"})
			return
		}
		in := s.instance(name)
		if in == nil {
			wire.WriteError(w, &wire.Error{Code: "unknown_instance", Message: fmt.Sprintf("%s has no instance %q", s.info.ID, name)})
			return
		}
		srv := in.servers[rest[:i]]
		if srv == nil {
			wire.WriteError(w, &wire.Error{Code: wire.CodeUnsupported, Message: fmt.Sprintf("%s does not offer %s", s.info.ID, rest[:i])})
			return
		}
		srv.Serve(w, r, rest[i+1:])
	case strings.HasPrefix(p, "/jobs/") && r.Method == http.MethodPost:
		s.runJob(w, r, name, strings.TrimPrefix(p, "/jobs/"))
	case p == "/events" && r.Method == http.MethodPost:
		s.event(w, r, name, body)
	case strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/public/") || p == "/public":
		s.router.ServeHTTP(w, r)
	default:
		http.NotFound(w, r)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func (s *Service) instance(name string) *inst {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.instances[name]
}

// configureBody is what the core sends (core/services.go).
type configureBody struct {
	Version   string `json:"version"`
	Service   string `json:"service"`
	PublicURL string `json:"publicUrl"`
	Instances map[string]struct {
		Config  map[string]any      `json:"config"`
		Slots   map[string][]string `json:"slots"`
		Targets map[string][]string `json:"targets"`
	} `json:"instances"`
}

// configure builds the instances whose configuration changed, starts
// them and stops the ones they replace or that went away.
func (s *Service) configure(w http.ResponseWriter, raw []byte) {
	var body configureBody
	if err := json.Unmarshal(raw, &body); err != nil {
		wire.WriteError(w, &wire.Error{Code: wire.CodeBadRequest, Message: err.Error()})
		return
	}
	s.mc.setPublicURL(body.PublicURL)
	errs := map[string]string{}
	jobs := map[string][]wire.Job{}

	s.mu.Lock()
	first := !s.started
	s.started = true
	s.mu.Unlock()
	if first {
		if s.info.Migrate != nil {
			if err := s.info.Migrate(s.mc); err != nil {
				wire.WriteError(w, &wire.Error{Code: wire.CodeInternal, Message: "migrate: " + err.Error()})
				return
			}
		}
	}

	next := map[string]*inst{}
	var started, stopped []*inst
	names := make([]string, 0, len(body.Instances))
	for name := range body.Instances {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		ci := body.Instances[name]
		key, _ := json.Marshal(ci)
		if old := s.instance(name); old != nil && old.raw == string(key) {
			next[name] = old
			continue
		}
		in, err := s.build(name, ci.Config, ci.Slots, ci.Targets)
		if err != nil {
			errs[name] = err.Error()
			continue
		}
		in.raw = string(key)
		next[name] = in
		started = append(started, in)
	}
	s.mu.Lock()
	for name, old := range s.instances {
		if next[name] != old {
			stopped = append(stopped, old)
		}
	}
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, old := range stopped {
		if st, ok := old.value.(companion.Stopper); ok {
			if err := st.Stop(ctx); err != nil {
				s.log.Printf("%s: stop: %v", old.name, err)
			}
		}
	}
	for _, in := range started {
		if st, ok := in.value.(companion.Starter); ok {
			if err := st.Start(); err != nil {
				errs[in.name] = "start: " + err.Error()
				delete(next, in.name)
			}
		}
	}
	for name, in := range next {
		for _, j := range in.jobs {
			jobs[name] = append(jobs[name], wire.Job{Name: j.Name, Schedule: j.Schedule, Timeout: durationString(j.Timeout)})
		}
		sort.Slice(jobs[name], func(a, b int) bool { return jobs[name][a].Name < jobs[name][b].Name })
	}
	s.mu.Lock()
	s.instances = next
	s.version = body.Version
	s.mu.Unlock()
	if first && s.info.Start != nil {
		if err := s.info.Start(s.mc); err != nil {
			s.log.Printf("start: %v", err)
		}
	}
	writeJSON(w, map[string]any{"errors": errs, "jobs": jobs})
}

// build creates, connects, provisions and validates one instance.
func (s *Service) build(name string, config map[string]any, slots, targets map[string][]string) (*inst, error) {
	v := s.info.New()
	if err := blocks.DecodeConfig(v, config); err != nil {
		return nil, err
	}
	err := blocks.InjectSlots(v, slots, func(id string) (any, error) {
		for _, sl := range s.info.Slots {
			if contains(slots[sl.Name], id) {
				return s.mc.slotClient(name, sl, id, targets[id])
			}
		}
		return nil, fmt.Errorf("%s is not connected", id)
	})
	if err != nil {
		return nil, err
	}
	ictx := &instCtx{modCtx: s.mc, name: name, slots: slots, targets: targets}
	if p, ok := v.(companion.Provisioner); ok {
		if err := p.Provision(ictx); err != nil {
			return nil, err
		}
	}
	if val, ok := v.(companion.Validator); ok {
		if err := val.Validate(); err != nil {
			return nil, err
		}
	}
	in := &inst{name: name, value: v, servers: map[string]*wire.Server{}, jobs: map[string]companion.Job{}, slots: slots, targets: targets}
	for _, c := range s.info.Provides {
		if a, ok := wire.Lookup(c.String()); ok {
			if srv := a.Server(v); srv != nil {
				in.servers[c.String()] = srv
			}
		}
	}
	if jp, ok := v.(companion.JobProvider); ok {
		for _, j := range jp.Jobs() {
			in.jobs[j.Name] = j
		}
	}
	return in, nil
}

// runJob runs an instance job, or a module job without an instance.
func (s *Service) runJob(w http.ResponseWriter, r *http.Request, name, job string) {
	var run func(ctx context.Context) error
	timeout := time.Hour
	if name == "" {
		for _, j := range s.info.Jobs {
			if j.Name == job && j.RunModule != nil {
				run = func(ctx context.Context) error { return j.RunModule(ctx, s.mc) }
				if j.Timeout > 0 {
					timeout = j.Timeout
				}
			}
		}
	} else if in := s.instance(name); in != nil {
		if j, ok := in.jobs[job]; ok && j.Run != nil {
			run = j.Run
			if j.Timeout > 0 {
				timeout = j.Timeout
			}
		}
	}
	if run == nil {
		wire.WriteError(w, &wire.Error{Code: "not_found", Message: fmt.Sprintf("no job %s for %q", job, name)})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	if err := safely(func() error { return run(ctx) }); err != nil {
		wire.WriteError(w, &wire.Error{Code: wire.CodeInternal, Message: err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// event delivers an event once, however often the core sends it.
func (s *Service) event(w http.ResponseWriter, r *http.Request, name string, body []byte) {
	var ev companion.Event
	if err := json.Unmarshal(body, &ev); err != nil || ev.ID == "" {
		wire.WriteError(w, &wire.Error{Code: wire.CodeBadRequest, Message: "an event with an id"})
		return
	}
	s.mu.Lock()
	_, dup := s.seen[ev.ID]
	if !dup {
		s.seen[ev.ID] = s.opts.Now()
		if len(s.seen) > 10000 {
			for id, t := range s.seen {
				if s.opts.Now().Sub(t) > 24*time.Hour {
					delete(s.seen, id)
				}
			}
		}
	}
	s.mu.Unlock()
	if dup {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var err error
	if in := s.instance(name); in != nil {
		if h, ok := in.value.(companion.EventHandler); ok {
			err = safely(func() error { return h.HandleEvent(r.Context(), ev) })
		}
	} else if s.info.OnEvent != nil {
		err = safely(func() error { return s.info.OnEvent(s.mc, ev) })
	}
	if err != nil {
		// Delivered again later; forget it so the retry counts.
		s.mu.Lock()
		delete(s.seen, ev.ID)
		s.mu.Unlock()
		wire.WriteError(w, &wire.Error{Code: wire.CodeInternal, Message: err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Close stops every instance and the building block.
func (s *Service) Close(ctx context.Context) {
	s.mu.Lock()
	insts := s.instances
	s.instances = map[string]*inst{}
	s.mu.Unlock()
	for _, in := range insts {
		if st, ok := in.value.(companion.Stopper); ok {
			st.Stop(ctx)
		}
	}
	if s.info.Stop != nil {
		s.info.Stop(s.mc)
	}
	s.mc.close()
}

func safely(f func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return f()
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
