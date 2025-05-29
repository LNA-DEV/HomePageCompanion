package webpush

import (
	"fmt"
	"log"
	"time"

	"gorm.io/gorm"

	"github.com/LNA-DEV/HomePageCompanion/database"
	"github.com/LNA-DEV/HomePageCompanion/models"
	"github.com/SherClockHolmes/webpush-go"
)

var VapidKey models.VAPIDKey

func LoadVAPIDKeys() error {
	var key models.VAPIDKey

	// Try to find the first saved key
	if err := database.Db.First(&key).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			// No keys found — generate new ones
			privateKey, publicKey, err := webpush.GenerateVAPIDKeys()
			if err != nil {
				return fmt.Errorf("failed to generate VAPID keys: %w", err)
			}

			// Save new keys
			key = models.VAPIDKey{
				PublicKey:  publicKey,
				PrivateKey: privateKey,
				CreatedAt:  time.Now(),
			}

			if err := database.Db.Create(&key).Error; err != nil {
				return fmt.Errorf("failed to save VAPID keys: %w", err)
			}

			log.Println("Generated and saved new VAPID keys.")
		} else {
			return err
		}
	} else {
		log.Println("Loaded existing VAPID keys.")
	}

	VapidKey = key

	return nil
}
