// Package routing is the routing module: geo.routing/v1 on OSRM (driving)
// and Transitous (rail), the services the website's browsers used to ask
// themselves. It sends at most one request per second, the OSRM demo
// server's limit, whoever asks.
package routing

import (
	"context"
	"sync"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/capability/geo"
	"github.com/LNA-DEV/HomePageCompanion/companion"
)

func init() {
	companion.Register(companion.Info{
		ID:       "routing",
		Name:     "Routing",
		New:      func() companion.Instance { return new(Service) },
		Provides: []companion.Capability{geo.RoutingV1},
	})
}

// Point is a WGS84 coordinate.
type Point struct {
	Lat float64
	Lng float64
}

// Service is one routing setup.
type Service struct {
	OSRMURL       string `json:"osrmUrl"`
	TransitousURL string `json:"transitousUrl"`
	// MinInterval between two requests (1s when 0).
	MinInterval companion.Duration `json:"minInterval"`

	router *Router
	mu     sync.Mutex
	last   time.Time
}

func (s *Service) Provision(ctx companion.Context) error {
	s.router = NewRouter(s.OSRMURL, s.TransitousURL, ctx.HTTP())
	if s.MinInterval == 0 {
		s.MinInterval = companion.Duration(time.Second)
	}
	return nil
}

// Routed reports whether a mode gets a computed track. Flights are drawn
// straight.
func Routed(mode string) bool { return mode != "" && mode != "flight" }

// Route computes the track: car → OSRM driving; train → Transitous rail per
// segment, else the road corridor.
func (s *Service) Route(ctx context.Context, mode string, points []geo.Point) (geo.Route, error) {
	if !Routed(mode) {
		return geo.Route{}, geo.ErrUnroutable
	}
	pts := make([]Point, len(points))
	for i, p := range points {
		pts[i] = Point{p.Lat, p.Lng}
	}
	track, source, err := s.router.Route(mode, pts, s.wait)
	if err != nil {
		return geo.Route{}, err
	}
	return geo.Route{Points: track, Source: source}, nil
}

// wait blocks until MinInterval has passed since the previous request.
func (s *Service) wait() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d := time.Until(s.last.Add(s.MinInterval.D())); d > 0 {
		time.Sleep(d)
	}
	s.last = time.Now()
}

var _ geo.Router = (*Service)(nil)
