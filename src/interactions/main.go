package interactions

import (
	"encoding/json"
	"net/http"

	"github.com/LNA-DEV/HomePageCompanion/autouploader"
	"github.com/LNA-DEV/HomePageCompanion/config"
	"github.com/gin-gonic/gin"
)

func HandleInteraction(c *gin.Context) {
	itemName := c.Param("item_name")

	likesList := []LikesResponse{}

	for _, target := range config.Data.Targets {
		switch target.Platform {
		case "bluesky":
			bskyItem, autoUploadErr := autouploader.GetPublishedEntry(itemName, "bluesky")
			if autoUploadErr == nil {
				print("Could not get AutoUploadItem for" + itemName)
			}
			bskyResult, bskyErr := handleBlueskyLikes(*bskyItem, target.Name)
			if bskyErr != nil {
				c.Data(http.StatusInternalServerError, "application/text", []byte(bskyErr.Error()))
			}

			response := LikesResponse{Platform: "Bluesky", Likes: len(bskyResult.Likes)}

			likesList = append(likesList, response)
		case "pixelfed":
			pixelfedItem, autoUploadErr := autouploader.GetPublishedEntry(itemName, "pixelfed")
			if autoUploadErr == nil {
				print("Could not get AutoUploadItem for" + itemName)
			}
			pixelfedResult, pixelfedErr := handlePixelfedLikes(*pixelfedItem, target.Name)
			if pixelfedErr != nil {
				c.Data(http.StatusInternalServerError, "application/text", []byte(pixelfedErr.Error()))
			}

			response := LikesResponse{Platform: "Pixelfed", Likes: len(pixelfedResult.Accounts)}

			likesList = append(likesList, response)
		}
	}

	jsonData, jsonErr := json.Marshal(likesList)
	if jsonErr != nil {
		c.Data(http.StatusInternalServerError, "application/text", []byte(jsonErr.Error()))
		return
	}

	c.Data(http.StatusOK, "application/json", jsonData)
}

type LikesResponse struct {
	Platform string `json:"platform"`
	Likes    int    `json:"likes"`
}
