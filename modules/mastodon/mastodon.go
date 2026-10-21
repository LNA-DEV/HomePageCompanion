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
	"github.com/LNA-DEV/HomePageCompanion/lib/fediauth"
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
	oauth    *fediauth.Conn
	selfMu   sync.Mutex
	self     string
	selfTime time.Time
}

// oauthScopes are what the module calls: posting with images and deleting
// (write:statuses, write:media), the account (read:accounts), likes and
// replies (read:statuses).
var oauthScopes = []string{"read:accounts", "read:statuses", "write:media", "write:statuses"}

func (a *Account) Provision(ctx companion.Context) error {
	a.ctx = ctx
	a.oauth = fediauth.New(ctx, oauthScopes)
	return nil
}

// token is the connected account's token, or else the configured one.
func (a *Account) token(ctx context.Context) (string, error) {
	if tok, ok, err := a.oauth.Token(ctx); ok || err != nil {
		return tok, err
	}
	if a.Token == "" {
		return "", fmt.Errorf("mastodon %s: %w: connect it on its settings page or set token", a.ctx.InstanceName(), social.ErrNotConnected)
	}
	return a.Token, nil
}

func (a *Account) OAuthClient(ctx context.Context, redirectURI string) (companion.OAuthClient, error) {
	return a.oauth.Client(ctx, a.Instance, redirectURI)
}

// OAuthConnect checks the new token and keeps it; the account is
// @user@server.
func (a *Account) OAuthConnect(ctx context.Context, client companion.OAuthClient, tok companion.OAuthToken) (string, error) {
	acc, err := api.VerifyCredentials(ctx, a.Instance, tok.AccessToken)
	if err != nil {
		return "", err
	}
	name := "@" + acc.Username + "@" + host(a.Instance)
	a.forgetSelf()
	return name, a.oauth.Connect(client, tok, name, acc.ID)
}

func (a *Account) OAuthState() companion.OAuthState { return a.oauth.State(a.Token != "") }

func (a *Account) OAuthDisconnect(ctx context.Context) error {
	a.forgetSelf()
	return a.oauth.Disconnect(ctx, a.Instance)
}

func (a *Account) forgetSelf() {
	a.selfMu.Lock()
	a.self = ""
	a.selfMu.Unlock()
}

func host(instance string) string {
	h := strings.TrimRight(strings.TrimSpace(instance), "/")
	if i := strings.Index(h, "://"); i >= 0 {
		h = h[i+3:]
	}
	return h
}

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
	tok, err := a.token(ctx)
	if err != nil {
		return social.Result{}, err
	}
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
		media, err := api.UploadMedia(a.Instance, tok, data, img.Alt)
		if err != nil {
			return social.Result{}, wrap(err)
		}
		mediaIDs = append(mediaIDs, media.ID)
	}
	status, err := api.CreateStatus(a.Instance, tok, text(p), p.ContentWarning, mediaIDs, "public", p.IdempotencyKey)
	if err != nil {
		return social.Result{}, wrap(err)
	}
	return social.Result{ID: status.ID, URL: status.URL}, nil
}

// Delete removes a status; one that is gone already counts as deleted.
func (a *Account) Delete(ctx context.Context, ref social.Result) error {
	tok, err := a.token(ctx)
	if err != nil {
		return err
	}
	err = api.DeleteStatus(a.Instance, tok, ref.ID)
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
	tok, err := a.token(ctx)
	if err != nil {
		return "", err
	}
	acc, err := api.VerifyCredentials(ctx, a.Instance, tok)
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

func (a *Account) GetStats(ctx context.Context, ref social.Result) (social.Stats, error) {
	if ref.ID == "" {
		return social.Stats{}, errors.New("mastodon: missing status id")
	}
	tok, err := a.token(ctx)
	if err != nil {
		return social.Stats{}, err
	}
	resp, err := api.ListStatusLikes(a.Instance, tok, ref.ID)
	if err != nil {
		return social.Stats{}, wrap(err)
	}
	return social.Stats{Likes: len(resp.Accounts)}, nil
}

func (a *Account) ListReplies(ctx context.Context, ref social.Result) ([]social.Reply, error) {
	tok, err := a.token(ctx)
	if err != nil {
		return nil, err
	}
	c, err := api.ListStatusContext(a.Instance, tok, ref.ID)
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

	_ companion.OAuthConnector = (*Account)(nil)
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
			tok, err := a.token(c.Request.Context())
			if err != nil {
				c.JSON(http.StatusBadGateway, gin.H{"ok": false, "error": err.Error()})
				return
			}
			acc, err := api.VerifyCredentials(c.Request.Context(), a.Instance, tok)
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
