package interactions

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/LNA-DEV/HomePageCompanion/database"
	"github.com/LNA-DEV/HomePageCompanion/models"
	"github.com/gin-gonic/gin"
)

var ErrRateLimited = errors.New("rate limited")

// HandleInteraction retrieves interactions from the database for a given item
func HandleInteraction(c *gin.Context) {
	itemID := c.Param("item_id")
	targetName := c.Param("target_name")

	var interactions []models.Interaction
	var err error

	if targetName == "all" {
		err = database.Db.Where("item_id = ?", itemID).Find(&interactions).Error
	} else {
		err = database.Db.Where("item_id = ? AND target_name = ?", itemID, targetName).Find(&interactions).Error
	}

	if err != nil {
		c.Data(http.StatusInternalServerError, "application/text", []byte(err.Error()))
		return
	}

	likesList := []LikesResponse{}
	for _, interaction := range interactions {
		likesList = append(likesList, LikesResponse{
			Platform: interaction.Platform,
			Likes:    interaction.LikeCount,
		})
	}

	jsonData, jsonErr := json.Marshal(likesList)
	if jsonErr != nil {
		c.Data(http.StatusInternalServerError, "application/text", []byte(jsonErr.Error()))
		return
	}

	c.Data(http.StatusOK, "application/json", jsonData)
}

// FetchAndStoreInteractions is a back-compat alias for FetchAllThrottled.
// New code should call FetchAllThrottled directly.
func FetchAndStoreInteractions() {
	FetchAllThrottled()
}

type LikesResponse struct {
	Platform string `json:"platform"`
	Likes    int    `json:"likes"`
}
