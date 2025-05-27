package main

import (
	"log"

	"github.com/LNA-DEV/HomePageCompanion/src/autouploader"
	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

var db *gorm.DB

func main() {
	// Database
	var err error
	db, err = gorm.Open(sqlite.Open("companion.db"), &gorm.Config{})
	if err != nil {
		log.Fatal("Failed to connect to database:", err)
	}

	if err := db.AutoMigrate(&Webmention{}); err != nil {
		log.Fatal("Migration failed:", err)
	}

	// Router config
	router := gin.Default()

	router.POST("/webmention", handleWebmention)
	router.POST("/upload/:platform", uploadNext)
	
	router.Run(":8080")
}

func uploadNext(c *gin.Context) {
	platform := c.Param("platform")
	autouploader.Publish(platform)
}
