package database

import (
	"fmt"
	"log"
	"time"

	"gorm.io/gorm"
)

type Migration struct {
	ID        uint   `gorm:"primaryKey"`
	Name      string `gorm:"uniqueIndex;not null"`
	AppliedAt time.Time
}

type MigrationFunc func(tx *gorm.DB) error

type migrationEntry struct {
	Name string
	Fn   MigrationFunc
}

var registry []migrationEntry

func RegisterMigration(name string, fn MigrationFunc) {
	registry = append(registry, migrationEntry{Name: name, Fn: fn})
}

// RunMigrations applies every registered data migration not applied yet, each
// in its own transaction. It runs before the schema AutoMigrate, so a
// migration must cope with tables that do not exist yet.
func RunMigrations() error {
	if err := Db.AutoMigrate(&Migration{}); err != nil {
		return err
	}

	for _, m := range registry {
		var existing Migration
		if Db.Where("name = ?", m.Name).First(&existing).Error == nil {
			continue
		}

		log.Printf("Running migration: %s", m.Name)
		err := Db.Transaction(func(tx *gorm.DB) error {
			if err := m.Fn(tx); err != nil {
				return err
			}
			return tx.Create(&Migration{Name: m.Name, AppliedAt: time.Now()}).Error
		})
		if err != nil {
			return fmt.Errorf("migration %q: %w", m.Name, err)
		}
		log.Printf("Migration %s completed", m.Name)
	}
	return nil
}
