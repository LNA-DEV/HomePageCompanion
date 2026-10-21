package trips

import (
	"bytes"
	"context"
	"mime"
	"os"
	"path/filepath"

	"github.com/LNA-DEV/HomePageCompanion/companion"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// importLegacy moves the trips, their stops and photos, the computed routes
// and the images in data/trips/ over. The images go to the blob store and
// keep their public URLs; the route keys are computed the same way, so no
// leg is routed again.
func importLegacy(mc companion.ModuleContext, l *companion.Legacy) error {
	if !l.HasTable("trips") {
		return nil
	}
	db := mc.DB()
	var trips []Trip
	var stops []TripStop
	var photos []TripPhoto
	var routes []RouteGeometry
	l.DB.Find(&trips)
	if l.HasTable("trip_stops") {
		l.DB.Find(&stops)
	}
	if l.HasTable("trip_photos") {
		l.DB.Find(&photos)
	}
	if l.HasTable("route_geometries") {
		l.DB.Find(&routes)
	}
	if !l.Check {
		err := db.Transaction(func(tx *gorm.DB) error {
			tx = tx.Omit(clause.Associations)
			for _, rows := range []any{&trips, &stops, &photos, &routes} {
				if err := tx.CreateInBatches(rows, 100).Error; err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	files := 0
	dir := filepath.Join(l.DataDir, "trips")
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		files++
		if l.Check {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return err
		}
		if err := mc.Blobs().Put(context.Background(), e.Name(), bytes.NewReader(data), int64(len(data)), mime.TypeByExtension(filepath.Ext(e.Name()))); err != nil {
			return err
		}
	}
	l.Reportf("trips: %d trip(s), %d stop(s), %d photo(s), %d computed route(s), %d image(s) into the blob store",
		len(trips), len(stops), len(photos), len(routes), files)
	return nil
}
