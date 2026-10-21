// Package instagram is the instagram module: one instance per account, each
// a social.publisher/v1 (images only) and social.interactions/v1. It keeps
// the account's token alive with a weekly refresh and tells its optional
// notify slot when the token is about to expire.
package instagram

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/capability/notify"
	"github.com/LNA-DEV/HomePageCompanion/capability/social"
	"github.com/LNA-DEV/HomePageCompanion/companion"
	"github.com/LNA-DEV/HomePageCompanion/lib/metatoken"
	"github.com/LNA-DEV/HomePageCompanion/lib/profiles"
	"github.com/LNA-DEV/HomePageCompanion/modules/instagram/api"
	"github.com/gin-gonic/gin"
)

func init() {
	companion.Register(companion.Info{
		ID:       "instagram",
		Name:     "Instagram",
		New:      func() companion.Instance { return new(Account) },
		Provides: []companion.Capability{social.PublisherV1, social.InteractionsV1},
		Slots:    []companion.Slot{{Name: "notify", Needs: notify.NotifyV1, Optional: true}},
		Routes:   routes,
		Import:   importLegacy,
		UI: &companion.UI{
			Actions: []companion.Action{{Label: "Refresh token now", Endpoint: "tokens/refresh"}},
			Widgets: []companion.Widget{{Element: "instagram-token-widget", Size: "S", Title: "Instagram token"}},
		},
	})
}

const (
	maxStatusPolls = 10
	statusPollGap  = 2 * time.Second
	// maxMentions is Instagram's limit of @ tags per caption.
	maxMentions = 20
)

// Account is one Instagram professional account.
type Account struct {
	AccountID   string `json:"accountId"`
	AccessToken string `json:"accessToken" companion:"secret"`
	MaxLength   int    `json:"maxLength"` // 2000 when 0

	Notify notify.Notifier `companion:"slot=notify"`

	ctx     companion.Context
	keeper  *metatoken.Keeper
	pollGap time.Duration
	selfMu  sync.Mutex
	self    string
	selfAt  time.Time
}

func (a *Account) Provision(ctx companion.Context) error {
	a.ctx = ctx
	a.pollGap = statusPollGap
	k, err := metatoken.New(ctx, a.AccessToken, func(_ context.Context, tok string) (string, time.Duration, error) {
		r, err := api.RefreshToken(tok)
		return r.AccessToken, time.Duration(r.ExpiresIn) * time.Second, err
	})
	if err != nil {
		return err
	}
	a.keeper = k
	return nil
}

func (a *Account) Validate() error {
	if a.AccountID == "" {
		return errors.New("accountId is required")
	}
	return nil
}

// SetPollGap shortens the status polling; for tests.
func (a *Account) SetPollGap(d time.Duration) { a.pollGap = d }

func (a *Account) token() string { return a.keeper.Token() }

func (a *Account) Jobs() []companion.Job {
	return []companion.Job{{Name: "token-refresh", Schedule: "0 20 3 * * *", Run: a.refreshDue}}
}

// refreshDue refreshes a due token and warns through the notify slot when
// it is about to expire, once a day.
func (a *Account) refreshDue(ctx context.Context) error {
	err := a.keeper.RunDue(ctx)
	st := a.keeper.Status()
	if a.Notify != nil && (st.Status == metatoken.StatusExpiring || st.Status == metatoken.StatusExpired) {
		body := "The Instagram token of " + a.ctx.InstanceName() + " has expired. Put a new one into .env."
		if st.Status == metatoken.StatusExpiring && st.DaysLeft != nil {
			body = fmt.Sprintf("The Instagram token of %s expires in %d days and could not be refreshed: %s", a.ctx.InstanceName(), *st.DaysLeft, st.LastError)
		}
		if nerr := a.Notify.Broadcast(ctx, notify.Message{Title: "Instagram token", Body: body}); nerr != nil {
			a.ctx.Log().Printf("%s: notify: %v", a.ctx.InstanceName(), nerr)
		}
	}
	return err
}

