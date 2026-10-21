// Package core is the companion's core: it reads companion.yaml, starts the
// configured modules and services, connects them, runs their jobs and
// events, and serves their APIs and UIs behind one gateway. A build's main
// package imports the modules and calls Main.
package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/capability/storage"
	"github.com/LNA-DEV/HomePageCompanion/companion"
	"github.com/LNA-DEV/HomePageCompanion/core/internal/conf"
	"github.com/LNA-DEV/HomePageCompanion/internal/blocks"
	"gorm.io/gorm"
)

// Options configure a Core.
type Options struct {
	ConfigPath string // companion.yaml
	DataDir    string // data/
	Shell      fs.FS  // the built admin shell; nil serves a placeholder
	// Listen overrides core.listen, ServicesListen core.servicesListen.
	Listen         string
	ServicesListen string
	// LockPath is companion.lock (default: next to companion.yaml).
	LockPath string
	// Logs is where the core's own log lines go (default: the standard
	// logger's output).
	Logs io.Writer
}

// Core is a running companion.
type Core struct {
	opts Options

	conf    atomic.Pointer[conf.File]
	confRaw []byte

	mu        sync.RWMutex
	instances map[string]*instance
	blocks    map[string]*block
	mcs       map[string]*modCtx // every building block's context, kept across reloads
	order     []string

	sql    storage.SQL
	sqlID  string
	blob   storage.Blob
	blobID string
	db     *gorm.DB // the core's own area

	secrets    *secretStore
	events     *bus
	jobs       *scheduler
	metrics    *metrics
	temp       *tempStore
	sessions   *sessions
	flows      oauthFlows
	services   *serviceManager
	httpClient *http.Client

	handler atomic.Pointer[http.Handler]
	log     *log.Logger
	started time.Time
	version string
}

// block is a configured building block.
type block struct {
	info    companion.Info
	mc      *modCtx
	started bool
}

// New prepares a core; Start starts it.
func New(opts Options) *Core {
	if opts.ConfigPath == "" {
		opts.ConfigPath = "companion.yaml"
	}
	if opts.DataDir == "" {
		opts.DataDir = "data"
	}
	c := &Core{
		opts:      opts,
		instances: map[string]*instance{},
		blocks:    map[string]*block{},
		mcs:       map[string]*modCtx{},
		started:   time.Now(),
		version:   buildVersion(),
	}
	out := opts.Logs
	if out == nil {
		out = log.Writer()
	}
	c.log = log.New(out, "core ", log.LstdFlags)
	c.conf.Store(&conf.File{})
	c.metrics = newMetrics()
	c.httpClient = newHTTPClient(c.userAgent)
	c.events = newBus(c)
	c.jobs = newScheduler(c)
	c.secrets = newSecretStore(c)
	c.temp = &tempStore{c: c}
	c.sessions = newSessions(c)
	c.services = newServiceManager(c)
	companion.SetLocalFetcher(c.localFetch)
	return c
}

func (c *Core) cfg() *conf.File { return c.conf.Load() }

func (c *Core) logOut() io.Writer { return c.log.Writer() }

func (c *Core) userAgent() string {
	contact := "https://github.com/LNA-DEV/HomePageCompanion"
	if d := c.cfg().Core.Domain; d != "" {
		contact = "https://" + d
	}
	return "HomePageCompanion/" + c.version + " (+" + contact + ")"
}

// envFiles are the .env files read before companion.yaml, as config.yaml's
// loader always did: data/.env, then ./.env; the environment wins.
func (c *Core) envFiles() []string {
	return []string{filepath.Join(c.opts.DataDir, ".env"), ".env"}
}

