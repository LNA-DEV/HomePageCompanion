package database

import (
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func useDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	prev := Db
	Db = db
	t.Cleanup(func() { Db = prev })
	return db
}

// A fresh installation has none of the tables migration 001 rewrites
// (autoposting-ui.md §3 #3).
func TestMigrationsOnEmptyDatabase(t *testing.T) {
	db := useDB(t)
	if err := RunMigrations(); err != nil {
		t.Fatalf("migrations on an empty database: %v", err)
	}
	var n int64
	db.Model(&Migration{}).Where("name = ?", "001_itemname_to_guid").Count(&n)
	if n != 1 {
		t.Fatalf("001 not recorded: %d", n)
	}
}

// An installation from before 001 still gets its item names rewritten to
// GUIDs and the column renamed.
func TestMigration001OnOldSchema(t *testing.T) {
	db := useDB(t)
	for _, q := range []string{
		`CREATE TABLE feed_items (id integer primary key, title text, guid text)`,
		`CREATE TABLE auto_upload_items (id integer primary key, platform text, item_name text)`,
		`CREATE TABLE interactions (id integer primary key, item_name text)`,
		`CREATE TABLE native_likes (id integer primary key, item_name text)`,
		`INSERT INTO feed_items (title, guid) VALUES ('IMG_1.jpg', 'uuid-1')`,
		`INSERT INTO auto_upload_items (platform, item_name) VALUES ('mastodon', 'IMG_1.jpg'), ('bluesky', 'unknown.jpg')`,
	} {
		if err := db.Exec(q).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := RunMigrations(); err != nil {
		t.Fatal(err)
	}
	var ids []string
	db.Raw(`SELECT item_id FROM auto_upload_items ORDER BY id`).Scan(&ids)
	if len(ids) != 2 || ids[0] != "uuid-1" || ids[1] != "unknown.jpg" {
		t.Fatalf("item ids: %v", ids)
	}
	// Running again changes nothing.
	if err := RunMigrations(); err != nil {
		t.Fatal(err)
	}
}
