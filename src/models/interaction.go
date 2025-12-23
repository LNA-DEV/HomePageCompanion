package models

import "time"

type Interaction struct {
	ID         uint   `gorm:"primaryKey"`
	ItemName   string `gorm:"index"`
	Platform   string `gorm:"index"`
	TargetName string `gorm:"index"`
	LikeCount  int
	// Future fields for expansion
	// CommentCount int
	// ShareCount   int
	// ViewCount    int
	CreatedAt time.Time
	UpdatedAt time.Time
}
