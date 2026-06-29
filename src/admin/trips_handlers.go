package admin

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/database"
	"github.com/LNA-DEV/HomePageCompanion/imageresize"
	"github.com/LNA-DEV/HomePageCompanion/models"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const tripMediaDir = "data/trips"
const tripMaxImageBytes = 30 << 20 // 30 MiB — generous so large phone photos (e.g. 50MP) reach the transform

// tripImageLimits transforms every (decodable) trip upload: re-encode to
// JPEG to strip EXIF/IPTC/XMP metadata (incl. GPS location) and cap the
// long edge at 4096px. No byte cap → quality stays at 95.
var tripImageLimits = imageresize.Limits{MaxLongEdge: 4096, ForceReencode: true}

// RegisterTripRoutes wires both the admin (auth-protected) and public (open)
// trip endpoints. Mirrors RegisterMicroblogRoutes — admin writes require the
// ApiKey, public reads are anonymous so the website can render the trip.
func RegisterTripRoutes(api *gin.RouterGroup, authMiddleware gin.HandlerFunc) {
	adm := api.Group("/admin/trips")
	adm.Use(authMiddleware)
	{
		adm.GET("", ListTripsAdmin)
		adm.POST("", CreateTrip)
		adm.GET("/:id", GetTripAdmin)
		adm.PUT("/:id", UpdateTrip)
		adm.DELETE("/:id", DeleteTrip)
		adm.POST("/upload", UploadTripImage)
	}

	pub := api.Group("/trips")
	{
		pub.GET("", ListTripsPublic)
		pub.GET("/:slug", GetTripPublic)
		pub.GET("/media/:sha", ServeTripMedia)
	}
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
	StopKey           string           `json:"stopKey"`
	Name              string           `json:"name"`
	StartDate         string           `json:"startDate"`
	EndDate           string           `json:"endDate"`
	Lat               float64          `json:"lat"`
	Lng               float64          `json:"lng"`
	Status            string           `json:"status"`
	Note              string           `json:"note"`
	Country           string           `json:"country"`
	TransportMode      string           `json:"transportMode"`
	TransportLabel     string           `json:"transportLabel"`
	TransportDuration  string           `json:"transportDuration"`
	DistanceKm         *int             `json:"distanceKm"`         // length of the leg into this stop
	TransportCountries []string         `json:"transportCountries"` // countries the leg passes through
	TransportWaypoints []models.Waypoint `json:"transportWaypoints"` // ordered via-points the route passes through
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

// adminTripStopView splits a stop's photos into the two galleries the editor
// edits independently (stop photos vs transport-leg photos).
type adminTripStopView struct {
	StopKey           string           `json:"stopKey"`
	Name              string           `json:"name"`
	StartDate         string           `json:"startDate"`
	EndDate           string           `json:"endDate"`
	Lat               float64          `json:"lat"`
	Lng               float64          `json:"lng"`
	Status            string           `json:"status"`
	Note              string           `json:"note"`
	Country           string           `json:"country"`
	TransportMode      string           `json:"transportMode"`
	TransportLabel     string           `json:"transportLabel"`
	TransportDuration  string           `json:"transportDuration"`
	DistanceKm         *int             `json:"distanceKm"`
	TransportCountries []string         `json:"transportCountries"`
	TransportWaypoints []models.Waypoint `json:"transportWaypoints"`
	Photos             []tripPhotoInput `json:"photos"`
	TransportPhotos    []tripPhotoInput `json:"transportPhotos"`
}

type adminTripView struct {
	ID        uint                `json:"id"`
	Slug      string              `json:"slug"`
	Title     string              `json:"title"`
	Published bool                `json:"published"`
	DaysTotal *int                `json:"daysTotal"`
	Stops     []adminTripStopView `json:"stops"`
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

func ListTripsAdmin(c *gin.Context) {
	var trips []models.Trip
	database.Db.Order("created_at DESC").Find(&trips)

	out := make([]adminTripListItem, 0, len(trips))
	for _, t := range trips {
		var count int64
		database.Db.Model(&models.TripStop{}).Where("trip_id = ?", t.ID).Count(&count)
		out = append(out, adminTripListItem{
			ID: t.ID, Slug: t.Slug, Title: t.Title, Published: t.Published, StopCount: count,
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

func CreateTrip(c *gin.Context) {
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
	slug := uniqueTripSlug(base, 0)

	trip := models.Trip{Slug: slug, Title: title, Published: false}
	if err := database.Db.Create(&trip).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not create trip"})
		return
	}
	c.JSON(http.StatusCreated, hydrateAdminTrip(trip.ID))
}

func GetTripAdmin(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	var trip models.Trip
	if err := database.Db.First(&trip, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "trip not found"})
		return
	}
	c.JSON(http.StatusOK, hydrateAdminTrip(trip.ID))
}

func UpdateTrip(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	var req updateTripInput
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON"})
		return
	}

	var trip models.Trip
	if err := database.Db.First(&trip, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "trip not found"})
		return
	}

	// Resolve slug: keep the existing one if blank, otherwise slugify and make
	// sure it does not collide with a *different* trip.
	slug := slugify(req.Slug)
	if slug == "" {
		slug = trip.Slug
	}
	var clash int64
	database.Db.Model(&models.Trip{}).Where("slug = ? AND id <> ?", slug, trip.ID).Count(&clash)
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

	// Replace the trip's stops + photos wholesale inside a transaction. Simpler
	// and less error-prone than diffing associations. NOTE: media files left on
	// disk by removed photos are not garbage-collected (same as microblog).
	err := database.Db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(&trip).Error; err != nil {
			return err
		}
		var stopIDs []uint
		tx.Model(&models.TripStop{}).Where("trip_id = ?", trip.ID).Pluck("id", &stopIDs)
		if len(stopIDs) > 0 {
			if err := tx.Where("stop_id IN ?", stopIDs).Delete(&models.TripPhoto{}).Error; err != nil {
				return err
			}
		}
		if err := tx.Where("trip_id = ?", trip.ID).Delete(&models.TripStop{}).Error; err != nil {
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
			stop := models.TripStop{
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
			if err := createTripPhotos(tx, stop.ID, "stop", s.Photos); err != nil {
				return err
			}
			if err := createTripPhotos(tx, stop.ID, "transport", s.TransportPhotos); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not save trip"})
		return
	}
	c.JSON(http.StatusOK, hydrateAdminTrip(trip.ID))
}

func DeleteTrip(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	err := database.Db.Transaction(func(tx *gorm.DB) error {
		var stopIDs []uint
		tx.Model(&models.TripStop{}).Where("trip_id = ?", id).Pluck("id", &stopIDs)
		if len(stopIDs) > 0 {
			if err := tx.Where("stop_id IN ?", stopIDs).Delete(&models.TripPhoto{}).Error; err != nil {
				return err
			}
		}
		if err := tx.Where("trip_id = ?", id).Delete(&models.TripStop{}).Error; err != nil {
			return err
		}
		return tx.Delete(&models.Trip{}, id).Error
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not delete trip"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "deleted"})
}

// UploadTripImage stores a multipart-uploaded image under
// data/trips/<sha256>.<ext> and returns a public URL the editor attaches to a
// stop or transport-leg photo. Mirrors UploadMicroblogImage.
func UploadTripImage(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, tripMaxImageBytes)
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
	// Strip metadata + bound dimensions by re-encoding. On decode failure
	// (e.g. HEIC, non-image) keep the original bytes and extension.
	if processed, perr := imageresize.PrepareForTarget(data, tripImageLimits); perr == nil {
		data = processed
		ext = ".jpg"
	}

	if err := os.MkdirAll(tripMediaDir, 0o755); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not create media dir"})
		return
	}
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	path := filepath.Join(tripMediaDir, sha+ext)
	if _, statErr := os.Stat(path); errors.Is(statErr, os.ErrNotExist) {
		if err := os.WriteFile(path, data, 0o644); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "write failed"})
			return
		}
	}

	publicURL := "/api/trips/media/" + sha + ext
	c.JSON(http.StatusOK, gin.H{"url": publicURL, "size": len(data)})
}