func (a *Account) Describe(context.Context) (social.Limits, error) {
	max := a.MaxLength
	if max <= 0 {
		max = 2000
	}
	return social.Limits{Platform: "instagram", Account: a.AccountID, MaxLength: max, MaxImages: 1, RequiresImage: true,
		MentionProfiles: []string{"instagram"}, MaxMentions: maxMentions}, nil
}

func caption(p social.Post) string {
	t := p.Text
	if p.ContentWarning != "" {
		t = "CW: " + p.ContentWarning + "\n\n" + t
	}
	return t
}

func (a *Account) Preview(_ context.Context, p social.Post) (social.Rendered, error) {
	r := social.Rendered{Text: caption(p), Images: p.Images}
	if len(p.Images) == 0 {
		r.Warnings = append(r.Warnings, "Instagram needs an image: this post is skipped")
	} else if len(p.Images) > 1 {
		r.Images = p.Images[:1]
		r.Warnings = append(r.Warnings, "only the first image is posted")
	}
	if p.Link != nil {
		r.Warnings = append(r.Warnings, "links in Instagram captions are not clickable")
	}
	return r, nil
}

func wrap(err error) error {
	if errors.Is(err, api.ErrRateLimited) {
		return fmt.Errorf("%w: %w", social.ErrRateLimited, err)
	}
	return err
}

// userTags tags everyone mentioned on the photo. The feed has no positions,
// so they are fixed: one person in the centre, several spread evenly along
// the middle row in feed order.
func userTags(mentions []social.Mention) []api.UserTag {
	if len(mentions) == 0 {
		return nil
	}
	tags := make([]api.UserTag, len(mentions))
	for i, m := range mentions {
		x := float64(i+1) / float64(len(mentions)+1)
		tags[i] = api.UserTag{Username: strings.TrimPrefix(m.Account, "@"), X: math.Round(x*100) / 100, Y: 0.5}
	}
	return tags
}

// Publish creates the media container (Instagram fetches the image from its
// URL itself), waits until it is ready and publishes it. What Instagram does
// with a private or missing account in user_tags is not known, so a 4xx
// other than 429 while tags are set gets one more try without them.
func (a *Account) Publish(ctx context.Context, p social.Post) (social.Result, error) {
	if len(p.Images) == 0 || p.Images[0].URL == "" {
		return social.Result{}, fmt.Errorf("instagram: %w", social.ErrNeedsImage)
	}
	tok := a.token()
	img := p.Images[0].URL
	tags := userTags(p.Mentions)
	creationID, err := api.CreateMediaContainer(a.AccountID, tok, img, caption(p), tags)
	var se *api.StatusError
	if err != nil && tags != nil && errors.As(err, &se) && se.StatusCode >= 400 && se.StatusCode < 500 {
		a.ctx.Log().Printf("%s: %s: photo tags rejected, retrying without them: %v", a.ctx.InstanceName(), p.ItemID, err)
		creationID, err = api.CreateMediaContainer(a.AccountID, tok, img, caption(p), nil)
	}
	if err != nil {
		return social.Result{}, fmt.Errorf("create media container: %w", wrap(err))
	}
	var status string
	var statusErr error
	for i := 0; i < maxStatusPolls; i++ {
		status, statusErr = api.CheckMediaStatus(creationID, tok)
		if statusErr == nil && status == "FINISHED" {
			break
		}
		select {
		case <-ctx.Done():
			return social.Result{}, ctx.Err()
		case <-time.After(a.pollGap):
		}
	}
	if status != "FINISHED" {
		if statusErr != nil {
			return social.Result{}, fmt.Errorf("media not ready: %w", wrap(statusErr))
		}
		return social.Result{}, fmt.Errorf("media not ready after %d attempts (last status: %q)", maxStatusPolls, status)
	}
	id, err := api.PublishContainer(a.AccountID, tok, creationID)
	if err != nil {
		return social.Result{}, fmt.Errorf("publish container: %w", wrap(err))
	}
	return social.Result{ID: id}, nil
}

