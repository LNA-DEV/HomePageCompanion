// Package geo defines geo.routing/v1: the track of a leg between points.
package geo

import (
	"context"

	"github.com/LNA-DEV/HomePageCompanion/companion"
)

// Router computes routes.
type Router interface {
	// Route returns the track through the points, [lat, lng] pairs, and the
	// service that computed it. Modes the router cannot route return
	// ErrUnroutable.
	Route(ctx context.Context, mode string, points []Point) (Route, error)
}

var RoutingV1 = companion.NewCapability[Router]("geo.routing", 1)

// Point is a coordinate.
type Point struct {
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
}

// Route is a computed track.
type Route struct {
	Points [][2]float64 `json:"points"`
	Source string       `json:"source"`
}

// ErrUnroutable is returned for a mode the router does not route.
type unroutable struct{}

func (unroutable) Error() string { return "geo: mode is not routed" }

var ErrUnroutable error = unroutable{}
