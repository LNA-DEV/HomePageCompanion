package companiontest

import (
	"path/filepath"
	"testing"

	"github.com/LNA-DEV/HomePageCompanion/companion"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// LegacyDB builds an old companion.db from SQL statements and returns it as
// a companion.Legacy, for testing a module's Import. Its report lines go to
// the test log.
func LegacyDB(t *testing.T, statements ...string) *companion.Legacy {
	t.Helper()
	dir := t.TempDir()
	db, err := gorm.Open(sqlite.Open(filepath.Join(dir, "companion.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range statements {
		if err := db.Exec(s).Error; err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	t.Cleanup(func() {
		if d, err := db.DB(); err == nil {
			d.Close()
		}
	})
	return companion.NewLegacy(db, dir, false, func(line string) { t.Log("migrate: " + line) })
}
