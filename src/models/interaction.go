package models

import "time"

type Interaction struct {
	ID         uint   `gorm:"primaryKey"`
	ItemName   string `gorm:"uniqueIndex:idx_item_platform_target"`
	Platform   string `gorm:"uniqueIndex:idx_item_platform_target"`
	TargetName string `gorm:"uniqueIndex:idx_item_platform_target"`
	LikeCount  int
	// Future fields for expansion
	// CommentCount int
	// ShareCount   int
	// ViewCount    int
	CreatedAt time.Time
	UpdatedAt time.Time
}
