package webpush

import (
	"fmt"
	"time"
	"log"
	"gorm.io/gorm"

	"github.com/LNA-DEV/HomePageCompanion/database"
	"github.com/SherClockHolmes/webpush-go"
)

type VAPIDKey struct {
	ID         uint   `gorm:"primaryKey"`
	PublicKey  string `gorm:"unique"`
	PrivateKey string
	CreatedAt  time.Time
}

// TableName overrides the table name used by GORM
func (VAPIDKey) TableName() string {
	return "vapid_keys"
}

var VapidKey VAPIDKey

func LoadVAPIDKeys() (error) {
	var key VAPIDKey

	// Try to find the first saved key
	if err := database.Db.First(&key).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			// No keys found — generate new ones
			privateKey, publicKey, err := webpush.GenerateVAPIDKeys()
			if err != nil {
				return fmt.Errorf("failed to generate VAPID keys: %w", err)
			}

			// Save new keys
			key = VAPIDKey{
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
