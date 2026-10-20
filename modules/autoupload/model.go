package autoupload

import "time"

// Publication states.
const (
	StatusPublished = "published"
	StatusSkipped   = "skipped" // by routing tag, or the target needs an image
	StatusManual    = "manual"  // posted by hand, marked here
)

// Publication is one post on one target instance: the state the Posts page
// shows per cell. Publications of photos no longer in the feed stay.
type Publication struct {
	ID          uint       `gorm:"primaryKey" json:"id"`
	ItemID      string     `gorm:"uniqueIndex:idx_pub_item_target;index" json:"itemId"`
	Target      string     `gorm:"uniqueIndex:idx_pub_item_target;index" json:"target"` // instance, "mastodon:main"
	Platform    string     `gorm:"index" json:"platform"`
	Source      string     `gorm:"index" json:"source"`               // who published: autoupload | microblog | backfill
	Connection  string     `gorm:"index" json:"connection,omitempty"` // autoupload instance name
	Status      string     `gorm:"index" json:"status"`
	Reason      string     `json:"reason,omitempty"`
	PostID      string     `json:"postId,omitempty"`
	PostURL     string     `json:"postUrl,omitempty"`
	Version     string     `json:"version,omitempty"`
	Likes       int        `json:"likes"`
	LikesAt     *time.Time `json:"likesAt,omitempty"`
	PublishedAt time.Time  `gorm:"index" json:"publishedAt"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
}

func (Publication) TableName() string { return "autoupload_publications" }

// Attempt is one try to publish an item on a target. A failure is open until
// a later attempt for the same item and target succeeds (ResolvedAt).
type Attempt struct {
	ID         uint       `gorm:"primaryKey" json:"id"`
	ItemID     string     `gorm:"index" json:"itemId"`
	Target     string     `gorm:"index" json:"target"`
	Platform   string     `gorm:"index" json:"platform"`
	Source     string     `gorm:"index" json:"source"`
	Connection string     `gorm:"index" json:"connection,omitempty"`
	Success    bool       `gorm:"index" json:"success"`
	Cause      string     `gorm:"index" json:"cause,omitempty"` // account | content | transient
	Code       string     `json:"code,omitempty"`
	HTTPStatus int        `json:"httpStatus,omitempty"`
	Error      string     `gorm:"type:text" json:"error,omitempty"`
	ResolvedAt *time.Time `gorm:"index" json:"resolvedAt,omitempty"`
	CreatedAt  time.Time  `gorm:"index" json:"createdAt"`
}

func (Attempt) TableName() string { return "autoupload_attempts" }

// Post is the index of every post the Posts page lists: feed items of the
// connected sources, microblog notes, and items that left their feed.
type Post struct {
	ItemID     string    `gorm:"primaryKey" json:"id"`
	Source     string    `gorm:"index" json:"source"` // "rss:photos", "microblog"
	Kind       string    `gorm:"index" json:"kind"`   // photo | article | note
	Title      string    `json:"title"`
	Summary    string    `json:"summary,omitempty"`
	Link       string    `json:"link,omitempty"`
	ImageURL   string    `json:"imageUrl,omitempty"`
	Categories []string  `gorm:"serializer:json" json:"categories,omitempty"`
	Published  time.Time `gorm:"index" json:"published"`
	InFeed     bool      `gorm:"index" json:"inFeed"`
	// WebsiteLikes are the likes given on the website itself, always shown
	// next to the platform likes, never added to them.
	WebsiteLikes int       `json:"websiteLikes"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

func (Post) TableName() string { return "autoupload_posts" }

// LikeDay is a publication's like count on one day, for the history.
type LikeDay struct {
	ItemID string `gorm:"primaryKey"`
	Target string `gorm:"primaryKey"`
	Day    string `gorm:"primaryKey"` // YYYY-MM-DD
	Likes  int
}

func (LikeDay) TableName() string { return "autoupload_like_days" }

// State is a connection's own state: paused after an account error, and
// when announcements started (older posts count as known).
type State struct {
	Connection  string `gorm:"primaryKey"`
	PausedAt    *time.Time
	PauseReason string
	Since       *time.Time
}

func (State) TableName() string { return "autoupload_state" }

// Announcement is a new post an announce connection will share.
type Announcement struct {
	ID         uint      `gorm:"primaryKey"`
	Connection string    `gorm:"uniqueIndex:idx_ann"`
	ItemID     string    `gorm:"uniqueIndex:idx_ann"`
	Round      int       `gorm:"uniqueIndex:idx_ann"` // 0, then 1 for a reshare
	DueAt      time.Time `gorm:"index"`
	DoneAt     *time.Time
}

func (Announcement) TableName() string { return "autoupload_announcements" }

// Run is one run of a connection, so "run now" can show what happened.
type Run struct {
	ID         uint       `gorm:"primaryKey" json:"id"`
	Connection string     `gorm:"index" json:"connection"`
	Trigger    string     `json:"trigger"` // schedule | manual | announce
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	Result     string     `json:"result"` // running | published | skipped | failed | nothing | paused
	ItemID     string     `json:"itemId,omitempty"`
	Title      string     `json:"title,omitempty"`
	Target     string     `json:"target"`
	PostURL    string     `json:"postUrl,omitempty"`
	Error      string     `json:"error,omitempty"`
}

func (Run) TableName() string { return "autoupload_runs" }

var tables = []any{&Publication{}, &Attempt{}, &Post{}, &LikeDay{}, &State{}, &Announcement{}, &Run{}}
