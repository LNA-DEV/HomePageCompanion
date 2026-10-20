// Package mastodon is the mastodon module: one instance per account, each a
// social.publisher/v1 and social.interactions/v1.
package mastodon

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
	"github.com/LNA-DEV/HomePageCompanion/modules/mastodon/api"
	"github.com/gin-gonic/gin"
)

func init() {
	companion.Register(companion.Info{
		ID:       "mastodon",
		Name:     "Mastodon",
		New:      func() companion.Instance { return new(Account) },
		Provides: []companion.Capability{social.PublisherV1, social.InteractionsV1},
		Routes:   routes,
		UI:       &companion.UI{Actions: []companion.Action{{Label: "Check connection", Endpoint: "check"}}},
	})
}

// lookupTimeout caps every lookup made for a mention.
const lookupTimeout = 5 * time.Second

// Account is one Mastodon account.
type Account struct {
	Instance string `json:"instance"`
	Token    string `json:"token" companion:"secret"`
	// MaxLength is the instance's post limit (500 when 0); instances that
	// allow more set it.
	MaxLength int `json:"maxLength"`
	// MaxImageBytes and MaxImageLongEdge override the image limits
	// (8 MB, 2048 px).
	MaxImageBytes    int `json:"maxImageBytes"`
	MaxImageLongEdge int `json:"maxImageLongEdge"`

	ctx      companion.Context
	selfMu   sync.Mutex
	self     string
	selfTime time.Time
}

func (a *Account) Provision(ctx companion.Context) error { a.ctx = ctx; return nil }

func (a *Account) Validate() error {
	if strings.TrimSpace(a.Instance) == "" {
		return errors.New("instance is required")
	}
	return nil
}

func (a *Account) Describe(context.Context) (social.Limits, error) {
	max := a.MaxLength
	if max <= 0 {
		max = 500
	}
	return social.Limits{
		Platform: "mastodon", Account: a.Instance, MaxLength: max, MaxImages: 4,
		LinkCard: "auto", ContentWarning: true,
		MentionProfiles: []string{"mastodon", "pixelfed"},
	}, nil
}

func (a *Account) limits() imageresize.Limits {
	lim := imageresize.DefaultsForPlatform("mastodon")
	if a.MaxImageBytes > 0 {
		lim.MaxBytes = a.MaxImageBytes
	}
	if a.MaxImageLongEdge > 0 {
		lim.MaxLongEdge = a.MaxImageLongEdge
	}
	return lim
}

// text is the status text; a link card comes from the link in the text.
func text(p social.Post) string {
	t := p.Text
	if p.Link != nil && p.Link.URL != "" && !strings.Contains(t, p.Link.URL) {
		t = strings.TrimSpace(t + "\n\n" + p.Link.URL)
	}
	return t
}

func (a *Account) Preview(_ context.Context, p social.Post) (social.Rendered, error) {
	r := social.Rendered{Text: text(p), Images: p.Images, Link: p.Link}
	if p.ContentWarning != "" {
		r.Warnings = append(r.Warnings, "shown behind the content warning: "+p.ContentWarning)
	}
	if len(p.Images) > 4 {
		r.Warnings = append(r.Warnings, "only the first 4 images are posted")
		r.Images = p.Images[:4]
	}
	return r, nil
}

func wrap(err error) error {
	if errors.Is(err, api.ErrRateLimited) {
		return fmt.Errorf("%w: %w", social.ErrRateLimited, err)
	}
	return err
}

// Publish uploads the images (fitted to the instance's limits) and creates
// a public status.
func (a *Account) Publish(ctx context.Context, p social.Post) (social.Result, error) {
	var mediaIDs []string
	for i, img := range p.Images {
		if i == 4 {
			break
		}
		data, _, err := companion.Fetch(ctx, a.ctx.HTTP(), img.URL)
		if err != nil {
			return social.Result{}, err
		}
		if prepared, err := imageresize.PrepareForTarget(data, a.limits()); err != nil {
			a.ctx.Log().Printf("%s: image prep failed for %s, sending the original: %v", a.ctx.InstanceName(), p.ItemID, err)
		} else {
			data = prepared
		}
		media, err := api.UploadMedia(a.Instance, a.Token, data, img.Alt)
		if err != nil {
			return social.Result{}, wrap(err)
		}
		mediaIDs = append(mediaIDs, media.ID)
	}
	status, err := api.CreateStatus(a.Instance, a.Token, text(p), p.ContentWarning, mediaIDs, "public")
	if err != nil {
		return social.Result{}, wrap(err)
	}
	return social.Result{ID: status.ID, URL: status.URL}, nil
}

