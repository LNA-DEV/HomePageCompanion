package wire

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/companion"
)

// Manifest is what a service says about itself, at GET /manifest and
// printed to stdout when started with COMPANION_MANIFEST=print. hpc apply
// records it in companion.lock; the core plans with that copy, so a
// service that is down does not change what the companion is.
type Manifest struct {
	ID      string `json:"id"` // the name it suggests for its block
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
	// Port the service listens on in its container (8080 when 0).
	Port int `json:"port,omitempty"`

	Provides   []Offer       `json:"provides,omitempty"`
	Slots      []Slot        `json:"slots,omitempty"`
	Jobs       []Job         `json:"jobs,omitempty"`
	Subscribes []string      `json:"subscribes,omitempty"` // event types it wants; delivered only if granted
	Requests   Requests      `json:"requests,omitempty"`   // the rights it asks for
	Config     []ConfigField `json:"config,omitempty"`
	UI         *companion.UI `json:"ui,omitempty"`
}

// Offer is an offered capability and its operations (the required ones
// and whichever optional ones the service implements).
type Offer struct {
	Capability string   `json:"capability"` // "social.publisher/v1"
	Operations []string `json:"operations,omitempty"`
}

// Slot is a place where the service needs a capability; the operator
// connects instances to it in companion.yaml.
type Slot struct {
	Name     string `json:"name"`
	Needs    string `json:"needs"`
	Multiple bool   `json:"multiple,omitempty"`
	Optional bool   `json:"optional,omitempty"`
}

// Job is run for every instance (POST /jobs/<name> with the instance
// header), or once for the service when Module is set.
type Job struct {
	Name string `json:"name"`
	// Schedule is a cron spec with seconds; ScheduleFrom names an instance
	// config field holding one instead. Neither: by hand only.
	Schedule     string `json:"schedule,omitempty"`
	ScheduleFrom string `json:"scheduleFrom,omitempty"`
	Timeout      string `json:"timeout,omitempty"` // "10m"; one hour when empty
	// Module jobs run once for the service, without an instance.
	Module bool `json:"module,omitempty"`
}

// Requests are the rights a service asks for. What it gets is what the
// operator grants in companion.yaml and confirmed with hpc apply.
type Requests struct {
	Egress []string `json:"egress,omitempty"`
	Public bool     `json:"public,omitempty"`
	Volume bool     `json:"volume,omitempty"`
}

// ConfigField is one instance config field, for the settings view.
type ConfigField struct {
	Name   string `json:"name"`
	Type   string `json:"type"` // string | number | boolean | duration | list | object
	Secret bool   `json:"secret,omitempty"`
	Help   string `json:"help,omitempty"`
}

// Check reports what makes a manifest unusable for this core: capabilities
// without an adapter, duplicate names.
func (m *Manifest) Check() error {
	var errs []string
	seen := map[string]bool{}
	for _, o := range m.Provides {
		a, ok := Lookup(o.Capability)
		if !ok {
			errs = append(errs, fmt.Sprintf("offers %s, which this core does not know", o.Capability))
			continue
		}
		for _, r := range a.Required {
			if len(o.Operations) > 0 && !Has(o.Operations, r) {
				errs = append(errs, fmt.Sprintf("offers %s without its operation %s", o.Capability, r))
			}
		}
	}
	for _, s := range m.Slots {
		if seen["slot:"+s.Name] {
			errs = append(errs, "slot "+s.Name+" twice")
		}
		seen["slot:"+s.Name] = true
		if _, ok := Lookup(s.Needs); !ok {
			errs = append(errs, fmt.Sprintf("slot %s needs %s, which this core does not know", s.Name, s.Needs))
		}
	}
	for _, j := range m.Jobs {
		if j.Name == "" || strings.ContainsAny(j.Name, "/ ") {
			errs = append(errs, fmt.Sprintf("job name %q", j.Name))
		}
		if seen["job:"+j.Name] {
			errs = append(errs, "job "+j.Name+" twice")
		}
		seen["job:"+j.Name] = true
		if j.Timeout != "" {
			if _, err := time.ParseDuration(j.Timeout); err != nil {
				errs = append(errs, fmt.Sprintf("job %s: timeout %q", j.Name, j.Timeout))
			}
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// Operations returns the offered operations of a capability: the listed
// ones, or the required ones when the manifest lists none.
func (m *Manifest) Operations(capability string) []string {
	for _, o := range m.Provides {
		if o.Capability == capability {
			if len(o.Operations) > 0 {
				return o.Operations
			}
			a, _ := Lookup(capability)
			return a.Required
		}
	}
	return nil
}

// Grants are the rights a service has (docs/concepts/modular-architecture.md
// §5.8). Offered capabilities and connected slots need no grant.
type Grants struct {
	Egress []string `json:"egress,omitempty"` // hosts, "*.example.com" for subdomains, "host:port"
	Events []string `json:"events,omitempty"`
	Public bool     `json:"public,omitempty"`
	Volume bool     `json:"volume,omitempty"`
}

// Beyond reports what g has that confirmed does not: the rights an
// update asks for anew.
func (g Grants) Beyond(confirmed Grants) []string {
	var out []string
	for _, h := range g.Egress {
		if !contains(confirmed.Egress, h) {
			out = append(out, "egress to "+h)
		}
	}
	for _, e := range g.Events {
		if !contains(confirmed.Events, e) {
			out = append(out, "event "+e)
		}
	}
	if g.Public && !confirmed.Public {
		out = append(out, "public routes")
	}
	if g.Volume && !confirmed.Volume {
		out = append(out, "a volume")
	}
	return out
}

// Intersect is what both allow.
func (g Grants) Intersect(o Grants) Grants {
	out := Grants{Public: g.Public && o.Public, Volume: g.Volume && o.Volume}
	for _, h := range g.Egress {
		if contains(o.Egress, h) {
			out.Egress = append(out.Egress, h)
		}
	}
	for _, e := range g.Events {
		if contains(o.Events, e) {
			out.Events = append(out.Events, e)
		}
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// LockFile is companion.lock's name, next to companion.yaml.
const LockFile = "companion.lock"

// Lock is companion.lock: what hpc apply started, pinned. The core reads
// the manifests and the confirmed grants from it.
type Lock struct {
	Version  int                      `json:"version"`
	Services map[string]LockedService `json:"services"`
}

// LockedService is one applied service.
type LockedService struct {
	From   string `json:"from"`
	Image  string `json:"image,omitempty"`  // with its digest
	Commit string `json:"commit,omitempty"` // for git sources
	URL    string `json:"url,omitempty"`    // where the core reaches it, when not the default
	// Manifest as the service reported it at apply time.
	Manifest Manifest `json:"manifest"`
	// Grants the operator confirmed.
	Grants    Grants    `json:"grants"`
	AppliedAt time.Time `json:"appliedAt"`
}

// ReadLock reads a lock file; a missing one is an empty lock.
func ReadLock(path string) (*Lock, []byte, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Lock{Version: 1, Services: map[string]LockedService{}}, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var l Lock
	if err := json.Unmarshal(raw, &l); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	if l.Services == nil {
		l.Services = map[string]LockedService{}
	}
	return &l, raw, nil
}

// Write writes the lock, services sorted.
func (l *Lock) Write(path string) error {
	if l.Version == 0 {
		l.Version = 1
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(l); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Names lists the locked services, sorted.
func (l *Lock) Names() []string {
	var out []string
	for n := range l.Services {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