// ---------------------------------------------------------------------------
// Public handlers

type publicPhoto struct {
	Src  string `json:"src,omitempty"`
	Cap  string `json:"cap"`
	Tint string `json:"tint,omitempty"`
}

type publicTransport struct {
	Mode       string        `json:"mode"`
	Label      string        `json:"label"`
	Duration   string        `json:"duration"`
	DistanceKm *int          `json:"distanceKm,omitempty"`
	Countries  []string      `json:"countries,omitempty"`
	Waypoints  []models.Waypoint `json:"waypoints,omitempty"`
	Photos     []publicPhoto `json:"photos"`
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

// publicStats: cities and countries are always present counts; daysElapsed,
// daysTotal and distanceKm are pointers because they may be unknown (no dates,
// no manual total, or no per-leg distances entered yet).
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
	UpdatedAt time.Time    `json:"updatedAt"` // last edit time; drives the "updated X ago" label
	Stats     publicStats  `json:"stats"`
	Stops     []publicStop `json:"stops"`
}

// publicTripListItem is a card on the website's trip index: enough to render a
// cover, title, date range and the header stats without fetching the full trip.
type publicTripListItem struct {
	Slug      string       `json:"slug"`
	Title     string       `json:"title"`
	DateRange string       `json:"dateRange,omitempty"`
	Cover     *publicPhoto `json:"cover,omitempty"`
	Stats     publicStats  `json:"stats"`
}

