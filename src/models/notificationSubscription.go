package models

import "gorm.io/gorm"

type NotificationSubscription struct {
	gorm.Model
	Endpoint       string `gorm:"uniqueIndex"`
	ExpirationTime *int64
	Auth           string
	P256dh         string
	UserID         *string // Optional: associate with user
}
