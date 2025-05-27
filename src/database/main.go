package database

import (
	"log"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

var Db *gorm.DB

func LoadDatabase() {
	var err error
	Db, err = gorm.Open(sqlite.Open("companion.db"), &gorm.Config{})
	if err != nil {
		log.Fatal("Failed to connect to database:", err)
	}
}

func MigrateModels(models []interface{}) {
	for _, v := range models {
		if err := Db.AutoMigrate(&v); err != nil {
			log.Fatal("Migration failed:", err)
		}
	}
}
