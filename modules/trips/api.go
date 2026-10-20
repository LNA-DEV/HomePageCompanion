package trips

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/capability/media"
	"github.com/LNA-DEV/HomePageCompanion/companion"
	"github.com/LNA-DEV/HomePageCompanion/lib/imageresize"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const maxImageBytes = 30 << 20 // 30 MiB, so large phone photos (e.g. 50MP) reach the transform

// mediaPath is where the website reads uploaded images.
const mediaPath = "/api/trips/media/"

// imageLimits transforms every decodable upload: re-encoded as JPEG to strip
// EXIF/IPTC/XMP (GPS included), the long edge capped at 4096px. No byte cap,
// so quality stays at 95.
var imageLimits = media.Limits{MaxLongEdge: 4096, ForceReencode: true}

// api serves the routes; the instance may be missing (no trips configured)
// or replaced by a reload, so it is looked up per request.
type api struct{ mc companion.ModuleContext }

func (a api) db() *gorm.DB { return a.mc.DB() }

func (a api) trips() *Trips {
	for _, in := range a.mc.Instances() {
		if t, ok := in.Instance.(*Trips); ok {
			return t
		}
	}
	return nil
}

func routes(mc companion.ModuleContext, r *companion.Router) {
	a := api{mc}
	adm := r.Admin
	adm.GET("/trips", a.listAdmin)
	adm.POST("/trips", a.create)
	adm.GET("/trips/:id", a.getAdmin)
	adm.PUT("/trips/:id", a.update)
	adm.DELETE("/trips/:id", a.delete)
	adm.POST("/upload", a.upload)
	adm.GET("/routes", a.routeStatus)
	adm.POST("/routes/backfill", a.backfill)
	adm.POST("/routes/:key/recompute", a.recompute)

	pub := r.Alias("/api/trips")
	pub.GET("", a.listPublic)
	pub.GET("/:slug", a.getPublic)
	pub.GET("/media/:sha", a.serveMedia)
}

// ---------------------------------------------------------------------------
// Request / response shapes

type tripPhotoInput struct {
	URL     string `json:"url"`
	Caption string `json:"caption"`
	Alt     string `json:"alt"`
	Tint    string `json:"tint"`
}

type tripStopInput struct {
	StopKey            string           `json:"stopKey"`
	Name               string           `json:"name"`
	StartDate          string           `json:"startDate"`
	EndDate            string           `json:"endDate"`
	Lat                float64          `json:"lat"`
	Lng                float64          `json:"lng"`
	Status             string           `json:"status"`
	Note               string           `json:"note"`
	Country            string           `json:"country"`
	TransportMode      string           `json:"transportMode"`
	TransportLabel     string           `json:"transportLabel"`
	TransportDuration  string           `json:"transportDuration"`
	DistanceKm         *int             `json:"distanceKm"`         // length of the leg into this stop
	TransportCountries []string         `json:"transportCountries"` // countries the leg passes through
	TransportWaypoints []Waypoint       `json:"transportWaypoints"` // ordered via-points of the route
	Photos             []tripPhotoInput `json:"photos"`             // stop gallery
	TransportPhotos    []tripPhotoInput `json:"transportPhotos"`    // transport-leg gallery
}

type updateTripInput struct {
	Slug      string          `json:"slug"`
	Title     string          `json:"title"`
	Published bool            `json:"published"`
	DaysTotal *int            `json:"daysTotal"` // manual: planned trip length
	Stops     []tripStopInput `json:"stops"`
}

type createTripInput struct {
	Title string `json:"title"`
	Slug  string `json:"slug"`
}

