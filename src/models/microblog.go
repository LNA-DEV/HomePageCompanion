package models

import "time"

// MicroblogPost is the local source-of-truth for a microblog entry. On create
// it is fanned out to every configured federation target (one
// MicroblogPublication row per target). Slug is used in public URLs and is
// the stable external identifier other systems (e.g., native likes,
// interactions) key on.
type MicroblogPost struct {
	ID             uint   `gorm:"primaryKey" json:"id"`
	Slug           string `gorm:"uniqueIndex" json:"slug"`
	Body           string `gorm:"type:text" json:"body"`
	ContentWarning string `json:"contentWarning"`
	ImageURL       string `json:"imageUrl"`
	ImageAltText   string `json:"imageAltText"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

// MicroblogPublication tracks one (post, target) federation attempt: its
// remote ids on success, the last error on failure, and timestamps used by
// the interactions scheduler to pace likes/comments refresh.
//
// NOTE: ExternalID has an explicit column tag (external_id) to avoid the
// GORM snake-case collision that would otherwise share `post_id` with the
// PostID foreign key. VersionId is pinned for the same robustness.
type MicroblogPublication struct {
	ID                  uint       `gorm:"primaryKey" json:"id"`
	PostID              uint       `gorm:"index" json:"postId"`
	TargetName          string     `gorm:"index" json:"targetName"`
	Platform            string     `gorm:"index" json:"platform"`
	PostUrl             *string    `json:"postUrl,omitempty"`
	ExternalID          *string    `gorm:"column:external_id" json:"externalPostId,omitempty"`
	VersionId           *string    `gorm:"column:version_id" json:"versionId,omitempty"`
	Success             bool       `gorm:"index" json:"success"`
	ErrorMessage        string     `gorm:"type:text" json:"errorMessage,omitempty"`
	LikesRefreshedAt    *time.Time `json:"likesRefreshedAt,omitempty"`
	CommentsRefreshedAt *time.Time `json:"commentsRefreshedAt,omitempty"`
	CreatedAt           time.Time  `json:"createdAt"`
	UpdatedAt           time.Time  `json:"updatedAt"`
}

// MicroblogComment is a reply imported from a federated platform. (Platform,
// ExternalID) is the dedupe key — re-imports replace nothing, so the model
// has the existing fields plus a refreshed ImportedAt.
type MicroblogComment struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	PostID     uint      `gorm:"index" json:"postId"`
	Platform   string    `gorm:"index;uniqueIndex:idx_comment_platform_external" json:"platform"`
	ExternalID string    `gorm:"uniqueIndex:idx_comment_platform_external" json:"externalId"`
	Author     string    `json:"author"`
	AuthorURL  string    `json:"authorUrl"`
	AvatarURL  string    `json:"avatarUrl"`
	Body       string    `gorm:"type:text" json:"body"`
	PostedAt   time.Time `json:"postedAt"`
	ImportedAt time.Time `json:"importedAt"`
}