func ListTripsPublic(c *gin.Context) {
	var trips []models.Trip
	database.Db.Where("published = ?", true).Order("created_at DESC").Find(&trips)

	out := make([]publicTripListItem, 0, len(trips))
	for _, t := range trips {
		var stops []models.TripStop
		database.Db.Where("trip_id = ?", t.ID).Order("position ASC").Find(&stops)
		out = append(out, publicTripListItem{
			Slug:      t.Slug,
			Title:     t.Title,
			DateRange: tripDateRange(stops),
			Cover:     coverForTrip(t.ID),
			Stats:     computeStats(stops, t.DaysTotal),
		})
	}
	c.JSON(http.StatusOK, out)
}

func GetTripPublic(c *gin.Context) {
	slug := c.Param("slug")
	var trip models.Trip
	if err := database.Db.
		Where("slug = ? AND published = ?", slug, true).
		First(&trip).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "trip not found"})
		return
	}

	var stops []models.TripStop
	database.Db.Where("trip_id = ?", trip.ID).Order("position ASC").Find(&stops)

	pubStops := make([]publicStop, 0, len(stops))
	for _, s := range stops {
		var photos []models.TripPhoto
		database.Db.Where("stop_id = ?", s.ID).Order("position ASC").Find(&photos)

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
		Stats:     computeStats(stops, trip.DaysTotal),
		Stops:     pubStops,
	})
}

// ServeTripMedia streams a previously-uploaded media file. Path traversal is
// blocked by rejecting separators in the name and confirming the resolved path
// stays inside tripMediaDir. Mirrors ServeMicroblogMedia.
func ServeTripMedia(c *gin.Context) {
	name := c.Param("sha")
	if strings.Contains(name, "/") || strings.Contains(name, "..") || strings.Contains(name, "\\") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid filename"})
		return
	}
	path := filepath.Join(tripMediaDir, name)
	abs, err := filepath.Abs(path)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid path"})
		return
	}
	dirAbs, _ := filepath.Abs(tripMediaDir)
	if !strings.HasPrefix(abs, dirAbs+string(filepath.Separator)) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid filename"})
		return
	}
	c.File(abs)
}

// ---------------------------------------------------------------------------
// Helpers