// AdminStopView splits a stop's photos into the two galleries the editor
// edits independently.
type AdminStopView struct {
	StopKey            string           `json:"stopKey"`
	Name               string           `json:"name"`
	StartDate          string           `json:"startDate"`
	EndDate            string           `json:"endDate"`
	Lat                float64          `json:"lat"`
	Lng                float64          `json:"lng"`
	Status             string           `json:"status"`
	Note               string           `json:"note"`
	Country            string           `json:"country"`
	TransportMode      string           `json:"transportMode"`
	TransportLabel     string           `json:"transportLabel"`
	TransportDuration  string           `json:"transportDuration"`
	DistanceKm         *int             `json:"distanceKm"`
	TransportCountries []string         `json:"transportCountries"`
	TransportWaypoints []Waypoint       `json:"transportWaypoints"`
	Photos             []tripPhotoInput `json:"photos"`
	TransportPhotos    []tripPhotoInput `json:"transportPhotos"`
	// Route is the computed track of the leg into this stop; absent for the
	// first stop, flights and stops without a transport mode.
	Route *AdminRouteView `json:"route,omitempty"`
}

// AdminRouteView tells the editor whether a leg's route is computed. Status
// is ok | failed | pending (queued, or not computed yet).
type AdminRouteView struct {
	Key    string `json:"key"`
	Status string `json:"status"`
	Source string `json:"source,omitempty"`
	Error  string `json:"error,omitempty"`
}

// AdminTripView is the editor's trip.
type AdminTripView struct {
	ID        uint            `json:"id"`
	Slug      string          `json:"slug"`
	Title     string          `json:"title"`
	Published bool            `json:"published"`
	DaysTotal *int            `json:"daysTotal"`
	Stops     []AdminStopView `json:"stops"`
}

type adminTripListItem struct {
	ID        uint   `json:"id"`
	Slug      string `json:"slug"`
	Title     string `json:"title"`
	Published bool   `json:"published"`
	StopCount int64  `json:"stopCount"`
}

// ---------------------------------------------------------------------------
// Admin handlers

func (a api) listAdmin(c *gin.Context) {
	var trips []Trip
	a.db().Order("created_at DESC").Find(&trips)

	out := make([]adminTripListItem, 0, len(trips))
	for _, t := range trips {
		var count int64
		a.db().Model(&TripStop{}).Where("trip_id = ?", t.ID).Count(&count)
		out = append(out, adminTripListItem{
			ID: t.ID, Slug: t.Slug, Title: t.Title, Published: t.Published, StopCount: count,
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

func (a api) create(c *gin.Context) {
	var req createTripInput
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON"})
		return
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "title is required"})
		return
	}
	base := slugify(req.Slug)
	if base == "" {
		base = slugify(title)
	}
	if base == "" {
		base = "trip"
	}
	trip := Trip{Slug: a.uniqueSlug(base), Title: title}
	if err := a.db().Create(&trip).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not create trip"})
		return
	}
	c.JSON(http.StatusCreated, a.hydrateAdmin(trip.ID))
}

func (a api) getAdmin(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	var trip Trip
	if err := a.db().First(&trip, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "trip not found"})
		return
	}
	c.JSON(http.StatusOK, a.hydrateAdmin(trip.ID))
}

