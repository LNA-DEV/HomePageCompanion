package models

import "time"

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