// ResolveMention mentions an Instagram username. Meta has no lookup for
// arbitrary accounts, so nothing is checked: a wrong URL gives a dead
// mention. Never the posting account itself.
func (a *Account) ResolveMention(ctx context.Context, profileURL string) (social.Mention, error) {
	acc, ok := profiles.Instagram(profileURL)
	if !ok {
		return social.Mention{}, social.ErrNotAProfile
	}
	self, err := a.selfName(ctx)
	if err != nil {
		return social.Mention{}, fmt.Errorf("%w: %v", social.ErrIdentityUnknown, err)
	}
	if strings.EqualFold(acc.Name, self) {
		return social.Mention{}, social.ErrSelf
	}
	return social.Mention{Text: "@" + acc.Name, Account: acc.Name}, nil
}

func (a *Account) selfName(ctx context.Context) (string, error) {
	a.selfMu.Lock()
	defer a.selfMu.Unlock()
	if a.self != "" && time.Since(a.selfAt) < time.Hour {
		return a.self, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	me, err := api.Me(ctx, a.token())
	if err != nil {
		return "", err
	}
	a.self, a.selfAt = me, time.Now()
	return me, nil
}

func (a *Account) GetStats(_ context.Context, ref social.Result) (social.Stats, error) {
	if ref.ID == "" {
		return social.Stats{}, errors.New("instagram: missing media id")
	}
	n, err := api.MediaLikeCount(ref.ID, a.token())
	if err != nil {
		return social.Stats{}, wrap(err)
	}
	return social.Stats{Likes: n}, nil
}

// ListReplies: comments are not imported from Instagram.
func (a *Account) ListReplies(context.Context, social.Result) ([]social.Reply, error) {
	return nil, nil
}

func (a *Account) ListPosts(context.Context) ([]social.ListedPost, error) {
	media, err := api.ListMedia(a.AccountID, a.token())
	if err != nil {
		return nil, wrap(err)
	}
	out := make([]social.ListedPost, 0, len(media))
	for _, m := range media {
		lp := social.ListedPost{Ref: social.Result{ID: m.ID, URL: m.Permalink}, Text: m.Caption, CreatedAt: m.Timestamp}
		if m.MediaURL != "" {
			lp.ImageURLs = []string{m.MediaURL}
		}
		out = append(out, lp)
	}
	return out, nil
}

var (
	_ social.Publisher    = (*Account)(nil)
	_ social.Interactions = (*Account)(nil)
	_ social.Lister       = (*Account)(nil)
)

func routes(mc companion.ModuleContext, r *companion.Router) {
	r.Admin.GET("/tokens", func(c *gin.Context) {
		out := []metatoken.Status{}
		for _, in := range mc.Instances() {
			out = append(out, in.Instance.(*Account).keeper.Status())
		}
		c.JSON(http.StatusOK, out)
	})
	// Refresh now; a refusal from Meta is not an HTTP error, it shows in
	// lastError.
	r.Admin.POST("/tokens/refresh", func(c *gin.Context) {
		var body struct {
			Instance string `json:"instance"`
		}
		_ = c.ShouldBindJSON(&body)
		for _, in := range mc.Instances() {
			if in.Name == body.Instance {
				a := in.Instance.(*Account)
				_ = a.keeper.RefreshNow(c.Request.Context())
				c.JSON(http.StatusOK, a.keeper.Status())
				return
			}
		}
		c.JSON(http.StatusNotFound, gin.H{"error": "no such instance"})
	})
}

// importLegacy moves a refreshed token of the old platform_tokens table
// into the secret store, so the account does not fall back to the (older)
// token in .env.
func importLegacy(mc companion.ModuleContext, l *companion.Legacy) error {
	return metatoken.ImportLegacy(mc, l, "instagram", func(in companion.InstanceRef) *metatoken.Keeper {
		return in.Instance.(*Account).keeper
	})
}
