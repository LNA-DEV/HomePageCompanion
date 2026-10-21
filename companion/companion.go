// Package companion is the SDK for building blocks of the companion: modules
// compiled into the core, and (through package service) services in their
// own container. A module registers itself in init:
//
//	func init() {
//		companion.Register(companion.Info{
//			ID:  "autoupload",
//			New: func() companion.Instance { return new(Upload) },
//			Slots: []companion.Slot{
//				{Name: "source", Needs: feed.SourceV1},
//				{Name: "target", Needs: social.PublisherV1},
//			},
//		})
//	}
//
// The core reads companion.yaml, creates one Instance per configured instance,
// fills its config fields and slots, and runs Provision, Validate and Start.
// See docs/concepts/modular-architecture.md §5.
package companion

import (
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Capability is a versioned interface a building block offers or needs, such
// as social.publisher/v1. Type is the Go interface behind it; the core checks
// that a provider implements it. Optional are interfaces with the optional
// operations (social.Lister's listPosts): a provider may implement them.
type Capability struct {
	Name     string
	Major    int
	Type     reflect.Type
	Optional []reflect.Type
}

// NewCapability declares a capability backed by the Go interface T, with
// the optional operations of the interfaces Optional[…]() names.
func NewCapability[T any](name string, major int, optional ...reflect.Type) Capability {
	return Capability{Name: name, Major: major, Type: reflect.TypeFor[T](), Optional: optional}
}

// Optional names an interface of optional operations for NewCapability.
func Optional[T any]() reflect.Type { return reflect.TypeFor[T]() }

// String is the capability's full name, e.g. "social.publisher/v1".
func (c Capability) String() string { return fmt.Sprintf("%s/v%d", c.Name, c.Major) }

// Slot is a place where a building block needs a capability. The operator
// connects an instance to it in companion.yaml ("target: mastodon:main").
type Slot struct {
	Name     string
	Needs    Capability
	Multiple bool // a list of instances
	Optional bool // may stay unconnected
}

// Info describes a building block. ID is its name in companion.yaml and in
// URLs ("/api/b/<ID>/"). A building block is usable once at least one of its
// instances is configured, or its module-level part is (Config without
// instances).
type Info struct {
	ID      string
	Name    string // human-readable, defaults to ID
	Version string

	// New returns an empty instance; the core fills its config fields from
	// the instance's block in companion.yaml (json tags) and its slot fields
	// (tag `companion:"slot=<name>"`).
	New func() Instance

	// Provides lists the capabilities every instance offers. The core
	// refuses to start if an instance does not implement one of them.
	Provides []Capability
	Slots    []Slot

	// Routes registers the module's HTTP API under /api/b/<ID>/, once, for
	// all its instances. Aliases are legacy public prefixes the module also
	// answers under (e.g. "/api/tiles/basemap"); two modules claiming the
	// same alias stop the core.
	Routes  func(mc ModuleContext, r *Router)
	Aliases []string

	// Jobs are module-level jobs; instances add their own through
	// JobProvider.
	Jobs []Job

	// Subscribes lists the event types delivered to OnEvent (module level)
	// and to every instance implementing EventHandler.
	Subscribes []string
	OnEvent    func(mc ModuleContext, ev Event) error

	// Migrate creates or updates the module's tables. The core runs it once
	// per start, before Start, and before Import, without starting
	// anything else.
	Migrate func(mc ModuleContext) error

	// Start and Stop run once for the module, after all instances started
	// and before they stop.
	Start func(mc ModuleContext) error
	Stop  func(mc ModuleContext) error

	// Import moves the module's part of a pre-module installation over
	// (hpc migrate). See Legacy.
	Import func(mc ModuleContext, l *Legacy) error

	// BlobArea overrides the key prefix of the module's area in the blob
	// store (default: the ID). The basemap keeps "maps/basemap" so its
	// existing objects stay where they are.
	BlobArea string

	// UI is the module's admin UI: an entry.js registering its custom
	// elements, plus the manifest of where the shell places them.
	UI *UI

	// Order breaks ties when the core starts modules that do not depend on
	// each other; lower first. Storage modules use negative values.
	Order int
}

// DisplayName is Name, or ID when Name is empty.
func (i Info) DisplayName() string {
	if i.Name != "" {
		return i.Name
	}
	return i.ID
}

var (
	registryMu sync.RWMutex
	registry   = map[string]Info{}
)

// Register adds a building block. It panics on a duplicate or invalid ID, so
// a broken build fails at startup, not later.
func Register(info Info) {
	if err := validID(info.ID); err != nil {
		panic(fmt.Sprintf("companion.Register: %v", err))
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, dup := registry[info.ID]; dup {
		panic(fmt.Sprintf("companion.Register: %q registered twice", info.ID))
	}
	registry[info.ID] = info
}

// Registered returns every registered building block, sorted by ID.
func Registered() []Info {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]Info, 0, len(registry))
	for _, i := range registry {
		out = append(out, i)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ID < out[b].ID })
	return out
}

// Lookup returns the registered building block with this ID.
func Lookup(id string) (Info, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	i, ok := registry[id]
	return i, ok
}

// Unregister removes a building block. Only for tests.
func Unregister(id string) {
	registryMu.Lock()
	defer registryMu.Unlock()
	delete(registry, id)
}

func validID(id string) error {
	if id == "" {
		return fmt.Errorf("empty ID")
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '_') {
			return fmt.Errorf("ID %q: only a-z, 0-9, '.', '-' and '_'", id)
		}
	}
	if strings.HasPrefix(id, ".") || strings.HasSuffix(id, ".") {
		return fmt.Errorf("ID %q: no leading or trailing dot", id)
	}
	return nil
}

// InstanceID is the address of an instance, "<block>:<name>".
func InstanceID(block, name string) string { return block + ":" + name }

// SplitInstanceID splits "<block>:<name>"; a bare "<block>" means its
// "default" instance.
func SplitInstanceID(ref string) (block, name string) {
	if i := strings.IndexByte(ref, ':'); i >= 0 {
		return ref[:i], ref[i+1:]
	}
	return ref, DefaultInstance
}

// DefaultInstance is the name of the instance a module's "config:" block in
// companion.yaml creates.
const DefaultInstance = "default"

// Duration is a config value like "10m" or "7d" (days are allowed on top of
// time.ParseDuration's units).
type Duration time.Duration

// UnmarshalJSON accepts "10m", "36h", "7d" or a number of seconds.
func (d *Duration) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*d = 0
		return nil
	}
	if n, err := strconv.ParseFloat(s, 64); err == nil {
		*d = Duration(time.Duration(n * float64(time.Second)))
		return nil
	}
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.ParseFloat(days, 64)
		if err != nil {
			return fmt.Errorf("duration %q: %w", s, err)
		}
		*d = Duration(time.Duration(n * 24 * float64(time.Hour)))
		return nil
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

// MarshalJSON writes the duration as Go formats it.
func (d Duration) MarshalJSON() ([]byte, error) {
	return []byte(`"` + time.Duration(d).String() + `"`), nil
}

// D is the value as a time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }
