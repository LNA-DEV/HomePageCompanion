// Package bluesky is the bluesky module: one instance per account, each a
// social.publisher/v1 and social.interactions/v1.
package bluesky

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/capability/social"
	"github.com/LNA-DEV/HomePageCompanion/companion"
	"github.com/LNA-DEV/HomePageCompanion/lib/imageresize"
	"github.com/LNA-DEV/HomePageCompanion/lib/profiles"
	"github.com/LNA-DEV/HomePageCompanion/modules/bluesky/api"
	"github.com/gin-gonic/gin"
)

func init() {
	companion.Register(companion.Info{
		ID:       "bluesky",
		Name:     "Bluesky",
		New:      func() companion.Instance { return new(Account) },
		Provides: []companion.Capability{social.PublisherV1, social.InteractionsV1},
		Routes:   routes,
		UI:       &companion.UI{Actions: []companion.Action{{Label: "Check connection", Endpoint: "check"}}},
	})
}

const lookupTimeout = 5 * time.Second

// sessionTTL is how long a login is reused. Bluesky limits logins (30 in
// five minutes), and an access token lives about two hours.
const sessionTTL = 90 * time.Minute

// Account is one Bluesky account.
type Account struct {
	// Username is the handle (not an e-mail): mentions look it up as is.
	Username string `json:"username"`
	// Password is an app password.
	Password string `json:"password" companion:"secret"`
	// Service is the account's PDS (https://bsky.social when empty).
	Service string   `json:"service"`
	Langs   []string `json:"langs"`

	MaxImageBytes    int `json:"maxImageBytes"`
	MaxImageLongEdge int `json:"maxImageLongEdge"`

	ctx       companion.Context
	mu        sync.Mutex
	session   *api.Session
	sessionAt time.Time
	selfDID   string
	selfAt    time.Time
}

func (a *Account) Provision(ctx companion.Context) error { a.ctx = ctx; return nil }

func (a *Account) Validate() error {
	if strings.TrimSpace(a.Username) == "" {
		return errors.New("username is required")
	}
	return nil
}

func wrap(err error) error {
	if errors.Is(err, api.ErrRateLimited) {
		return fmt.Errorf("%w: %w", social.ErrRateLimited, err)
	}
	return err
}

// login returns a reused session, or logs in again.
func (a *Account) login(fresh bool) (*api.Session, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !fresh && a.session != nil && time.Since(a.sessionAt) < sessionTTL {
		return a.session, nil
	}
	s, err := api.LoginAt(a.Service, a.Username, a.Password)
	if err != nil {
		return nil, wrap(err)
	}
	a.session, a.sessionAt = s, time.Now()
	return s, nil
}

// withSession runs f with a session; an expired token logs in once more.
func withSession[T any](a *Account, f func(*api.Session) (T, error)) (T, error) {
	s, err := a.login(false)
	if err != nil {
		var zero T
		return zero, err
	}
	v, err := f(s)
	if err != nil && (strings.Contains(err.Error(), "ExpiredToken") || strings.Contains(err.Error(), "status: 401") || strings.Contains(err.Error(), "status 401")) {
		if s, err2 := a.login(true); err2 == nil {
			return f(s)
		}
	}
	return v, wrap(err)
}

func (a *Account) Describe(context.Context) (social.Limits, error) {
	return social.Limits{Platform: "bluesky", Account: a.Username, MaxLength: 300, MaxImages: 4, LinkCard: "manual",
		MentionProfiles: []string{"bluesky"}}, nil
}

func (a *Account) limits() imageresize.Limits {
	lim := imageresize.DefaultsForPlatform("bluesky")
	if a.MaxImageBytes > 0 {
		lim.MaxBytes = a.MaxImageBytes
	}
	if a.MaxImageLongEdge > 0 {
		lim.MaxLongEdge = a.MaxImageLongEdge
	}
	return lim
}

// text prepends a content warning: Bluesky has none for text.
func text(p social.Post) string {
	if strings.TrimSpace(p.ContentWarning) != "" {
		return "CW: " + p.ContentWarning + "\n\n" + p.Text
	}
	return p.Text
}