// Delete removes a status; one that is gone already counts as deleted.
func (a *Account) Delete(_ context.Context, ref social.Result) error {
	err := api.DeleteStatus(a.Instance, a.Token, ref.ID)
	if errors.Is(err, api.ErrNotFound) {
		return nil
	}
	return wrap(err)
}

// selfAcct is the posting account's canonical acct, cached for an hour.
func (a *Account) selfAcct(ctx context.Context) (string, error) {
	a.selfMu.Lock()
	defer a.selfMu.Unlock()
	if a.self != "" && time.Since(a.selfTime) < time.Hour {
		return a.self, nil
	}
	acc, err := api.VerifyCredentials(ctx, a.Instance, a.Token)
	if err != nil {
		return "", err
	}
	self, err := profiles.FediverseSelf(ctx, a.Instance, acc.Username)
	if err != nil {
		return "", err
	}
	a.self, a.selfTime = strings.ToLower(self), time.Now()
	return a.self, nil
}

// ResolveMention reads a Mastodon or Pixelfed profile URL and asks its host
// for the canonical acct. Never the posting account itself.
func (a *Account) ResolveMention(ctx context.Context, profileURL string) (social.Mention, error) {
	acc, ok := profiles.Fediverse(profileURL)
	if !ok {
		return social.Mention{}, social.ErrNotAProfile
	}
	ctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()
	self, err := a.selfAcct(ctx)
	if err != nil {
		return social.Mention{}, fmt.Errorf("%w: %v", social.ErrIdentityUnknown, err)
	}
	mention, id, err := profiles.FediverseMention(ctx, acc)
	if err != nil {
		return social.Mention{}, err
	}
	if strings.ToLower(id) == self {
		return social.Mention{}, social.ErrSelf
	}
	return social.Mention{Text: mention, Account: id}, nil
}

func (a *Account) GetStats(_ context.Context, ref social.Result) (social.Stats, error) {
	if ref.ID == "" {
		return social.Stats{}, errors.New("mastodon: missing status id")
	}
	resp, err := api.ListStatusLikes(a.Instance, a.Token, ref.ID)
	if err != nil {
		return social.Stats{}, wrap(err)
	}
	return social.Stats{Likes: len(resp.Accounts)}, nil
}

func (a *Account) ListReplies(_ context.Context, ref social.Result) ([]social.Reply, error) {
	c, err := api.ListStatusContext(a.Instance, a.Token, ref.ID)
	if err != nil {
		return nil, wrap(err)
	}
	out := make([]social.Reply, 0, len(c.Descendants))
	for _, s := range c.Descendants {
		avatar := s.Account.AvatarStatic
		if avatar == "" {
			avatar = s.Account.Avatar
		}
		out = append(out, social.Reply{ID: s.ID, Author: s.Account.Acct, AuthorURL: s.Account.URL, AvatarURL: avatar,
			Body: s.Content, URL: s.URL, PostedAt: s.CreatedAt})
	}
	return out, nil
}

var (
	_ social.Publisher    = (*Account)(nil)
	_ social.Interactions = (*Account)(nil)
	_ social.Deleter      = (*Account)(nil)
)

func routes(mc companion.ModuleContext, r *companion.Router) {
	// check verifies an instance's token, for the settings page.
	r.Admin.POST("/check", func(c *gin.Context) {
		var body struct {
			Instance string `json:"instance"`
		}
		_ = c.ShouldBindJSON(&body)
		for _, in := range mc.Instances() {
			if in.Name != body.Instance && body.Instance != "" {
				continue
			}
			a := in.Instance.(*Account)
			acc, err := api.VerifyCredentials(c.Request.Context(), a.Instance, a.Token)
			if err != nil {
				c.JSON(http.StatusBadGateway, gin.H{"ok": false, "error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, gin.H{"ok": true, "account": "@" + acc.Username})
			return
		}
		c.JSON(http.StatusNotFound, gin.H{"error": "no such instance"})
	})
}