func (a api) update(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	var req updateTripInput
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON"})
		return
	}

	var trip Trip
	if err := a.db().First(&trip, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "trip not found"})
		return
	}

	// A blank slug keeps the existing one; another must not collide with a
	// different trip.
	slug := slugify(req.Slug)
	if slug == "" {
		slug = trip.Slug
	}
	var clash int64
	a.db().Model(&Trip{}).Where("slug = ? AND id <> ?", slug, trip.ID).Count(&clash)
	if clash > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "another trip already uses that slug"})
		return
	}

	if strings.TrimSpace(req.Title) != "" {
		trip.Title = strings.TrimSpace(req.Title)
	}
	trip.Slug = slug
	trip.Published = req.Published
	trip.DaysTotal = req.DaysTotal

	// Stops and photos are replaced wholesale inside a transaction: simpler
	// than diffing associations. Images of removed photos stay in the blob
	// store (as in the microblog).
	err := a.db().Transaction(func(tx *gorm.DB) error {
		if err := tx.Omit("Stops").Save(&trip).Error; err != nil {
			return err
		}
		if err := deleteStops(tx, trip.ID); err != nil {
			return err
		}
		for si, s := range req.Stops {
			key := slugify(s.StopKey)
			if key == "" {
				key = slugify(s.Name)
			}
			status := strings.TrimSpace(s.Status)
			if status == "" {
				status = "visited"
			}
			stop := TripStop{
				TripID:             trip.ID,
				Position:           si,
				StopKey:            key,
				Name:               strings.TrimSpace(s.Name),
				StartDate:          strings.TrimSpace(s.StartDate),
				EndDate:            strings.TrimSpace(s.EndDate),
				Lat:                s.Lat,
				Lng:                s.Lng,
				Status:             status,
				Note:               s.Note,
				Country:            strings.TrimSpace(s.Country),
				TransportMode:      strings.TrimSpace(s.TransportMode),
				TransportLabel:     s.TransportLabel,
				TransportDuration:  s.TransportDuration,
				DistanceKm:         s.DistanceKm,
				TransportCountries: cleanCountries(s.TransportCountries),
				TransportWaypoints: cleanWaypoints(s.TransportWaypoints),
			}
			if err := tx.Create(&stop).Error; err != nil {
				return err
			}
			if err := createPhotos(tx, stop.ID, "stop", s.Photos); err != nil {
				return err
			}
			if err := createPhotos(tx, stop.ID, "transport", s.TransportPhotos); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not save trip"})
		return
	}
	// New or changed legs are routed in the background; the editor sees
	// them as "pending" until then.
	if t := a.trips(); t != nil {
		t.EnqueueTrip(trip.ID)
	}
	c.JSON(http.StatusOK, a.hydrateAdmin(trip.ID))
}

func (a api) delete(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	err := a.db().Transaction(func(tx *gorm.DB) error {
		if err := deleteStops(tx, id); err != nil {
			return err
		}
		return tx.Delete(&Trip{}, id).Error
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not delete trip"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "deleted"})
}

func deleteStops(tx *gorm.DB, tripID uint) error {
	var stopIDs []uint
	tx.Model(&TripStop{}).Where("trip_id = ?", tripID).Pluck("id", &stopIDs)
	if len(stopIDs) > 0 {
		if err := tx.Where("stop_id IN ?", stopIDs).Delete(&TripPhoto{}).Error; err != nil {
			return err
		}
	}
	return tx.Where("trip_id = ?", tripID).Delete(&TripStop{}).Error
}

// upload stores an image in the blob store as <sha256>.<ext> and returns
// the public URL the editor attaches to a photo.
func (a api) upload(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxImageBytes)
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "expected multipart field 'file'"})
		return
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "read failed"})
		return
	}
	if len(data) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "empty file"})
		return
	}

	ext := filepath.Ext(header.Filename)
	if ext == "" {
		ext = ".bin"
	}
	// Strip metadata and bound the size by re-encoding. An image that can't
	// be decoded (e.g. HEIC) keeps its bytes and extension.
	if processed, perr := a.resize(c, data); perr == nil {
		data = processed
		ext = ".jpg"
	}

	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:]) + ext
	ct := mime.TypeByExtension(ext)
	if _, err := a.mc.Blobs().Stat(c.Request.Context(), name); err != nil {
		if err := a.mc.Blobs().Put(c.Request.Context(), name, bytes.NewReader(data), int64(len(data)), ct); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "write failed"})
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"url": mediaPath + name, "size": len(data)})
}

// resize goes through the media slot when one is connected.
func (a api) resize(c *gin.Context, data []byte) ([]byte, error) {
	if t := a.trips(); t != nil && t.Media != nil {
		return t.Media.Resize(c.Request.Context(), data, imageLimits)
	}
	return imageresize.PrepareForTarget(data, imageresize.Limits{MaxLongEdge: imageLimits.MaxLongEdge, ForceReencode: imageLimits.ForceReencode})
}