func (a *Account) Preview(_ context.Context, p social.Post) (social.Rendered, error) {
	r := social.Rendered{Text: text(p), Images: p.Images}
	if len(p.Images) > 4 {
		r.Images = p.Images[:4]
		r.Warnings = append(r.Warnings, "only the first 4 images are posted")
	}
	if len(p.Images) == 0 && p.Link != nil {
		r.Link = p.Link
	}
	if n := len([]rune(r.Text)); n > 300 {
		r.Warnings = append(r.Warnings, fmt.Sprintf("the text has %d characters, Bluesky allows 300", n))
	}
	return r, nil
}

func (a *Account) upload(ctx context.Context, s *api.Session, url string, prep bool) (*api.BlobRef, error) {
	data, ct, err := companion.Fetch(ctx, a.ctx.HTTP(), url)
	if err != nil {
		return nil, err
	}
	if prep {
		if prepared, err := imageresize.PrepareForTarget(data, a.limits()); err != nil {
			a.ctx.Log().Printf("%s: image prep failed, sending the original: %v", a.ctx.InstanceName(), err)
		} else {
			data, ct = prepared, "image/jpeg"
		}
	}
	if ct == "" || !strings.HasPrefix(ct, "image/") {
		ct = "image/jpeg"
	}
	return api.UploadImage(s, data, ct)
}

// Publish posts the images, or for a post without images the link card
// (the sender's title, description and preview image).
func (a *Account) Publish(ctx context.Context, p social.Post) (social.Result, error) {
	mentions := map[string]string{}
	for _, m := range p.Mentions {
		mentions[m.Text] = m.Account
	}
	ref, err := withSession(a, func(s *api.Session) (*api.PostRef, error) {
		var embed map[string]any
		if len(p.Images) > 0 {
			var imgs []api.Image
			for i, img := range p.Images {
				if i == 4 {
					break
				}
				blob, err := a.upload(ctx, s, img.URL, true)
				if err != nil {
					return nil, err
				}
				imgs = append(imgs, api.Image{Blob: blob, Alt: img.Alt})
			}
			embed = api.ImagesEmbed(imgs)
		} else if p.Link != nil && p.Link.URL != "" {
			var thumb *api.BlobRef
			if p.Link.ImageURL != "" {
				var err error
				if thumb, err = a.upload(ctx, s, p.Link.ImageURL, true); err != nil {
					a.ctx.Log().Printf("%s: link card without preview image: %v", a.ctx.InstanceName(), err)
					thumb = nil
				}
			}
			embed = api.ExternalEmbed(p.Link.URL, p.Link.Title, p.Link.Description, thumb)
		}
		return api.CreateRecord(s, text(p), embed, a.Langs, time.Now(), mentions)
	})
	if err != nil {
		return social.Result{}, err
	}
	handle := a.Username
	if a.session != nil && a.session.Handle != "" {
		handle = a.session.Handle
	}
	return social.Result{ID: ref.URI, Version: ref.CID, URL: api.PostWebURL(handle, ref.URI)}, nil
}

func (a *Account) Delete(_ context.Context, ref social.Result) error {
	_, err := withSession(a, func(s *api.Session) (struct{}, error) {
		return struct{}{}, api.DeleteRecord(s, ref.ID)
	})
	return err
}

// ResolveMention looks up the handle or DID of a bsky.app profile URL. The
// mention shows the current handle; the facet carries the DID.
func (a *Account) ResolveMention(ctx context.Context, profileURL string) (social.Mention, error) {
	acc, ok := profiles.Bluesky(profileURL)
	if !ok {
		return social.Mention{}, social.ErrNotAProfile
	}
	ctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()
	self, err := a.self(ctx)
	if err != nil {
		return social.Mention{}, fmt.Errorf("%w: %v", social.ErrIdentityUnknown, err)
	}
	p, err := api.GetProfile(ctx, acc.Name)
	if err != nil {
		return social.Mention{}, err
	}
	if p.Handle == "handle.invalid" {
		return social.Mention{}, fmt.Errorf("%s has no valid handle", p.Did)
	}
	if p.Did == self {
		return social.Mention{}, social.ErrSelf
	}
	return social.Mention{Text: "@" + p.Handle, Account: p.Did}, nil
}

