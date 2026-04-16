package database

import (
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

func RunMigrations() {
	Db.AutoMigrate(&Migration{})

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
			log.Fatalf("Migration %q failed: %v", m.Name, err)
		}
		log.Printf("Migration %s completed", m.Name)
	}
}
