package core

import (
	"archive/tar"
	"context"
	"errors"
	"io"

	"github.com/LNA-DEV/HomePageCompanion/companion"
	"github.com/gin-gonic/gin"
)

// serviceManifest is what a service declares about itself (GET /manifest).
type serviceManifest struct {
	ID       string
	Provides []companion.Capability
	Slots    []companion.Slot
}

// serviceInstance is an instance of a service: a container the core talks
// to over HTTP.
type serviceInstance struct {
	block    string
	name     string
	manifest *serviceManifest
}

var errServicesUnsupported = errors.New("services are not supported by this core")

func (s *serviceInstance) offers(cap companion.Capability) bool {
	if s == nil || s.manifest == nil {
		return false
	}
	for _, p := range s.manifest.Provides {
		if p.Name == cap.Name && p.Major == cap.Major {
			return true
		}
	}
	return false
}

func (s *serviceInstance) adapter(cap companion.Capability) (any, error) {
	return nil, errServicesUnsupported
}

func (s *serviceInstance) granted(eventType string) bool { return false }

func (s *serviceInstance) deliver(ctx context.Context, ev companion.Event) error {
	return errServicesUnsupported
}

func (s *serviceInstance) provideNames() []string {
	var out []string
	if s.manifest != nil {
		for _, p := range s.manifest.Provides {
			out = append(out, p.String())
		}
	}
	return out
}

// serviceInfo is the Info of a service block, from its manifest.
func serviceInfo(block string) companion.Info {
	info := companion.Info{ID: block}
	if m := manifestFor(block); m != nil {
		info.Provides, info.Slots = m.Provides, m.Slots
	}
	return info
}

func manifestFor(block string) *serviceManifest { return nil }

// serviceManager starts and talks to the services.
type serviceManager struct{ c *Core }

func newServiceManager(c *Core) *serviceManager { return &serviceManager{c: c} }

func (m *serviceManager) instance(p *planned, ctx *instCtx) (*serviceInstance, error) {
	return nil, errServicesUnsupported
}

func (m *serviceManager) stopAll(ctx context.Context)                 {}
func (m *serviceManager) routes(r *gin.Engine, admin gin.HandlerFunc) {}
func (m *serviceManager) isService(id string) bool                    { return false }
func (m *serviceManager) serveUI(g *gin.Context, id, file string)     {}
func (m *serviceManager) gauges()                                     {}
func (m *serviceManager) backupVolumes(ctx context.Context, tw *tar.Writer) ([]string, error) {
	return nil, nil
}
func (m *serviceManager) restoreVolume(ctx context.Context, name string, r io.Reader) error {
	return nil
}
