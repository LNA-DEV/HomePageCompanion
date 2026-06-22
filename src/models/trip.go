package models

import "time"

// Trip is the local source-of-truth for a travel "trip overview": a title and
// an ordered list of stops. Slug is the stable public identifier used in
// /api/trips/:slug. Only Published trips are exposed on the public (anonymous)
// endpoints.
//
// Most header stats are derived from the stops rather than stored: daysElapsed
// (from the first stop's start date to today), cities (stop count), countries
// (distinct union of stop + transport-leg countries) and distance (sum of the
// per-leg distances) — see computeStats in the admin package. DaysTotal is the
// one manual stat: the planned trip length, which can't be derived because the
// final destination may not be decided yet.
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

// TripStop is one city/place on a trip. Position fixes the order within the
// trip. StopKey is the stable per-stop id used in the public payload (the HTML
// keys its map markers and feed cards on it); it defaults to a slug of Name
// when left empty. StartDate/EndDate are date-only strings (YYYY-MM-DD); the
// public endpoint formats them into the display range the HTML renders (an
// empty EndDate yields an open-ended range like "Jun 11–"). The TransportIn*
// fields describe the leg taken to reach this stop (empty TransportMode means
// no leg, e.g. the first stop). Photos with Kind "stop" are the stop's gallery;
// Kind "transport" belong to the leg.
type TripStop struct {
	ID                uint        `gorm:"primaryKey" json:"id"`
	TripID            uint        `gorm:"index" json:"tripId"`
	Position          int         `json:"position"`
	StopKey           string      `json:"stopKey"`
	Name              string      `json:"name"`
	StartDate         string      `json:"startDate"`
	EndDate           string      `json:"endDate"`
	Lat               float64     `json:"lat"`
	Lng               float64     `json:"lng"`
	Status            string      `json:"status"`
	Note              string      `gorm:"type:text" json:"note"`
	Country           string      `json:"country"`
	TransportMode     string      `json:"transportMode"`
	TransportLabel    string      `json:"transportLabel"`
	TransportDuration string      `json:"transportDuration"`
	// DistanceKm is the length of the transport leg *into* this stop (nil for
	// the first stop / stops with no leg). The trip's total distance is the sum
	// of these.
	DistanceKm *int `json:"distanceKm,omitempty"`
	// TransportCountries are the countries this leg passes through (optional,
	// e.g. a train that crosses a border). They count toward the trip's
	// distinct-country total alongside each stop's own Country.
	TransportCountries []string    `gorm:"serializer:json" json:"transportCountries"`
	// TransportWaypoints are intermediate points along this leg's route (ordered
	// from the previous stop toward this one). They are not stops/stays — pure
	// geometry the public renderer draws the trail through so the track follows
	// the real path instead of a straight line.
	TransportWaypoints []Waypoint  `gorm:"serializer:json" json:"transportWaypoints"`
	Photos             []TripPhoto `gorm:"foreignKey:StopID" json:"photos"`
	CreatedAt         time.Time   `json:"createdAt"`
	UpdatedAt         time.Time   `json:"updatedAt"`
}

// Waypoint is one intermediate point along a transport leg's route — pure
// geometry so the public renderer can draw an accurate trail, not a real stop.
type Waypoint struct {
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
}

// TripPhoto is a single photo attached to a stop. Kind distinguishes a stop
// gallery photo ("stop") from a transport-leg photo ("transport"). URL points
// at a previously-uploaded media file served by /api/trips/media/:sha. Tint is
// an optional fallback colour used by the frontend when no image is present yet
// (matches the HTML's placeholder tints for upcoming stops).
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
