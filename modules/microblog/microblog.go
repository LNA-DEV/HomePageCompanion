// Package microblog is the microblog module: posts written in the admin UI,
// published to every target of its publishTo slot (social.publisher/v1),
// with replies imported from the targets and likes from the interactions
// module. The website reads them under the old /api/microblog/ URLs.
package microblog

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/capability/social"
	"github.com/LNA-DEV/HomePageCompanion/companion"
	"gorm.io/gorm"
)

func init() {
	companion.Register(companion.Info{
		ID:   "microblog",
		Name: "Microblog",
		New:  func() companion.Instance { return new(Blog) },
		Slots: []companion.Slot{
			{Name: "publishTo", Needs: social.PublisherV1, Multiple: true, Optional: true},
		},
		Subscribes: []string{social.EventStatsUpdated},
		OnEvent:    onEvent,
		Migrate:    migrate,
		Routes:     routes,
		Aliases:    []string{"/api/microblog"},
		Import:     importLegacy,
		UI: &companion.UI{
			Nav:   []companion.NavEntry{{Label: "Microblog", Icon: "message-square", Page: "", Group: "Content", Order: 20}},
			Pages: []companion.Page{{Path: "", Element: "microblog-page", Title: "Microblog"}},
		},
	})
}

// Post is a microblog entry. Slug is its stable public id; likes and
// replies key on it.
type Post struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	Slug           string    `gorm:"uniqueIndex" json:"slug"`
	Body           string    `gorm:"type:text" json:"body"`
	ContentWarning string    `json:"contentWarning"`
	ImageURL       string    `json:"imageUrl"`
	ImageAltText   string    `json:"imageAltText"`
	NativeLikes    int       `json:"nativeLikes"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

func (Post) TableName() string { return "microblog_posts" }

// Publication is one (post, target) federation attempt.
type Publication struct {
	ID                  uint       `gorm:"primaryKey" json:"id"`
	PostID              uint       `gorm:"index" json:"postId"`
	Target              string     `gorm:"index" json:"target"`     // "mastodon:main"
	TargetName          string     `gorm:"index" json:"targetName"` // "main"
	Platform            string     `gorm:"index" json:"platform"`
	PostUrl             *string    `json:"postUrl,omitempty"`
	ExternalID          *string    `gorm:"column:external_id" json:"externalPostId,omitempty"`
	VersionId           *string    `gorm:"column:version_id" json:"versionId,omitempty"`
	Success             bool       `gorm:"index" json:"success"`
	ErrorMessage        string     `gorm:"type:text" json:"errorMessage,omitempty"`
	Likes               int        `json:"likes"`
	LikesRefreshedAt    *time.Time `json:"likesRefreshedAt,omitempty"`
	CommentsRefreshedAt *time.Time `json:"commentsRefreshedAt,omitempty"`
	CreatedAt           time.Time  `json:"createdAt"`
	UpdatedAt           time.Time  `json:"updatedAt"`
}

func (Publication) TableName() string { return "microblog_publications" }

func (p Publication) ref() social.Result {
	return social.Result{ID: deref(p.ExternalID), URL: deref(p.PostUrl), Version: deref(p.VersionId)}
}

// Comment is a reply imported from a network; (Platform, ExternalID) is the
// dedupe key.
type Comment struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	PostID     uint      `gorm:"index" json:"postId"`
	Platform   string    `gorm:"index;uniqueIndex:idx_mb_comment" json:"platform"`
	ExternalID string    `gorm:"uniqueIndex:idx_mb_comment" json:"externalId"`
	Author     string    `json:"author"`
	AuthorURL  string    `json:"authorUrl"`
	AvatarURL  string    `json:"avatarUrl"`
	Body       string    `gorm:"type:text" json:"body"`
	PostedAt   time.Time `json:"postedAt"`
	ImportedAt time.Time `json:"importedAt"`
}

func (Comment) TableName() string { return "microblog_comments" }

func migrate(mc companion.ModuleContext) error {
	if mc.DB() == nil {
		return errors.New("microblog: no database")
	}
	return mc.DB().AutoMigrate(&Post{}, &Publication{}, &Comment{})
}

// MaxBodyChars is Mastodon's default ceiling.
const MaxBodyChars = 500

// mediaPath is where the website reads uploaded images.
const mediaPath = "/api/microblog/media/"

// Blog is the module's one instance.
type Blog struct {
	PublishTo    map[string]social.Publisher `companion:"slot=publishTo"`
	PublishToIDs []string                    `companion:"slotid=publishTo"`
	// MaxBodyChars caps a post (500 when 0).
	MaxBodyChars int `json:"maxBodyChars"`

	ctx companion.Context
	mu  sync.Mutex
}

func (b *Blog) Provision(ctx companion.Context) error {
	b.ctx = ctx
	if b.MaxBodyChars <= 0 {
		b.MaxBodyChars = MaxBodyChars
	}
	return migrate(ctx)
}

func (b *Blog) Jobs() []companion.Job {
	return []companion.Job{{Name: "replies", Schedule: "0 */5 * * * *", Timeout: 10 * time.Minute, Run: b.refreshStalest}}
}

func (b *Blog) db() *gorm.DB { return b.ctx.DB() }

// Create stores the post and publishes it to every target.
func (b *Blog) Create(ctx context.Context, body, cw, imageURL, alt string) (*Post, []Publication, error) {
	body = strings.TrimSpace(body)
	if body == "" && strings.TrimSpace(imageURL) == "" {
		return nil, nil, errors.New("microblog: post must have body or image")
	}
	if len([]rune(body)) > b.MaxBodyChars {
		return nil, nil, fmt.Errorf("microblog: body exceeds %d characters", b.MaxBodyChars)
	}
	var post *Post
	for i := 0; i < 5; i++ {
		p := &Post{Slug: newSlug(), Body: body, ContentWarning: cw, ImageURL: imageURL, ImageAltText: alt}
		err := b.db().Create(p).Error
		if err == nil {
			post = p
			break
		}
		if !strings.Contains(strings.ToLower(err.Error()), "unique") {
			return nil, nil, err
		}
	}
	if post == nil {
		return nil, nil, errors.New("microblog: could not allocate a slug")
	}
	var pubs []Publication
	for _, id := range b.PublishToIDs {
		_, name := companion.SplitInstanceID(id)
		pub := Publication{PostID: post.ID, Target: id, TargetName: name}
		if lim, err := b.PublishTo[id].Describe(ctx); err == nil {
			pub.Platform = lim.Platform
		}
		b.db().Create(&pub)
		pubs = append(pubs, b.publish(ctx, post, &pub))
	}
	return post, pubs, nil
}

// image resolves the post's image for a network: an uploaded image goes
// through a temp URL of the core (Threads and Instagram fetch it
// themselves, so it must be absolute), any other URL as it is.
func (b *Blog) image(ctx context.Context, post *Post) (string, error) {
	name, ok := strings.CutPrefix(post.ImageURL, mediaPath)
	if !ok {
		return post.ImageURL, nil
	}
	rc, info, err := b.ctx.Blobs().Get(ctx, name)
	if err != nil {
		return "", fmt.Errorf("image %s: %w", name, err)
	}
	defer rc.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(rc); err != nil {
		return "", err
	}
	return b.ctx.Temp().Put(ctx, buf.Bytes(), info.ContentType, 6*time.Hour)
}

func (b *Blog) publish(ctx context.Context, post *Post, pub *Publication) Publication {
	target, ok := b.PublishTo[pub.Target]
	var res social.Result
	var err error
	if !ok {
		err = fmt.Errorf("microblog: target %s is no longer configured", pub.Target)
	} else {
		p := social.Post{ItemID: post.Slug, Kind: "note", Text: post.Body, ContentWarning: post.ContentWarning}
		if strings.TrimSpace(post.ImageURL) != "" {
			var url string
			url, err = b.image(ctx, post)
			p.Images = []social.Image{{URL: url, Alt: post.ImageAltText}}
		}
		if err == nil {
			res, err = target.Publish(ctx, p)
		}
	}
	now := time.Now()
	title := firstLine(post.Body)
	if err == nil {
		pub.Success, pub.ErrorMessage = true, ""
		pub.PostUrl, pub.ExternalID = strPtr(res.URL), strPtr(res.ID)
		pub.VersionId = nil
		if res.Version != "" {
			pub.VersionId = strPtr(res.Version)
		}
		b.ctx.Log().Printf("published slug=%s to %s: %s", post.Slug, pub.Target, res.URL)
		_ = b.ctx.Emit(ctx, social.EventPostPublished, social.PostPublished{ItemID: post.Slug, Kind: "note", Title: title, Source: "microblog",
			Target: pub.Target, Platform: pub.Platform, Ref: res, Time: now})
	} else {
		pub.Success, pub.ErrorMessage = false, err.Error()
		b.ctx.Log().Printf("publish slug=%s to %s failed: %v", post.Slug, pub.Target, err)
		f := social.Classify(err)
		_ = b.ctx.Emit(ctx, social.EventPostFailed, social.PostFailed{ItemID: post.Slug, Kind: "note", Title: title, Source: "microblog",
			Target: pub.Target, Platform: pub.Platform, Error: err.Error(), Cause: f.Cause, HTTPStatus: f.HTTPStatus, Time: now})
	}
	b.db().Save(pub)
	return *pub
}

// Retry publishes a failed publication again.
func (b *Blog) Retry(ctx context.Context, publicationID uint) (Publication, error) {
	var pub Publication
	if err := b.db().First(&pub, publicationID).Error; err != nil {
		return Publication{}, err
	}
	if pub.Success {
		return pub, errors.New("microblog: publication already succeeded")
	}
	var post Post
	if err := b.db().First(&post, pub.PostID).Error; err != nil {
		return Publication{}, err
	}
	return b.publish(ctx, &post, &pub), nil
}

// Delete removes a post: on every network where it went out (best effort,
// a post already gone counts as deleted), then here.
func (b *Blog) Delete(ctx context.Context, postID uint) error {
	var post Post
	if err := b.db().First(&post, postID).Error; err != nil {
		return err
	}
	var pubs []Publication
	b.db().Where("post_id = ?", postID).Find(&pubs)
	for _, pub := range pubs {
		if !pub.Success || pub.ExternalID == nil {
			continue
		}
		if d, ok := b.PublishTo[pub.Target].(social.Deleter); ok {
			if err := d.Delete(ctx, pub.ref()); err != nil {
				b.ctx.Log().Printf("remote delete on %s failed: %v", pub.Target, err)
			}
		} else {
			b.ctx.Log().Printf("cannot remote-delete on %s", pub.Target)
		}
		_ = b.ctx.Emit(ctx, social.EventPostRemoved, social.PostRemoved{ItemID: post.Slug, Source: "microblog", Target: pub.Target, Time: time.Now()})
	}
	return b.db().Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("post_id = ?", postID).Delete(&Comment{}).Error; err != nil {
			return err
		}
		if err := tx.Where("post_id = ?", postID).Delete(&Publication{}).Error; err != nil {
			return err
		}
		return tx.Delete(&Post{}, postID).Error
	})
}

// interactionsOf is the target's social.interactions/v1, when it offers one.
func (b *Blog) interactionsOf(target string) social.Interactions {
	v, err := b.ctx.Capability(target, social.InteractionsV1)
	if err != nil {
		return nil
	}
	i, _ := v.(social.Interactions)
	return i
}

// refreshStalest imports the replies of the publications not refreshed for
// the longest time, five per run.
func (b *Blog) refreshStalest(ctx context.Context) error {
	var pubs []Publication
	b.db().Where("success = ?", true).Order("comments_refreshed_at IS NOT NULL, comments_refreshed_at").Limit(5).Find(&pubs)
	for i, pub := range pubs {
		if i > 0 {
			time.Sleep(time.Second)
		}
		if err := b.refreshReplies(ctx, pub); err != nil {
			b.ctx.Log().Printf("replies of %s on %s: %v", pub.TargetName, pub.Target, err)
		}
	}
	return nil
}

// RefreshPost imports likes and replies of one post right away.
func (b *Blog) RefreshPost(ctx context.Context, postID uint) error {
	if !b.mu.TryLock() {
		return errBusy
	}
	defer b.mu.Unlock()
	var pubs []Publication
	b.db().Where("post_id = ? AND success = ?", postID, true).Find(&pubs)
	var first error
	for _, pub := range pubs {
		in := b.interactionsOf(pub.Target)
		if in == nil {
			continue
		}
		if s, err := in.GetStats(ctx, pub.ref()); err == nil {
			now := time.Now()
			b.db().Model(&Publication{}).Where("id = ?", pub.ID).Updates(map[string]any{"likes": s.Likes, "likes_refreshed_at": now})
		} else if first == nil {
			first = err
		}
		if err := b.refreshReplies(ctx, pub); err != nil && first == nil {
			first = err
		}
	}
	return first
}

var errBusy = errors.New("another refresh is running, try again in a moment")

func (b *Blog) refreshReplies(ctx context.Context, pub Publication) error {
	in := b.interactionsOf(pub.Target)
	if in == nil {
		return nil
	}
	replies, err := in.ListReplies(ctx, pub.ref())
	now := time.Now()
	b.db().Model(&Publication{}).Where("id = ?", pub.ID).Update("comments_refreshed_at", now)
	if err != nil {
		return err
	}
	for _, r := range replies {
		c := Comment{PostID: pub.PostID, Platform: pub.Platform, ExternalID: r.ID, Author: r.Author, AuthorURL: r.AuthorURL,
			AvatarURL: r.AvatarURL, Body: r.Body, PostedAt: r.PostedAt, ImportedAt: now}
		var existing Comment
		if b.db().Where("platform = ? AND external_id = ?", c.Platform, c.ExternalID).Limit(1).Find(&existing).RowsAffected > 0 {
			b.db().Model(&existing).Updates(map[string]any{"author": c.Author, "author_url": c.AuthorURL, "avatar_url": c.AvatarURL, "body": c.Body, "imported_at": now})
			continue
		}
		b.db().Create(&c)
	}
	return nil
}

// onEvent keeps the likes the interactions module counts.
func onEvent(mc companion.ModuleContext, ev companion.Event) error {
	var s social.StatsUpdated
	if err := ev.Decode(&s); err != nil {
		return err
	}
	var post Post
	if mc.DB().Where("slug = ?", s.ItemID).Limit(1).Find(&post).RowsAffected == 0 {
		return nil
	}
	if s.Target == social.WebsiteTarget {
		return mc.DB().Model(&post).Update("native_likes", s.Likes).Error
	}
	return mc.DB().Model(&Publication{}).Where("post_id = ? AND target = ?", post.ID, s.Target).
		Updates(map[string]any{"likes": s.Likes, "likes_refreshed_at": s.Time}).Error
}

// newSlug builds a slug like 20260515T101530Z-3z6rmt that sorts roughly by
// creation time and is filesystem-safe.
func newSlug() string {
	ts := time.Now().UTC().Format("20060102T150405Z")
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ts
	}
	return ts + "-" + strings.ToLower(strings.TrimRight(base32.StdEncoding.EncodeToString(b[:]), "="))
}

func strPtr(s string) *string { return &s }

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if r := []rune(s); len(r) > 80 {
		s = string(r[:79]) + "…"
	}
	return s
}

func mediaName(ext string, sum string) string {
	if ext == "" {
		ext = ".bin"
	}
	return path.Clean(sum + ext)
}
