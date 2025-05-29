package webpush

import (
	"net/http"

	"github.com/LNA-DEV/HomePageCompanion/database"
	"github.com/LNA-DEV/HomePageCompanion/models"
	"github.com/gin-gonic/gin"
)

type SubscriptionRequest struct {
	Endpoint       string           `json:"endpoint" binding:"required"`
	ExpirationTime *int64           `json:"expirationTime"`
	Keys           SubscriptionKeys `json:"keys" binding:"required"`
	UserID         *string          `json:"userId"` // optional
}

type SubscriptionKeys struct {
	P256dh string `json:"p256dh" binding:"required"`
	Auth   string `json:"auth" binding:"required"`
}

func SubscribeHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		var req SubscriptionRequest

		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		sub := models.NotificationSubscription{
			Endpoint:       req.Endpoint,
			ExpirationTime: req.ExpirationTime,
			Auth:           req.Keys.Auth,
			P256dh:         req.Keys.P256dh,
			UserID:         req.UserID,
		}

		// Create or update (based on endpoint)
		err := database.Db.Where("endpoint = ?", sub.Endpoint).FirstOrCreate(&sub).Error
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save subscription"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "subscription saved"})
	}
}