// Start reads the config and starts every instance. On error, whatever
// started is stopped again.
func (c *Core) Start(ctx context.Context) error {
	next, err := c.provision(ctx)
	if err != nil {
		return err
	}
	c.markInterruptedRuns()
	for _, id := range c.order {
		if err := c.startInstance(next[id]); err != nil {
			c.stopAll(ctx, next, nil)
			return err
		}
	}
	if err := c.startBlocks(); err != nil {
		c.stopAll(ctx, next, nil)
		return err
	}
	c.startCoreJobs()
	c.events.start()
	c.jobs.start()
	if err := c.rebuildGateway(); err != nil {
		return err
	}
	if err := c.services.listen(); err != nil {
		return err
	}
	c.log.Printf("started %d instance(s) of %d building block(s)", len(next), len(c.blocks))
	return nil
}

// Prepare provisions every instance and runs the modules' Migrate, and
// nothing else: no Start, no jobs, no events, no HTTP. The instances count
// as running, so ModuleContext.Instances lists them. For "companion
// migrate" and tools that work on the modules' data; Stop ends it.
func (c *Core) Prepare(ctx context.Context) error {
	next, err := c.provision(ctx)
	if err != nil {
		return err
	}
	c.mu.Lock()
	for _, in := range next {
		in.running = true
	}
	var blocks []*block
	for _, b := range c.blocks {
		blocks = append(blocks, b)
	}
	c.mu.Unlock()
	sort.Slice(blocks, func(i, j int) bool {
		if blocks[i].info.Order != blocks[j].info.Order {
			return blocks[i].info.Order < blocks[j].info.Order
		}
		return blocks[i].info.ID < blocks[j].info.ID
	})
	for _, b := range blocks {
		if b.info.Migrate != nil {
			if err := safely(func() error { return b.info.Migrate(b.mc) }); err != nil {
				c.stopAll(ctx, next, nil)
				return fmt.Errorf("%s: migrate: %w", b.info.ID, err)
			}
		}
	}
	return nil
}

// provision reads the config and builds every instance, storage first.
func (c *Core) provision(ctx context.Context) (map[string]*instance, error) {
	if err := os.MkdirAll(c.opts.DataDir, 0o755); err != nil {
		return nil, err
	}
	f, raw, err := conf.Load(c.opts.ConfigPath, c.envFiles()...)
	if err != nil {
		return nil, err
	}
	lock, lockRaw, err := c.services.readLock()
	if err != nil {
		return nil, err
	}
	ps, err := c.plan(f, lock)
	if err != nil {
		return nil, err
	}
	c.conf.Store(f)
	c.confRaw = raw
	c.services.lockRaw = lockRaw

	next := map[string]*instance{}
	for _, p := range ps {
		in, err := c.create(p, next)
		if err != nil {
			c.stopAll(ctx, next, nil)
			return nil, err
		}
		next[p.id] = in
		if err := c.afterStorage(in); err != nil {
			c.stopAll(ctx, next, nil)
			return nil, err
		}
	}

	c.mu.Lock()
	c.instances = next
	c.order = idsOf(ps)
	for _, p := range ps {
		if _, ok := c.blocks[p.block]; !ok {
			c.blocks[p.block] = &block{info: p.info, mc: next[p.id].ctx.modCtx}
		}
	}
	c.mu.Unlock()
	return next, nil
}

