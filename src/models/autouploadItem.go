package models

import "time"

type AutoUploadItem struct {
	ID        uint   `gorm:"primaryKey"`
	Platform  string `gorm:"index"`
	ItemID    string `gorm:"column:item_id;index"`
	PostUrl   *string
	VersionId *string
	PostId    *string
	CreatedAt time.Time
}
