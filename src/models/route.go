package models

import "time"

// RouteGeometry is the computed track of one trip leg, keyed by what the
// route depends on: the mode and the ordered points A → via-points → B (see
// routing.Key). It is a table of its own rather than a field on TripStop
// because UpdateTrip deletes and re-creates every stop on each save; a field
// there would be lost — and recomputed — on every edit.
//
// Status "ok" carries Points; "failed" carries Error and is retried by the
// next backfill. The public trip endpoint only ever sends "ok" geometry.
type RouteGeometry struct {
	Key       string       `gorm:"primaryKey" json:"key"`
	Mode      string       `json:"mode"`
	Status    string       `gorm:"index" json:"status"`
	Source    string       `json:"source"`                        // osrm | transitous
	Points    [][2]float64 `gorm:"serializer:json" json:"points"` // [lat, lng]
	Error     string       `json:"error,omitempty"`
	FetchedAt time.Time    `json:"fetchedAt"`
}