// hydrateAdminTrip loads a trip with its stops + photos and shapes it for the
// editor, splitting each stop's photos into the stop / transport galleries.
func hydrateAdminTrip(id uint) adminTripView {
	var trip models.Trip
	database.Db.First(&trip, id)

	var stops []models.TripStop
	database.Db.Where("trip_id = ?", id).Order("position ASC").Find(&stops)

	views := make([]adminTripStopView, 0, len(stops))
	for _, s := range stops {
		var photos []models.TripPhoto
		database.Db.Where("stop_id = ?", s.ID).Order("position ASC").Find(&photos)

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
		views = append(views, adminTripStopView{
			StopKey: s.StopKey, Name: s.Name, StartDate: s.StartDate, EndDate: s.EndDate,
			Lat: s.Lat, Lng: s.Lng,
			Status: s.Status, Note: s.Note, Country: s.Country,
			TransportMode: s.TransportMode, TransportLabel: s.TransportLabel,
			TransportDuration: s.TransportDuration,
			DistanceKm:        s.DistanceKm,
			TransportCountries: cleanCountries(s.TransportCountries),
			TransportWaypoints: cleanWaypoints(s.TransportWaypoints),
			Photos:             stopPhotos, TransportPhotos: transportPhotos,
		})
	}

	return adminTripView{
		ID: trip.ID, Slug: trip.Slug, Title: trip.Title, Published: trip.Published,
		DaysTotal: trip.DaysTotal,
		Stops:     views,
	}
}

func createTripPhotos(tx *gorm.DB, stopID uint, kind string, photos []tripPhotoInput) error {
	for pi, p := range photos {
		// Skip empty rows (no image and no placeholder tint).
		if strings.TrimSpace(p.URL) == "" && strings.TrimSpace(p.Tint) == "" {
			continue
		}
		photo := models.TripPhoto{
			StopID: stopID, Kind: kind, Position: pi,
			URL: p.URL, Caption: p.Caption, Alt: p.Alt, Tint: p.Tint,
		}
		if err := tx.Create(&photo).Error; err != nil {
			return err
		}
	}
	return nil
}

// formatStopDates turns the stored YYYY-MM-DD start/end into the human display
// range the HTML renders, matching its style:
//   - "Jun 3–4"        same month, two days
//   - "Jun 30 – Jul 2" spans months
//   - "Jun 11–"        open-ended (no end date yet — e.g. the current stop)
//   - "Jun 3"          single day, or end == start
//
// Unparseable input falls back to the raw start string rather than erroring.
func formatStopDates(start, end string) string {
	start = strings.TrimSpace(start)
	end = strings.TrimSpace(end)
	if start == "" {
		return ""
	}
	s, err := time.Parse("2006-01-02", start)
	if err != nil {
		return start
	}
	startStr := s.Format("Jan 2")
	if end == "" {
		return startStr + "–"
	}
	e, err := time.Parse("2006-01-02", end)
	if err != nil {
		return startStr
	}
	if s.Equal(e) {
		return startStr
	}
	if s.Year() == e.Year() && s.Month() == e.Month() {
		return startStr + "–" + e.Format("2")
	}
	return startStr + " – " + e.Format("Jan 2")
}

// computeStats derives the header stats from a trip's stops. Everything except
// daysTotal (passed in — it's the one manual stat) is calculated: cities is the
// stop count; countries is the distinct union of each stop's Country and its
// transport leg's TransportCountries; distanceKm is the sum of per-leg
// distances (nil if none entered); daysElapsed counts from the earliest stop
// start date to today, floored at 0 and capped at daysTotal when that is set.
func computeStats(stops []models.TripStop, daysTotal *int) publicStats {
	stats := publicStats{Cities: len(stops), DaysTotal: daysTotal}

	countries := map[string]bool{}
	distance := 0
	hasDistance := false
	var firstStart time.Time
	hasStart := false

	for _, s := range stops {
		if c := strings.TrimSpace(s.Country); c != "" {
			countries[c] = true
		}
		for _, c := range s.TransportCountries {
			if c = strings.TrimSpace(c); c != "" {
				countries[c] = true
			}
		}
		if s.DistanceKm != nil {
			distance += *s.DistanceKm
			hasDistance = true
		}
		if d, err := time.Parse("2006-01-02", strings.TrimSpace(s.StartDate)); err == nil {
			if !hasStart || d.Before(firstStart) {
				firstStart = d
			}
			hasStart = true
		}
	}

	stats.Countries = len(countries)
	if hasDistance {
		stats.DistanceKm = &distance
	}
	if hasStart {
		now := time.Now().UTC()
		today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		elapsed := int(today.Sub(firstStart).Hours()/24) + 1
		if elapsed < 0 {
			elapsed = 0
		}
		if daysTotal != nil && elapsed > *daysTotal {
			elapsed = *daysTotal
		}
		stats.DaysElapsed = &elapsed
	}
	return stats
}