func (a api) routeStatus(c *gin.Context) {
	var ok, failed int64
	a.db().Model(&RouteGeometry{}).Where("status = ?", StatusOK).Count(&ok)
	a.db().Model(&RouteGeometry{}).Where("status = ?", StatusFailed).Count(&failed)
	pending, router := 0, false
	if t := a.trips(); t != nil {
		pending, router = t.Pending(), t.Routing != nil
	}
	c.JSON(http.StatusOK, gin.H{"ok": ok, "failed": failed, "pending": pending, "router": router})
}

// backfill queues every leg without geometry and every failed one;
// ?force=1 recomputes all of them.
func (a api) backfill(c *gin.Context) {
	t := a.trips()
	if t == nil || t.Routing == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "no router connected to the routing slot"})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"queued": t.EnqueueAll(c.Query("force") == "1")})
}

func (a api) recompute(c *gin.Context) {
	t := a.trips()
	if t == nil || t.Routing == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "no router connected to the routing slot"})
		return
	}
	if !t.Recompute(c.Param("key")) {
		c.JSON(http.StatusNotFound, gin.H{"error": "no current leg has that key"})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"status": "queued"})
}

// ---------------------------------------------------------------------------
// Public handlers

type publicPhoto struct {
	Src  string `json:"src,omitempty"`
	Cap  string `json:"cap"`
	Tint string `json:"tint,omitempty"`
}

type publicTransport struct {
	Mode       string     `json:"mode"`
	Label      string     `json:"label"`
	Duration   string     `json:"duration"`
	DistanceKm *int       `json:"distanceKm,omitempty"`
	Countries  []string   `json:"countries,omitempty"`
	Waypoints  []Waypoint `json:"waypoints,omitempty"`
	// Geometry is the computed track [[lat, lng], …]; absent until the leg
	// is routed, and for flights. travel.js then draws a straight line
	// through the waypoints.
	Geometry [][2]float64  `json:"geometry,omitempty"`
	Photos   []publicPhoto `json:"photos"`
}

type publicStop struct {
	ID          string           `json:"id"`
	Name        string           `json:"name"`
	Dates       string           `json:"dates"`
	Lat         float64          `json:"lat"`
	Lng         float64          `json:"lng"`
	Status      string           `json:"status"`
	Note        string           `json:"note"`
	Country     string           `json:"country,omitempty"`
	TransportIn *publicTransport `json:"transportIn"`
	Photos      []publicPhoto    `json:"photos"`
}

// publicStats: cities and countries are always present; daysElapsed,
// daysTotal and distanceKm may be unknown.
type publicStats struct {
	DaysElapsed *int `json:"daysElapsed,omitempty"`
	DaysTotal   *int `json:"daysTotal,omitempty"`
	Cities      int  `json:"cities"`
	Countries   int  `json:"countries"`
	DistanceKm  *int `json:"distanceKm,omitempty"`
}

type publicTrip struct {
	Slug      string       `json:"slug"`
	Title     string       `json:"title"`
	UpdatedAt time.Time    `json:"updatedAt"` // drives the "updated X ago" label
	Stats     publicStats  `json:"stats"`
	Stops     []publicStop `json:"stops"`
}

// publicTripListItem is a card on the website's trip index.
type publicTripListItem struct {
	Slug      string       `json:"slug"`
	Title     string       `json:"title"`
	DateRange string       `json:"dateRange,omitempty"`
	Cover     *publicPhoto `json:"cover,omitempty"`
	Stats     publicStats  `json:"stats"`
}

func (a api) stopsOf(tripID uint) []TripStop {
	var stops []TripStop
	a.db().Where("trip_id = ?", tripID).Order("position ASC").Find(&stops)
	return stops
}

