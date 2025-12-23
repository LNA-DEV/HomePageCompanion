package models

import "time"

type NativeLike struct {
	ID        uint   `gorm:"primaryKey"`
	ItemName  string `gorm:"index;not null"`
	IPHash    string `gorm:"column:ip_hash;index;not null"` // SHA256 hash of IP + salt for GDPR compliance
	Token     string `gorm:"index;not null"`
	CreatedAt time.Time
}