// create builds, provisions and validates one instance. Its slots resolve
// against next (the instances of the new generation) first, then against
// the running ones.
func (c *Core) create(p *planned, next map[string]*instance) (*instance, error) {
	in := &instance{id: p.id, block: p.block, name: p.name, info: p.info, values: p.values, slots: p.slots}
	mc := c.moduleCtx(p.info)
	in.ctx = &instCtx{modCtx: mc, name: p.name}
	if p.service != nil {
		svc, err := c.services.instance(p, func(id string) *instance {
			if t := next[id]; t != nil {
				return t
			}
			c.mu.RLock()
			defer c.mu.RUnlock()
			return c.instances[id]
		})
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p.id, err)
		}
		in.service, in.value = svc, svc
		return in, nil
	}
	if p.info.New == nil {
		return nil, fmt.Errorf("%s: module has no New", p.id)
	}
	in.value = p.info.New()
	if err := blocks.DecodeConfig(in.value, p.values); err != nil {
		return nil, fmt.Errorf("%s: %w", p.id, err)
	}
	err := blocks.InjectSlots(in.value, p.slots, func(id string) (any, error) {
		target := next[id]
		if target == nil {
			c.mu.RLock()
			target = c.instances[id]
			c.mu.RUnlock()
		}
		if target == nil {
			return nil, fmt.Errorf("no instance %s", id)
		}
		for _, s := range p.info.Slots {
			for _, sid := range p.slots[s.Name] {
				if sid == id {
					impl, err := target.provide(s.Needs)
					if err != nil {
						return nil, err
					}
					return c.measured(s.Needs, id, impl), nil
				}
			}
		}
		return nil, fmt.Errorf("%s is not connected", id)
	})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p.id, err)
	}
	for _, cap := range p.info.Provides {
		if cap.Type != nil && !reflect.TypeOf(in.value).Implements(cap.Type) {
			return nil, fmt.Errorf("%s: %T does not implement %s", p.id, in.value, cap)
		}
	}
	if err := safely(func() error {
		if pr, ok := in.value.(companion.Provisioner); ok {
			if err := pr.Provision(in.ctx); err != nil {
				return err
			}
		}
		if v, ok := in.value.(companion.Validator); ok {
			return v.Validate()
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("%s: %w", p.id, err)
	}
	return in, nil
}

// moduleCtx returns the building block's context, shared by its instances
// across reloads.
func (c *Core) moduleCtx(info companion.Info) *modCtx {
	c.mu.Lock()
	defer c.mu.Unlock()
	if mc, ok := c.mcs[info.ID]; ok {
		return mc
	}
	mc := newModCtx(c, info)
	c.mcs[info.ID] = mc
	return mc
}

// afterStorage wires the storage modules into the core as soon as they
// exist, before anything else is provisioned.
func (c *Core) afterStorage(in *instance) error {
	f := c.cfg()
	if c.sql == nil && provides(in.info, storage.SQLV1) {
		if want := f.Core.Database; want == "" || refID(want) == in.id {
			sql, err := in.provide(storage.SQLV1)
			if err != nil {
				return err
			}
			c.sql, c.sqlID = sql.(storage.SQL), in.id
			db, err := c.sql.Open("core")
			if err != nil {
				return fmt.Errorf("core database: %w", err)
			}
			c.db = db
			if err := c.migrateCore(); err != nil {
				return err
			}
		}
	}
	if c.blob == nil && provides(in.info, storage.BlobV1) {
		if want := f.Core.Blobs; want == "" || refID(want) == in.id {
			b, err := in.provide(storage.BlobV1)
			if err != nil {
				return err
			}
			c.blob, c.blobID = b.(storage.Blob), in.id
		}
	}
	return nil
}

func refID(ref string) string {
	b, n := conf.Ref(ref)
	return companion.InstanceID(b, n)
}

// migrateCore creates the core's own tables.
func (c *Core) migrateCore() error {
	return c.db.AutoMigrate(&eventRow{}, &deliveryRow{}, &jobRun{}, &secretRow{}, &sessionRow{}, &metaRow{})
}

func (c *Core) startInstance(in *instance) error {
	if s, ok := in.value.(companion.Starter); ok {
		if err := safely(s.Start); err != nil {
			in.err = err.Error()
			return fmt.Errorf("%s: start: %w", in.id, err)
		}
	}
	c.mu.Lock()
	in.running, in.startedAt, in.err = true, time.Now(), ""
	c.mu.Unlock()
	if jp, ok := in.value.(companion.JobProvider); ok {
		for _, j := range jp.Jobs() {
			c.jobs.add(in.id, j)
		}
	}
	return nil
}

// startBlocks runs the module-level Start and registers module jobs for
// blocks that are not started yet.
func (c *Core) startBlocks() error {
	c.mu.RLock()
	var todo []*block
	for _, b := range c.blocks {
		if !b.started {
			todo = append(todo, b)
		}
	}
	c.mu.RUnlock()
	sort.Slice(todo, func(i, j int) bool {
		if todo[i].info.Order != todo[j].info.Order {
			return todo[i].info.Order < todo[j].info.Order
		}
		return todo[i].info.ID < todo[j].info.ID
	})
	for _, b := range todo {
		if b.info.Migrate != nil {
			if err := safely(func() error { return b.info.Migrate(b.mc) }); err != nil {
				return fmt.Errorf("%s: migrate: %w", b.info.ID, err)
			}
		}
		if b.info.Start != nil {
			if err := safely(func() error { return b.info.Start(b.mc) }); err != nil {
				return fmt.Errorf("%s: start: %w", b.info.ID, err)
			}
		}
		for _, j := range b.info.Jobs {
			c.jobs.addModule(b.info.ID, j, b.mc)
		}
		c.mu.Lock()
		b.started = true
		c.mu.Unlock()
	}
	return nil
}

// Stop stops every instance in reverse start order.
func (c *Core) Stop(ctx context.Context) {
	c.jobs.stop()
	c.events.stop()
	c.mu.RLock()
	var blocks []*block
	for _, b := range c.blocks {
		blocks = append(blocks, b)
	}
	c.mu.RUnlock()
	for _, b := range blocks {
		if b.started && b.info.Stop != nil {
			if err := safely(func() error { return b.info.Stop(b.mc) }); err != nil {
				c.log.Printf("%s: stop: %v", b.info.ID, err)
			}
		}
	}
	c.stopAll(ctx, c.instances, c.order)
	c.services.stopAll(ctx)
	if c.sql != nil {
		c.sql.Close()
	}
}

// stopAll stops the instances, last started first.
func (c *Core) stopAll(ctx context.Context, set map[string]*instance, order []string) {
	if order == nil {
		for id := range set {
			order = append(order, id)
		}
		sort.Strings(order)
	}
	for i := len(order) - 1; i >= 0; i-- {
		in := set[order[i]]
		if in == nil {
			continue
		}
		c.stopInstance(ctx, in)
	}
}

func (c *Core) stopInstance(ctx context.Context, in *instance) {
	c.jobs.removeOwner(in.id)
	if s, ok := in.value.(companion.Stopper); ok {
		sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		if err := safely(func() error { return s.Stop(sctx) }); err != nil {
			c.log.Printf("%s: stop: %v", in.id, err)
		}
		cancel()
	}
	c.mu.Lock()
	in.running = false
	c.mu.Unlock()
}

// Reload reads the config again and swaps the instances whose config or
// connections changed, together with every instance connected to them. A
// new instance that fails leaves the old ones running.
func (c *Core) Reload(ctx context.Context) error {
	f, raw, err := conf.Load(c.opts.ConfigPath, c.envFiles()...)
	if err != nil {
		return err
	}
	lock, lockRaw, err := c.services.readLock()
	if err != nil {
		return err
	}
	if string(raw) == string(c.confRaw) && string(lockRaw) == string(c.services.lockRaw) {
		return nil
	}
	ps, err := c.plan(f, lock)
	if err != nil {
		return err
	}
	if f.Core.Database != c.cfg().Core.Database || f.Core.Blobs != c.cfg().Core.Blobs {
		return errors.New("core.database or core.blobs changed: restart the companion")
	}

	c.mu.RLock()
	old := c.instances
	c.mu.RUnlock()

	changed := map[string]bool{}
	for _, p := range ps {
		in, ok := old[p.id]
		if !ok || !in.sameAs(p) {
			changed[p.id] = true
			continue
		}
		for _, d := range p.deps() {
			if changed[d] {
				changed[p.id] = true
			}
		}
	}
	for _, p := range ps {
		if changed[p.id] && (p.id == c.sqlID || p.id == c.blobID) {
			return fmt.Errorf("%s changed: restart the companion", p.id)
		}
	}

	next := map[string]*instance{}
	var created []*instance
	for _, p := range ps {
		if !changed[p.id] {
			next[p.id] = old[p.id]
			continue
		}
		in, err := c.create(p, next)
		if err != nil {
			for _, n := range created {
				c.stopInstance(ctx, n)
			}
			return err
		}
		next[p.id] = in
		created = append(created, in)
	}

	// Swap: stop what goes away or is replaced, then start the new ones.
	newBlocks := map[string]bool{}
	for _, p := range ps {
		newBlocks[p.block] = true
	}
	var stopOrder []string
	for i := len(c.order) - 1; i >= 0; i-- {
		id := c.order[i]
		if n, ok := next[id]; !ok || n != old[id] {
			stopOrder = append(stopOrder, id)
		}
	}
	for _, id := range stopOrder {
		c.stopInstance(ctx, old[id])
	}
	c.conf.Store(f)
	c.confRaw = raw
	c.services.lockRaw = lockRaw
	c.mu.Lock()
	c.instances = next
	c.order = idsOf(ps)
	blocksChanged := false
	for id, b := range c.blocks {
		if !newBlocks[id] {
			blocksChanged = true
			delete(c.blocks, id)
			if b.started && b.info.Stop != nil {
				go func() { _ = safely(func() error { return b.info.Stop(b.mc) }) }()
			}
			c.jobs.removeOwner(id)
		}
	}
	for _, p := range ps {
		if b, ok := c.blocks[p.block]; !ok {
			c.blocks[p.block] = &block{info: p.info, mc: next[p.id].ctx.modCtx}
			blocksChanged = true
		} else if p.service != nil && !reflect.DeepEqual(b.info, p.info) {
			// A service's manifest changed with hpc apply.
			b.info = p.info
			blocksChanged = true
		}
	}
	c.mu.Unlock()
	for _, in := range created {
		if err := c.startInstance(in); err != nil {
			c.log.Printf("reload: %v", err)
		}
	}
	if err := c.startBlocks(); err != nil {
		c.log.Printf("reload: %v", err)
	}
	if blocksChanged {
		if err := c.rebuildGateway(); err != nil {
			return err
		}
	}
	if err := c.services.listen(); err != nil {
		return err
	}
	c.log.Printf("reloaded: %d instance(s) replaced", len(created))
	return nil
}

func idsOf(ps []*planned) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.id
	}
	return out
}

