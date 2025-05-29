package models

import "time"

type AutoUploadItem struct {
	ID        uint   `gorm:"primaryKey"`
	Platform  string `gorm:"index"`
	ItemName  string `gorm:"index"`
	CreatedAt time.Time
}