// self is the posting account's DID, cached for an hour.
func (a *Account) self(ctx context.Context) (string, error) {
	a.mu.Lock()
	if a.selfDID != "" && time.Since(a.selfAt) < time.Hour {
		defer a.mu.Unlock()
		return a.selfDID, nil
	}
	a.mu.Unlock()
	p, err := api.GetProfile(ctx, a.Username)
	if err != nil {
		return "", err
	}
	a.mu.Lock()
	a.selfDID, a.selfAt = p.Did, time.Now()
	a.mu.Unlock()
	return p.Did, nil
}

func (a *Account) GetStats(_ context.Context, ref social.Result) (social.Stats, error) {
	if ref.ID == "" || ref.Version == "" {
		return social.Stats{}, errors.New("bluesky: post URI or CID missing")
	}
	resp, err := withSession(a, func(s *api.Session) (*api.LikesResponse, error) {
		return api.ListPostLikes(s, ref.ID, ref.Version)
	})
	if err != nil {
		return social.Stats{}, err
	}
	return social.Stats{Likes: len(resp.Likes)}, nil
}

func (a *Account) ListReplies(_ context.Context, ref social.Result) ([]social.Reply, error) {
	view, err := withSession(a, func(s *api.Session) (*api.ThreadView, error) {
		return api.GetPostThread(s, ref.ID, 1)
	})
	if err != nil {
		return nil, err
	}
	var out []social.Reply
	for _, r := range view.Thread.Replies {
		p := r.Post
		if p.URI == "" {
			continue
		}
		out = append(out, social.Reply{ID: p.URI, Author: p.Author.Handle, AuthorURL: "https://bsky.app/profile/" + p.Author.Handle,
			AvatarURL: p.Author.Avatar, Body: p.Record.Text, URL: api.PostWebURL(p.Author.Handle, p.URI), PostedAt: p.Record.CreatedAt})
	}
	return out, nil
}

// ListPosts returns the account's posts for the backfill.
func (a *Account) ListPosts(context.Context) ([]social.ListedPost, error) {
	items, err := withSession(a, func(s *api.Session) ([]api.FeedItem, error) {
		return api.ListAllAuthorPosts(s, s.Did)
	})
	if err != nil {
		return nil, err
	}
	handle := a.Username
	out := make([]social.ListedPost, 0, len(items))
	for _, it := range items {
		lp := social.ListedPost{Ref: social.Result{ID: it.Post.URI, Version: it.Post.CID, URL: api.PostWebURL(handle, it.Post.URI)}}
		for _, img := range it.Post.Embed.Images {
			if img.Fullsize != "" {
				lp.ImageURLs = append(lp.ImageURLs, img.Fullsize)
			}
		}
		out = append(out, lp)
	}
	return out, nil
}

var (
	_ social.Publisher    = (*Account)(nil)
	_ social.Interactions = (*Account)(nil)
	_ social.Deleter      = (*Account)(nil)
	_ social.Lister       = (*Account)(nil)
)

func routes(mc companion.ModuleContext, r *companion.Router) {
	r.Admin.POST("/check", func(c *gin.Context) {
		var body struct {
			Instance string `json:"instance"`
		}
		_ = c.ShouldBindJSON(&body)
		for _, in := range mc.Instances() {
			if in.Name != body.Instance && body.Instance != "" {
				continue
			}
			s, err := in.Instance.(*Account).login(true)
			if err != nil {
				c.JSON(http.StatusBadGateway, gin.H{"ok": false, "error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, gin.H{"ok": true, "account": "@" + s.Handle})
			return
		}
		c.JSON(http.StatusNotFound, gin.H{"error": "no such instance"})
	})
}
