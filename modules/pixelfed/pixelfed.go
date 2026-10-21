// Package pixelfed is the pixelfed module: one instance per account, each a
// social.publisher/v1 (images only) and social.interactions/v1.
package pixelfed

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/capability/social"
	"github.com/LNA-DEV/HomePageCompanion/companion"
	"github.com/LNA-DEV/HomePageCompanion/lib/fediauth"
	"github.com/LNA-DEV/HomePageCompanion/lib/imageresize"
	"github.com/LNA-DEV/HomePageCompanion/lib/profiles"
	"github.com/LNA-DEV/HomePageCompanion/modules/pixelfed/api"
	"github.com/gin-gonic/gin"
)

func init() {
	companion.Register(companion.Info{
		ID:       "pixelfed",
		Name:     "Pixelfed",
		New:      func() companion.Instance { return new(Account) },
		Provides: []companion.Capability{social.PublisherV1, social.InteractionsV1},
		Routes:   routes,
		UI:       &companion.UI{Actions: []companion.Action{{Label: "Check connection", Endpoint: "check"}}},
	})
}

const lookupTimeout = 5 * time.Second

// Account is one Pixelfed account.
type Account struct {
	Instance         string `json:"instance"`
	Token            string `json:"token" companion:"secret"`
	MaxLength        int    `json:"maxLength"` // 2000 when 0
	MaxImageBytes    int    `json:"maxImageBytes"`
	MaxImageLongEdge int    `json:"maxImageLongEdge"`

	ctx      companion.Context
	oauth    *fediauth.Conn
	selfMu   sync.Mutex
	self     string
	selfTime time.Time
}

// oauthScopes: Pixelfed has no granular scopes; its endpoints check read
// and write. Its tokens expire after a year and come with a refresh token.
var oauthScopes = []string{"read", "write"}

func (a *Account) Provision(ctx companion.Context) error {
	a.ctx = ctx
	a.oauth = fediauth.New(ctx, oauthScopes)
	a.oauth.Revoke = false // Pixelfed has no /oauth/revoke
	return nil
}

// token is the connected account's token, or else the configured one.
func (a *Account) token(ctx context.Context) (string, error) {
	if tok, ok, err := a.oauth.Token(ctx); ok || err != nil {
		return tok, err
	}
	if a.Token == "" {
		return "", fmt.Errorf("pixelfed %s: %w: connect it on its settings page or set token", a.ctx.InstanceName(), social.ErrNotConnected)
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
		max = 2000
	}
	return social.Limits{Platform: "pixelfed", Account: a.Instance, MaxLength: max, MaxImages: 1, RequiresImage: true,
		MentionProfiles: []string{"pixelfed", "mastodon"}}, nil
}

func (a *Account) limits() imageresize.Limits {
	lim := imageresize.DefaultsForPlatform("pixelfed")
	if a.MaxImageBytes > 0 {
		lim.MaxBytes = a.MaxImageBytes
	}
	if a.MaxImageLongEdge > 0 {
		lim.MaxLongEdge = a.MaxImageLongEdge
	}
	return lim
}

// caption carries a content warning and a link in the text: Pixelfed has
// neither natively.
func caption(p social.Post) string {
	t := p.Text
	if p.ContentWarning != "" {
		t = "CW: " + p.ContentWarning + "\n\n" + t
	}
	if p.Link != nil && p.Link.URL != "" && !strings.Contains(t, p.Link.URL) {
		t = strings.TrimSpace(t + "\n\n" + p.Link.URL)
	}
	return t
}

func (a *Account) Preview(_ context.Context, p social.Post) (social.Rendered, error) {
	r := social.Rendered{Text: caption(p), Images: p.Images}
	if len(p.Images) == 0 {
		r.Warnings = append(r.Warnings, "Pixelfed needs an image: this post is skipped")
	} else if len(p.Images) > 1 {
		r.Images = p.Images[:1]
		r.Warnings = append(r.Warnings, "only the first image is posted")
	}
	return r, nil
}

func wrap(err error) error {
	if errors.Is(err, api.ErrRateLimited) {
		return fmt.Errorf("%w: %w", social.ErrRateLimited, err)
	}
	return err
}

func (a *Account) Publish(ctx context.Context, p social.Post) (social.Result, error) {
	if len(p.Images) == 0 {
		return social.Result{}, fmt.Errorf("pixelfed: %w", social.ErrNeedsImage)
	}
	tok, err := a.token(ctx)
	if err != nil {
		return social.Result{}, err
	}
	data, _, err := companion.Fetch(ctx, a.ctx.HTTP(), p.Images[0].URL)
	if err != nil {
		return social.Result{}, err
	}
	if prepared, err := imageresize.PrepareForTarget(data, a.limits()); err != nil {
		a.ctx.Log().Printf("%s: image prep failed for %s, sending the original: %v", a.ctx.InstanceName(), p.ItemID, err)
	} else {
		data = prepared
	}
	media, err := api.UploadMedia(a.Instance, tok, data, p.Images[0].Alt)
	if err != nil {
		return social.Result{}, wrap(err)
	}
	post, err := api.CreatePost(a.Instance, tok, caption(p), media.ID)
	if err != nil {
		return social.Result{}, wrap(err)
	}
	return social.Result{ID: post.ID, URL: post.URL}, nil
}

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

// GetStats counts the likes. The instance comes from the post's URL, so a
// post matched by the backfill on another instance still counts.
func (a *Account) GetStats(ctx context.Context, ref social.Result) (social.Stats, error) {
	if ref.ID == "" {
		return social.Stats{}, errors.New("pixelfed: missing post id")
	}
	instance := a.Instance
	if ref.URL != "" {
		pu, err1 := url.Parse(ref.URL)
		cu, err2 := url.Parse(a.Instance)
		if err1 == nil && err2 == nil && pu.Host != "" && !strings.EqualFold(pu.Host, cu.Host) {
			instance = pu.Host
		}
	}
	tok, err := a.token(ctx)
	if err != nil {
		return social.Stats{}, err
	}
	resp, err := api.ListPostLikes(instance, tok, ref.ID)
	if err != nil {
		return social.Stats{}, wrap(err)
	}
	return social.Stats{Likes: len(resp.Accounts)}, nil
}

// ListReplies is not supported by Pixelfed's API the companion uses.
func (a *Account) ListReplies(context.Context, social.Result) ([]social.Reply, error) {
	return nil, nil
}

// ListPosts returns the account's posts for the backfill.
func (a *Account) ListPosts(ctx context.Context) ([]social.ListedPost, error) {
	tok, err := a.token(ctx)
	if err != nil {
		return nil, err
	}
	statuses, err := api.ListOwnStatuses(ctx, a.Instance, tok)
	if err != nil {
		return nil, wrap(err)
	}
	out := make([]social.ListedPost, 0, len(statuses))
	for _, s := range statuses {
		lp := social.ListedPost{Ref: social.Result{ID: s.ID, URL: s.URL}, Text: s.Content, CreatedAt: s.CreatedAt}
		for _, m := range s.MediaAttach {
			lp.ImageURLs = append(lp.ImageURLs, m.URL)
		}
		out = append(out, lp)
	}
	return out, nil
}

var (
	_ social.Publisher    = (*Account)(nil)
	_ social.Interactions = (*Account)(nil)
	_ social.Lister       = (*Account)(nil)

	_ companion.OAuthConnector = (*Account)(nil)
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
