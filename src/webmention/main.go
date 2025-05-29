package webmention

import (
	"log"
	"net/http"
	"net/url"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/database"
	"github.com/LNA-DEV/HomePageCompanion/models"
	"github.com/gin-gonic/gin"
)

func isValidURL(str string) bool {
	u, err := url.ParseRequestURI(str)
	return err == nil && u.Scheme != "" && u.Host != ""
}

func HandleWebmention(c *gin.Context) {
	source := c.PostForm("source")
	target := c.PostForm("target")

	if source == "" || target == "" || !isValidURL(source) || !isValidURL(target) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid or missing 'source' and/or 'target'"})
		return
	}

	mention := models.Webmention{Source: source, Target: target, CreatedAt: time.Now()}
	if err := database.Db.Create(&mention).Error; err != nil {
		log.Println("Error saving mention:", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to store webmention"})
		return
	}

	log.Printf("Stored webmention: source=%s target=%s", source, target)
	c.Status(http.StatusAccepted)
}
