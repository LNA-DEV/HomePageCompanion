package trips

import "time"

// Trip is a travel overview: a title and an ordered list of stops. Slug is
// the public id in /api/trips/:slug; only Published trips are public.
//
// Most header stats are derived from the stops (see computeStats).
// DaysTotal is the one manual stat: the planned length, which can't be
// derived because the final destination may not be decided yet.
type Trip struct {
	ID        uint       `gorm:"primaryKey" json:"id"`
	Slug      string     `gorm:"uniqueIndex" json:"slug"`
	Title     string     `json:"title"`
	Published bool       `gorm:"index" json:"published"`
	DaysTotal *int       `json:"daysTotal,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
	UpdatedAt time.Time  `json:"updatedAt"`
	Stops     []TripStop `gorm:"foreignKey:TripID" json:"stops"`
}

// TripStop is one place on a trip. Position fixes the order. StopKey is the
// stable per-stop id in the public payload (the website keys its markers and
// cards on it), a slug of Name when empty. StartDate/EndDate are YYYY-MM-DD;
// an empty EndDate is an open-ended stay. The Transport* fields describe the
// leg into this stop (no TransportMode: no leg, e.g. the first stop). Photos
// of Kind "stop" are the stop's gallery, "transport" the leg's.
type TripStop struct {
	ID                uint    `gorm:"primaryKey" json:"id"`
	TripID            uint    `gorm:"index" json:"tripId"`
	Position          int     `json:"position"`
	StopKey           string  `json:"stopKey"`
	Name              string  `json:"name"`
	StartDate         string  `json:"startDate"`
	EndDate           string  `json:"endDate"`
	Lat               float64 `json:"lat"`
	Lng               float64 `json:"lng"`
	Status            string  `json:"status"`
	Note              string  `gorm:"type:text" json:"note"`
	Country           string  `json:"country"`
	TransportMode     string  `json:"transportMode"`
	TransportLabel    string  `json:"transportLabel"`
	TransportDuration string  `json:"transportDuration"`
	// DistanceKm is the length of the leg into this stop; the trip's
	// distance is their sum.
	DistanceKm *int `json:"distanceKm,omitempty"`
	// TransportCountries are the countries the leg passes through; they
	// count toward the trip's countries.
	TransportCountries []string `gorm:"serializer:json" json:"transportCountries"`
	// TransportWaypoints are via-points of the leg, from the previous stop
	// toward this one: geometry only, not stays.
	TransportWaypoints []Waypoint  `gorm:"serializer:json" json:"transportWaypoints"`
	Photos             []TripPhoto `gorm:"foreignKey:StopID" json:"photos"`
	CreatedAt          time.Time   `json:"createdAt"`
	UpdatedAt          time.Time   `json:"updatedAt"`
}

// Waypoint is a via-point of a leg.
type Waypoint struct {
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
}

// TripPhoto is a photo of a stop ("stop") or of its leg ("transport"). URL
// points at an upload served by /api/trips/media/:sha. Tint is the
// placeholder colour the website shows while there is no image yet.
type TripPhoto struct {
	ID       uint   `gorm:"primaryKey" json:"id"`
	StopID   uint   `gorm:"index" json:"stopId"`
	Kind     string `gorm:"index" json:"kind"`
	Position int    `json:"position"`
	URL      string `json:"url"`
	Caption  string `json:"caption"`
	Alt      string `json:"alt,omitempty"`
	Tint     string `json:"tint,omitempty"`
}

// RouteGeometry is the computed track of one leg, keyed by what it depends
// on (Key). It is a table of its own because saving a trip re-creates every
// stop; a field there would be recomputed on every edit.
//
// Status "ok" carries Points, "failed" carries Error and is retried by the
// next backfill. The public payload only carries "ok" geometry.
type RouteGeometry struct {
	Key       string       `gorm:"primaryKey" json:"key"`
	Mode      string       `json:"mode"`
	Status    string       `gorm:"index" json:"status"`
	Source    string       `json:"source"`                        // osrm | transitous
	Points    [][2]float64 `gorm:"serializer:json" json:"points"` // [lat, lng]
	Error     string       `json:"error,omitempty"`
	FetchedAt time.Time    `json:"fetchedAt"`
}