// coverForTrip returns the trip's first photo (earliest stop, earliest photo
// within it) as a cover thumbnail, or nil when the trip has no photos yet.
func coverForTrip(tripID uint) *publicPhoto {
	var photo models.TripPhoto
	err := database.Db.
		Joins("JOIN trip_stops ON trip_stops.id = trip_photos.stop_id").
		Where("trip_stops.trip_id = ?", tripID).
		Order("trip_stops.position ASC, trip_photos.position ASC").
		First(&photo).Error
	if err != nil {
		return nil
	}
	return &publicPhoto{Src: photo.URL, Cap: photo.Caption, Tint: photo.Tint}
}

// tripDateRange formats the trip's overall span — first stop's start to the last
// stop's end (falling back to the last start when open-ended) — e.g.
// "Jun 3–4, 2026" or "Jun 3 – Jul 2, 2026". Returns "" when no dates are set.
func tripDateRange(stops []models.TripStop) string {
	if len(stops) == 0 {
		return ""
	}
	s, err := time.Parse("2006-01-02", strings.TrimSpace(stops[0].StartDate))
	if err != nil {
		return ""
	}
	last := stops[len(stops)-1]
	endStr := strings.TrimSpace(last.EndDate)
	if endStr == "" {
		endStr = strings.TrimSpace(last.StartDate)
	}
	e, err := time.Parse("2006-01-02", endStr)
	if err != nil || e.Before(s) || s.Equal(e) {
		return s.Format("Jan 2, 2006")
	}
	if s.Year() == e.Year() {
		if s.Month() == e.Month() {
			return s.Format("Jan 2") + "–" + e.Format("2, 2006")
		}
		return s.Format("Jan 2") + " – " + e.Format("Jan 2, 2006")
	}
	return s.Format("Jan 2, 2006") + " – " + e.Format("Jan 2, 2006")
}

// cleanCountries trims and drops blank entries, always returning a non-nil
// slice so admin JSON renders [] rather than null.
func cleanCountries(in []string) []string {
	out := make([]string, 0, len(in))
	for _, c := range in {
		if c = strings.TrimSpace(c); c != "" {
			out = append(out, c)
		}
	}
	return out
}

// cleanWaypoints drops null/zero points and always returns a non-nil slice so
// JSON renders [] rather than null (and old trips with no waypoints surface as
// [] on read). (0,0) is treated as "unset" and dropped.
func cleanWaypoints(in []models.Waypoint) []models.Waypoint {
	out := make([]models.Waypoint, 0, len(in))
	for _, w := range in {
		if w.Lat == 0 && w.Lng == 0 {
			continue
		}
		out = append(out, w)
	}
	return out
}

var slugInvalid = regexp.MustCompile(`[^a-z0-9]+`)

// slugify lower-cases, replaces runs of non-alphanumerics with a single hyphen
// and trims leading/trailing hyphens.
func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = slugInvalid.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

// uniqueTripSlug returns base, or base-2, base-3, ... until it finds one no
// existing trip uses.
func uniqueTripSlug(base string, _ int) string {
	candidate := base
	for i := 2; ; i++ {
		var count int64
		database.Db.Model(&models.Trip{}).Where("slug = ?", candidate).Count(&count)
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