func (a api) listPublic(c *gin.Context) {
	var trips []Trip
	a.db().Where("published = ?", true).Order("created_at DESC").Find(&trips)

	out := make([]publicTripListItem, 0, len(trips))
	for _, t := range trips {
		stops := a.stopsOf(t.ID)
		out = append(out, publicTripListItem{
			Slug:      t.Slug,
			Title:     t.Title,
			DateRange: tripDateRange(stops),
			Cover:     a.cover(t.ID),
			Stats:     computeStats(stops, t.DaysTotal, time.Now()),
		})
	}
	c.JSON(http.StatusOK, out)
}

func (a api) getPublic(c *gin.Context) {
	var trip Trip
	if err := a.db().Where("slug = ? AND published = ?", c.Param("slug"), true).First(&trip).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "trip not found"})
		return
	}
	stops := a.stopsOf(trip.ID)
	legs := LegsOf(stops)
	geometries := Lookup(a.db(), legKeys(legs))

	pubStops := make([]publicStop, 0, len(stops))
	for i, s := range stops {
		var photos []TripPhoto
		a.db().Where("stop_id = ?", s.ID).Order("position ASC").Find(&photos)

		stopPhotos := []publicPhoto{}
		transportPhotos := []publicPhoto{}
		for _, p := range photos {
			pp := publicPhoto{Src: p.URL, Cap: p.Caption, Tint: p.Tint}
			if p.Kind == "transport" {
				transportPhotos = append(transportPhotos, pp)
			} else {
				stopPhotos = append(stopPhotos, pp)
			}
		}

		var transportIn *publicTransport
		if strings.TrimSpace(s.TransportMode) != "" {
			transportIn = &publicTransport{
				Mode:       s.TransportMode,
				Label:      s.TransportLabel,
				Duration:   s.TransportDuration,
				DistanceKm: s.DistanceKm,
				Countries:  cleanCountries(s.TransportCountries),
				Waypoints:  cleanWaypoints(s.TransportWaypoints),
				Photos:     transportPhotos,
			}
			if leg, ok := legs[i]; ok {
				transportIn.Geometry = geometries[leg.Key]
			}
		}

		id := s.StopKey
		if id == "" {
			id = slugify(s.Name)
		}
		pubStops = append(pubStops, publicStop{
			ID: id, Name: s.Name, Dates: formatStopDates(s.StartDate, s.EndDate),
			Lat: s.Lat, Lng: s.Lng,
			Status: s.Status, Note: s.Note, Country: s.Country,
			TransportIn: transportIn, Photos: stopPhotos,
		})
	}

	c.JSON(http.StatusOK, publicTrip{
		Slug:      trip.Slug,
		Title:     trip.Title,
		UpdatedAt: trip.UpdatedAt,
		Stats:     computeStats(stops, trip.DaysTotal, time.Now()),
		Stops:     pubStops,
	})
}

// serveMedia streams an upload from the blob store.
func (a api) serveMedia(c *gin.Context) {
	name := c.Param("sha")
	if strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid filename"})
		return
	}
	rc, info, err := a.mc.Blobs().Get(c.Request.Context(), name)
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	defer rc.Close()
	ct := info.ContentType
	if ct == "" {
		ct = mime.TypeByExtension(filepath.Ext(name))
	}
	if ct == "" {
		ct = "application/octet-stream"
	}
	c.Header("Cache-Control", "public, max-age=31536000, immutable")
	c.DataFromReader(http.StatusOK, info.Size, ct, rc, nil)
}

// ---------------------------------------------------------------------------
// Helpers

