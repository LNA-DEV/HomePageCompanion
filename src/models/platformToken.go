package models

import "time"

// PlatformToken is the access token a Meta target (Instagram, Threads) uses
// right now: the one from .env until the first refresh, then the latest
// refreshed one. SeedHash is the SHA-256 of the .env token the row descends
// from, so a token put into .env by hand replaces the row
// (docs/concepts/meta-token-refresh.md §5).
type PlatformToken struct {
	ID          uint   `gorm:"primaryKey"`
	TargetName  string `gorm:"uniqueIndex;not null"`
	Platform    string
	AccessToken string `json:"-"` // base64(nonce ‖ AES-256-GCM ciphertext), see metatoken
	SeedHash    string
	// RefreshedAt is nil while the row still holds the unrefreshed seed;
	// ExpiresAt stays nil until the first refresh reports expires_in.
	RefreshedAt   *time.Time
	ExpiresAt     *time.Time
	LastAttemptAt *time.Time
	LastError     string // of the last attempt; empty after a success
	CreatedAt     time.Time
	UpdatedAt     time.Time
}
