// Package conf reads companion.yaml: the runtime config of the core, its
// modules and services. ${VAR} and ${VAR:-default} are expanded from the
// environment (and .env) first, as config.yaml always was.
package conf

import (
	"bytes"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/LNA-DEV/HomePageCompanion/lib/envfile"
	"gopkg.in/yaml.v3"
)

// File is companion.yaml.
type File struct {
	Core     Core                    `yaml:"core"`
	Modules  map[string]ModuleBlock  `yaml:"modules"`
	Services map[string]ServiceBlock `yaml:"services"`
}

// Core is the core's own block.
type Core struct {
	// Domain admits https://<domain> and its subdomains through CORS.
	Domain string `yaml:"domain"`
	// ExtraOrigins are admitted exactly, never as patterns (the onion site).
	ExtraOrigins []string `yaml:"extraOrigins"`
	// AdminKey opens the admin UI and the admin API.
	AdminKey string `yaml:"adminKey"`
	// PublicURL is how browsers reach the companion.
	PublicURL string `yaml:"publicUrl"`
	Listen    string `yaml:"listen"` // ":8080" when empty
	// ServicesListen is where services reach the core: its API for them
	// (/core/v1/) and the egress proxy. ":3128" when empty; only opened
	// when a service is configured, and never published to the host.
	ServicesListen string `yaml:"servicesListen"`
	// Database and Blobs name the storage instances; when empty, the only
	// configured or compiled-in one is used.
	Database string  `yaml:"database"`
	Blobs    string  `yaml:"blobs"`
	Backups  Backups `yaml:"backups"`
	// CacheMB caps every building block's cache directory (1024 when 0).
	CacheMB int `yaml:"cacheMB"`
}

// Backups configures the scheduled backup.
type Backups struct {
	Schedule string `yaml:"schedule"` // cron with seconds; empty: no scheduled backup
	Keep     int    `yaml:"keep"`     // how many to keep (7 when 0)
}

// ModuleBlock is one building block's part of modules:. Config is the
// "default" instance; Instances are named ones.
type ModuleBlock struct {
	Config    map[string]any            `yaml:"config"`
	Instances map[string]map[string]any `yaml:"instances"`
}

// ServiceBlock is one service.
type ServiceBlock struct {
	// From is the source: oci://<image>, git+https://<repo>@<ref> or
	// path:<dir> (docs/concepts/modular-architecture.md §5.6).
	From string `yaml:"from"`
	// Dockerfile is the Dockerfile within a git or path source, when it is
	// not <source>/Dockerfile.
	Dockerfile string `yaml:"dockerfile"`
	// URL is where the core reaches the service, when it does not run
	// next to the companion under its block name (development).
	URL       string                    `yaml:"url"`
	Grants    Grants                    `yaml:"grants"`
	Config    map[string]any            `yaml:"config"`
	Instances map[string]map[string]any `yaml:"instances"`
	Limits    ServiceLimits             `yaml:"limits"`
	Env       map[string]string         `yaml:"env"`
}

// Grants are the rights a service gets.
type Grants struct {
	Egress []string `yaml:"egress"`
	Events []string `yaml:"events"`
	Public bool     `yaml:"public"`
	Volume *bool    `yaml:"volume"`
}

// ServiceLimits cap a service container.
type ServiceLimits struct {
	MemoryMB int     `yaml:"memoryMB"`
	PIDs     int     `yaml:"pids"`
	CPUs     float64 `yaml:"cpus"`
}

// Instance is one configured instance, in a stable order.
type Instance struct {
	Block  string // building block ID
	Name   string
	Values map[string]any
	// Service is set for instances of a service.
	Service bool
}

// ID is "<block>:<name>".
func (i Instance) ID() string { return i.Block + ":" + i.Name }

// DefaultInstance is the name the "config:" block gets.
const DefaultInstance = "default"

// Load reads the config at path, after loading the .env files next to the
// data directory.
func Load(path string, envFiles ...string) (*File, []byte, error) {
	for _, f := range envFiles {
		envfile.Load(f)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	f, err := Parse(raw)
	return f, raw, err
}

// Parse expands placeholders and decodes the config. Unknown keys in the
// core block are an error, so a typo does not silently do nothing.
func Parse(raw []byte) (*File, error) {
	expanded := envfile.Expand(raw)
	dec := yaml.NewDecoder(bytes.NewReader(expanded))
	dec.KnownFields(true)
	var f File
	if err := dec.Decode(&f); err != nil {
		if err.Error() == "EOF" {
			return &File{}, nil
		}
		return nil, fmt.Errorf("companion.yaml: %w", err)
	}
	return &f, nil
}

// Instances lists every configured instance of modules and services,
// sorted by block and name.
func (f *File) Instances() []Instance {
	var out []Instance
	add := func(block string, service bool, cfg map[string]any, named map[string]map[string]any) {
		if cfg != nil || len(named) == 0 {
			if cfg == nil {
				cfg = map[string]any{}
			}
			if _, clash := named[DefaultInstance]; !clash {
				out = append(out, Instance{Block: block, Name: DefaultInstance, Values: cfg, Service: service})
			}
		}
		for name, v := range named {
			if v == nil {
				v = map[string]any{}
			}
			out = append(out, Instance{Block: block, Name: name, Values: v, Service: service})
		}
	}
	for block, m := range f.Modules {
		add(block, false, m.Config, m.Instances)
	}
	for block, s := range f.Services {
		add(block, true, s.Config, s.Instances)
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].Block != out[b].Block {
			return out[a].Block < out[b].Block
		}
		return out[a].Name < out[b].Name
	})
	return out
}

// HasBlock reports whether the block is configured as a module or service.
func (f *File) HasBlock(id string) bool {
	_, m := f.Modules[id]
	_, s := f.Services[id]
	return m || s
}

// Ref resolves an instance reference: "rss:photos", or "gbif" for
// gbif:default.
func Ref(ref string) (block, name string) {
	ref = strings.TrimSpace(ref)
	if i := strings.IndexByte(ref, ':'); i >= 0 {
		return ref[:i], ref[i+1:]
	}
	return ref, DefaultInstance
}