// hydrateAdmin loads a trip with its stops and photos and shapes it for the
// editor.
func (a api) hydrateAdmin(id uint) AdminTripView {
	var trip Trip
	a.db().First(&trip, id)
	stops := a.stopsOf(id)
	legs := LegsOf(stops)
	routeRows := Statuses(a.db(), legKeys(legs))

	views := make([]AdminStopView, 0, len(stops))
	for i, s := range stops {
		var photos []TripPhoto
		a.db().Where("stop_id = ?", s.ID).Order("position ASC").Find(&photos)

		stopPhotos := []tripPhotoInput{}
		transportPhotos := []tripPhotoInput{}
		for _, p := range photos {
			pi := tripPhotoInput{URL: p.URL, Caption: p.Caption, Alt: p.Alt, Tint: p.Tint}
			if p.Kind == "transport" {
				transportPhotos = append(transportPhotos, pi)
			} else {
				stopPhotos = append(stopPhotos, pi)
			}
		}
		views = append(views, AdminStopView{
			StopKey: s.StopKey, Name: s.Name, StartDate: s.StartDate, EndDate: s.EndDate,
			Lat: s.Lat, Lng: s.Lng,
			Status: s.Status, Note: s.Note, Country: s.Country,
			TransportMode: s.TransportMode, TransportLabel: s.TransportLabel,
			TransportDuration:  s.TransportDuration,
			DistanceKm:         s.DistanceKm,
			TransportCountries: cleanCountries(s.TransportCountries),
			TransportWaypoints: cleanWaypoints(s.TransportWaypoints),
			Photos:             stopPhotos, TransportPhotos: transportPhotos,
			Route: adminRoute(legs, routeRows, i),
		})
	}

	return AdminTripView{
		ID: trip.ID, Slug: trip.Slug, Title: trip.Title, Published: trip.Published,
		DaysTotal: trip.DaysTotal,
		Stops:     views,
	}
}

func adminRoute(legs map[int]Leg, rows map[string]RouteGeometry, i int) *AdminRouteView {
	leg, ok := legs[i]
	if !ok {
		return nil
	}
	v := &AdminRouteView{Key: leg.Key, Status: "pending"}
	if row, ok := rows[leg.Key]; ok {
		v.Status, v.Source, v.Error = row.Status, row.Source, row.Error
	}
	return v
}

func createPhotos(tx *gorm.DB, stopID uint, kind string, photos []tripPhotoInput) error {
	for pi, p := range photos {
		// Skip empty rows (no image and no placeholder tint).
		if strings.TrimSpace(p.URL) == "" && strings.TrimSpace(p.Tint) == "" {
			continue
		}
		photo := TripPhoto{
			StopID: stopID, Kind: kind, Position: pi,
			URL: p.URL, Caption: p.Caption, Alt: p.Alt, Tint: p.Tint,
		}
		if err := tx.Create(&photo).Error; err != nil {
			return err
		}
	}
	return nil
}

// cover is the trip's first photo (earliest stop, earliest photo in it), or
// nil while the trip has none.
func (a api) cover(tripID uint) *publicPhoto {
	var photo TripPhoto
	err := a.db().
		Joins("JOIN trip_stops ON trip_stops.id = trip_photos.stop_id").
		Where("trip_stops.trip_id = ?", tripID).
		Order("trip_stops.position ASC, trip_photos.position ASC").
		First(&photo).Error
	if err != nil {
		return nil
	}
	return &publicPhoto{Src: photo.URL, Cap: photo.Caption, Tint: photo.Tint}
}

// uniqueSlug returns base, or base-2, base-3, ... until no trip uses it.
func (a api) uniqueSlug(base string) string {
	candidate := base
	for i := 2; ; i++ {
		var count int64
		a.db().Model(&Trip{}).Where("slug = ?", candidate).Count(&count)
		if count == 0 {
			return candidate
		}
		candidate = base + "-" + strconv.Itoa(i)
	}
}

func parseUintParam(c *gin.Context, name string) (uint, bool) {
	v, err := strconv.ParseUint(c.Param(name), 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid " + name})
		return 0, false
	}
	return uint(v), true
}

var slugInvalid = regexp.MustCompile(`[^a-z0-9]+`)

// slugify lower-cases, replaces runs of non-alphanumerics with one hyphen
// and trims hyphens at both ends.
func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = slugInvalid.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}