// safely runs f and turns a panic into an error, so one building block
// cannot take the companion down.
func safely(f func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v\n%s", r, debug.Stack())
		}
	}()
	return f()
}

// Instance returns a running instance's value; for tests and tools.
func (c *Core) Instance(id string) (companion.Instance, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	in, ok := c.instances[id]
	if !ok || !in.running {
		return nil, false
	}
	return in.value, true
}

// DB returns the core's own database area; for tests and tools.
func (c *Core) DB() *gorm.DB { return c.db }

// Handler is the gateway.
func (c *Core) Handler() http.Handler { return c }

// ServeHTTP serves the current gateway, which a reload may replace.
func (c *Core) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := c.handler.Load()
	if h == nil {
		http.Error(w, "starting", http.StatusServiceUnavailable)
		return
	}
	(*h).ServeHTTP(w, r)
}

// linkedVersion is stamped by hpc build (-ldflags -X).
var linkedVersion string

func buildVersion() string {
	if linkedVersion != "" {
		return strings.TrimPrefix(linkedVersion, "v")
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, d := range bi.Deps {
			if d.Path == "github.com/LNA-DEV/HomePageCompanion" && d.Version != "" && d.Version != "(devel)" && !strings.HasPrefix(d.Version, "v0.0.0-00010101000000") {
				return strings.TrimPrefix(d.Version, "v")
			}
		}
		if bi.Main.Path == "github.com/LNA-DEV/HomePageCompanion" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			return strings.TrimPrefix(bi.Main.Version, "v")
		}
	}
	return "dev"
}

// ModuleContext returns a building block's context; for tests and tools.
func (c *Core) ModuleContext(id string) (companion.ModuleContext, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	mc, ok := c.mcs[id]
	return mc, ok
}
