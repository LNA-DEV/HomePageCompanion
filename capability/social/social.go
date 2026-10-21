// Package social defines the capabilities of social networks:
// social.publisher/v1 (post) and social.interactions/v1 (likes and replies),
// and the events about posts that building blocks exchange.
package social

import (
	"context"
	"errors"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/companion"
)

// Publisher posts to one account on a network.
type Publisher interface {
	// Describe reports what the target accepts.
	Describe(ctx context.Context) (Limits, error)
	// Preview renders the post as the platform will show it, without
	// sending anything.
	Preview(ctx context.Context, p Post) (Rendered, error)
	// Publish sends the post.
	Publish(ctx context.Context, p Post) (Result, error)
	// ResolveMention turns a profile URL into a mention on this network.
	// ErrNotAProfile: the URL is not a profile the network can mention, try
	// the next one. ErrSelf: the profile is the posting account.
	// ErrIdentityUnknown: the posting account's identity could not be
	// read, so nobody can be mentioned safely.
	ResolveMention(ctx context.Context, profileURL string) (Mention, error)
}

// Lister is the optional listPosts operation of social.publisher/v1: the
// account's own posts, newest first, for matching old posts to feed items.
type Lister interface {
	ListPosts(ctx context.Context) ([]ListedPost, error)
}

// Deleter is the optional delete operation of social.publisher/v1. A post
// that is already gone counts as deleted.
type Deleter interface {
	Delete(ctx context.Context, ref Result) error
}

// Interactions reads what happened to a published post.
type Interactions interface {
	GetStats(ctx context.Context, ref Result) (Stats, error)
	ListReplies(ctx context.Context, ref Result) ([]Reply, error)
}

var (
	PublisherV1    = companion.NewCapability[Publisher]("social.publisher", 1)
	InteractionsV1 = companion.NewCapability[Interactions]("social.interactions", 1)
)

var (
	ErrNotAProfile     = errors.New("not a profile this network can mention")
	ErrSelf            = errors.New("profile is the posting account")
	ErrIdentityUnknown = errors.New("own identity unknown")
	// ErrRateLimited marks a refused call that may be repeated later.
	ErrRateLimited = errors.New("rate limited")
	// ErrNeedsImage is returned by Publish for a post without an image on a
	// target with RequiresImage.
	ErrNeedsImage = errors.New("needs an image")

	// ErrNotConnected is returned when an account has neither a connected
	// OAuth token nor one in the config, or its connection broke (the
	// platform refused to refresh it). Only the operator can fix it.
	ErrNotConnected = errors.New("no account connected")
)

// Post is what a building block wants posted.
type Post struct {
	ItemID string `json:"itemId"`
	Kind   string `json:"kind"` // photo | article | note
	Text   string `json:"text"`
	// ContentWarning is shown before the text where the network supports
	// it, else prepended as "CW: …".
	ContentWarning string    `json:"contentWarning,omitempty"`
	Images         []Image   `json:"images"`         // may be empty (blog post)
	Link           *LinkCard `json:"link,omitempty"` // URL, title, description, preview image
	// Mentions are the mentions the text contains, as ResolveMention
	// returned them; networks that need facets or photo tags read them here.
	Mentions []Mention `json:"mentions,omitempty"`
}

// Image is an image to attach. URL is a public http(s) URL or a URL of the
// core (companion.Fetch resolves both); Instagram and Threads hand it to
// the platform, which fetches it itself.
type Image struct {
	URL string `json:"url"`
	Alt string `json:"alt,omitempty"`
}

// LinkCard is the preview of a link.
type LinkCard struct {
	URL         string `json:"url"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	ImageURL    string `json:"imageUrl,omitempty"`
}

// Mention is a person mentioned in a post.
type Mention struct {
	Text    string `json:"text"`    // as in the post: "@jane", "@jane@mastodon.social"
	Account string `json:"account"` // DID, username or canonical acct
}

// Limits is what a target accepts.
type Limits struct {
	Platform string `json:"platform"` // network key: mastodon, bluesky …
	Account  string `json:"account,omitempty"`

	MaxLength int `json:"maxLength"`
	// LengthUnit is how the network counts: "rune" (default) or
	// "utf8-emoji" (Threads: an emoji counts as its UTF-8 bytes).
	LengthUnit  string `json:"lengthUnit,omitempty"`
	MaxHashtags int    `json:"maxHashtags,omitempty"` // 0 = unlimited

	RequiresImage bool `json:"requiresImage,omitempty"`
	MaxImages     int  `json:"maxImages,omitempty"`
	// LinkCard: "auto" (the network builds the card from a link in the
	// text), "manual" (the sender supplies it) or "" (none).
	LinkCard       string `json:"linkCard,omitempty"`
	ContentWarning bool   `json:"contentWarning,omitempty"` // native support

	// MentionProfiles are the profile kinds (lna:profile platforms) the
	// network can mention, preferred first; MaxMentions caps them per post.
	MentionProfiles []string `json:"mentionProfiles,omitempty"`
	MaxMentions     int      `json:"maxMentions,omitempty"`
}

// Rendered is a preview.
type Rendered struct {
	Text     string    `json:"text"`
	Images   []Image   `json:"images,omitempty"`
	Link     *LinkCard `json:"link,omitempty"`
	Warnings []string  `json:"warnings,omitempty"`
}

// Result identifies a published post on its network. ID is the network's
// id (a Mastodon status id, an Instagram media id, a Bluesky at:// URI),
// URL its public web page, Version a network-specific version (Bluesky's
// CID).
type Result struct {
	ID      string `json:"id"`
	URL     string `json:"url,omitempty"`
	Version string `json:"version,omitempty"`
}

// Stats are a post's numbers.
type Stats struct {
	Likes   int `json:"likes"`
	Replies int `json:"replies,omitempty"`
}

// Reply is a reply to a post.
type Reply struct {
	ID        string    `json:"id"`
	Author    string    `json:"author"`
	AuthorURL string    `json:"authorUrl,omitempty"`
	AvatarURL string    `json:"avatarUrl,omitempty"`
	Body      string    `json:"body"`
	URL       string    `json:"url,omitempty"`
	PostedAt  time.Time `json:"postedAt"`
}

// ListedPost is one of the account's own posts.
type ListedPost struct {
	Ref       Result    `json:"ref"`
	Text      string    `json:"text,omitempty"`
	ImageURLs []string  `json:"imageUrls,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}
