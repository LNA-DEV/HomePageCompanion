package social

import "time"

// Event types about posts. Every building block that publishes emits
// PostPublished or PostFailed, so the autoupload Posts page and the like
// counting see microblog posts as well as photos.
const (
	EventPostPublished = "social.post.published/v1"
	EventPostFailed    = "social.post.failed/v1"
	EventPostRemoved   = "social.post.removed/v1"
	EventStatsUpdated  = "social.stats.updated/v1"
)

// PostPublished: a post went out.
type PostPublished struct {
	ItemID     string    `json:"itemId"`
	Kind       string    `json:"kind"` // photo | article | note
	Title      string    `json:"title,omitempty"`
	Link       string    `json:"link,omitempty"`
	Source     string    `json:"source"`               // the publishing building block: "autoupload", "microblog"
	Connection string    `json:"connection,omitempty"` // its instance, e.g. "autoupload:photos-mastodon"
	Target     string    `json:"target"`               // the publisher instance, "mastodon:main"
	Platform   string    `json:"platform"`
	Ref        Result    `json:"ref"`
	Time       time.Time `json:"time"`
}

// PostFailed: a post did not go out.
type PostFailed struct {
	ItemID     string    `json:"itemId"`
	Kind       string    `json:"kind"`
	Title      string    `json:"title,omitempty"`
	Link       string    `json:"link,omitempty"`
	Source     string    `json:"source"`
	Connection string    `json:"connection,omitempty"`
	Target     string    `json:"target"`
	Platform   string    `json:"platform"`
	Error      string    `json:"error"`
	Cause      string    `json:"cause"` // account | content | transient
	HTTPStatus int       `json:"httpStatus,omitempty"`
	Time       time.Time `json:"time"`
}

// PostRemoved: a publication no longer counts (deleted on the network, or
// released for reposting).
type PostRemoved struct {
	ItemID string    `json:"itemId"`
	Source string    `json:"source"`
	Target string    `json:"target"`
	Time   time.Time `json:"time"`
}

// StatsUpdated: new numbers for a published item on one target. Target
// "website" carries the likes given on the website itself.
type StatsUpdated struct {
	ItemID   string    `json:"itemId"`
	Source   string    `json:"source"` // who published it: "autoupload", "microblog"
	Target   string    `json:"target"`
	Platform string    `json:"platform"`
	Likes    int       `json:"likes"`
	Time     time.Time `json:"time"`
}

// WebsiteTarget is the Target of StatsUpdated for likes on the website.
const WebsiteTarget = "website"
