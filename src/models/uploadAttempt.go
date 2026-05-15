package models

import "time"

// UploadAttempt records a single attempt to publish a feed item to a target
// platform. One row per attempt, regardless of outcome. Used by the admin UI
// to surface per-image failures and provider health.
type UploadAttempt struct {
	ID             uint   `gorm:"primaryKey" json:"id"`
	Source         string `gorm:"column:source;default:autouploader;index" json:"source"`
	ConnectionName string `gorm:"index" json:"connectionName"`
	ItemID         string `gorm:"column:item_id;index" json:"itemId"`
	Platform       string `gorm:"index" json:"platform"`
	TargetName     string `gorm:"index" json:"targetName"`
	Success        bool   `gorm:"index" json:"success"`
	ErrorCode      string `json:"errorCode,omitempty"`
	ErrorMessage   string `gorm:"type:text" json:"errorMessage,omitempty"`
	HTTPStatus     int    `json:"httpStatus,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
}
