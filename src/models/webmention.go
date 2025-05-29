package models

import "time"

type Webmention struct {
	ID        uint   `gorm:"primaryKey"`
	Source    string `gorm:"not null"`
	Target    string `gorm:"not null"`
	CreatedAt time.Time
}