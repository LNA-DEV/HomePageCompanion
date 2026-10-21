// Package threads is the threads module: one instance per account, each a
// social.publisher/v1 (image and text posts) and social.interactions/v1. It
// keeps the account's token alive like the instagram module.
package threads

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/LNA-DEV/HomePageCompanion/capability/notify"
	"github.com/LNA-DEV/HomePageCompanion/capability/social"
	"github.com/LNA-DEV/HomePageCompanion/companion"
	"github.com/LNA-DEV/HomePageCompanion/lib/metatoken"
	"github.com/LNA-DEV/HomePageCompanion/lib/profiles"
	"github.com/LNA-DEV/HomePageCompanion/modules/threads/api"
	"github.com/gin-gonic/gin"
)

func init() {
	companion.Register(companion.Info{
		ID:       "threads",
		Name:     "Threads",
		New:      func() companion.Instance { return new(Account) },
		Provides: []companion.Capability{social.PublisherV1, social.InteractionsV1},
		Slots:    []companion.Slot{{Name: "notify", Needs: notify.NotifyV1, Optional: true}},
		Routes:   routes,
		Import:   importLegacy,
		UI: &companion.UI{
			Actions: []companion.Action{{Label: "Refresh token now", Endpoint: "tokens/refresh"}},
			Widgets: []companion.Widget{{Element: "threads-token-widget", Size: "S", Title: "Threads token"}},
		},
	})
}

const maxStatusPolls = 10

// Account is one Threads account. Either its token comes from the config
// (accessToken and userId), or the account is connected on the settings
// page through OAuth, with the Threads app's ID and secret (appId,
// appSecret).
type Account struct {
	UserID      string `json:"userId"`
	AccessToken string `json:"accessToken" companion:"secret"`
	AppID       string `json:"appId"`
	AppSecret   string `json:"appSecret" companion:"secret"`

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
	a.pollGap = 2 * time.Second
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
	if (a.AppID == "") != (a.AppSecret == "") {
		return errors.New("appId and appSecret go together")
	}
	return nil
}

// userID is the connected account's ID, or else the configured one.
func (a *Account) userID() string {
	if _, id, ok := a.keeper.Connected(); ok && id != "" {
		return id
	}
	return a.UserID
}

// account is the token and user ID to post with.
func (a *Account) account() (token, id string, err error) {
	token, id = a.token(), a.userID()
	if token == "" || id == "" {
		return "", "", fmt.Errorf("threads %s: %w: connect it on its settings page, or set accessToken and userId", a.ctx.InstanceName(), social.ErrNotConnected)
	}
	return token, id, nil
}

func (a *Account) OAuthClient(_ context.Context, redirectURI string) (companion.OAuthClient, error) {
	if a.AppID == "" {
		return companion.OAuthClient{}, fmt.Errorf("set appId and appSecret, the Threads app ID and secret from the app dashboard, and register %s there as a redirect callback URL", redirectURI)
	}
	return companion.OAuthClient{AuthURL: api.AuthorizeURL, TokenURL: api.TokenURL, ClientID: a.AppID, ClientSecret: a.AppSecret,
		Scopes: api.Scopes, ScopeSep: ","}, nil
}

// OAuthConnect trades the login's short-lived token for a long-lived one
// and hands that to the keeper, which refreshes it from now on.
func (a *Account) OAuthConnect(ctx context.Context, _ companion.OAuthClient, tok companion.OAuthToken) (string, error) {
	long, err := api.ExchangeLongLived(ctx, a.AppSecret, tok.AccessToken)
	if err != nil {
		return "", err
	}
	p, err := api.GetProfile(ctx, long.AccessToken)
	if err != nil {
		return "", err
	}
	expires := time.Now().Add(time.Duration(long.ExpiresIn) * time.Second)
	if long.ExpiresIn <= 0 {
		expires = time.Now().Add(60 * 24 * time.Hour)
	}
	name := "@" + p.Username
	if err := a.keeper.Connect(long.AccessToken, expires, name, p.UserID); err != nil {
		return "", err
	}
	a.forgetSelf()
	return name, nil
}

func (a *Account) OAuthState() companion.OAuthState {
	st := a.keeper.Status()
	if st.Source != "connected" {
		return companion.OAuthState{}
	}
	out := companion.OAuthState{Connected: true, Account: st.Account, ExpiresAt: st.ExpiresAt}
	if a.AccessToken != "" {
		out.Note = "A new accessToken in the settings replaces the connected account."
	}
	return out
}

func (a *Account) OAuthDisconnect(context.Context) error {
	a.forgetSelf()
	return a.keeper.Disconnect()
}

func (a *Account) forgetSelf() {
	a.selfMu.Lock()
	a.self = ""
	a.selfMu.Unlock()
}

// SetPollGap shortens the status polling; for tests.
func (a *Account) SetPollGap(d time.Duration) { a.pollGap = d }

func (a *Account) token() string { return a.keeper.Token() }

func (a *Account) Jobs() []companion.Job {
	return []companion.Job{{Name: "token-refresh", Schedule: "0 22 3 * * *", Run: a.refreshDue}}
}

func (a *Account) refreshDue(ctx context.Context) error {
	err := a.keeper.RunDue(ctx)
	st := a.keeper.Status()
	if a.Notify != nil && (st.Status == metatoken.StatusExpiring || st.Status == metatoken.StatusExpired) {
		body := "The Threads token of " + a.ctx.InstanceName() + " has expired. Connect the account again on its settings page, or put a new token into .env."
		if st.Status == metatoken.StatusExpiring && st.DaysLeft != nil {
			body = fmt.Sprintf("The Threads token of %s expires in %d days and could not be refreshed: %s", a.ctx.InstanceName(), *st.DaysLeft, st.LastError)
		}
		if nerr := a.Notify.Broadcast(ctx, notify.Message{Title: "Threads token", Body: body}); nerr != nil {
			a.ctx.Log().Printf("%s: notify: %v", a.ctx.InstanceName(), nerr)
		}
	}
	return err
}

// Describe: Threads counts an emoji as its UTF-8 bytes, and indexes only one
// topic tag per post (further #tags show as dead text).
func (a *Account) Describe(context.Context) (social.Limits, error) {
	return social.Limits{Platform: "threads", Account: a.userID(), MaxLength: 500, LengthUnit: "utf8-emoji", MaxHashtags: 1,
		MaxImages: 1, LinkCard: "auto", MentionProfiles: []string{"threads"}}, nil
}

// text carries a content warning and the link in the text: Threads builds
// the card from the first link.
func text(p social.Post) string {
	t := p.Text
	if strings.TrimSpace(p.ContentWarning) != "" {
		t = "CW: " + p.ContentWarning + "\n\n" + t
	}
	if p.Link != nil && p.Link.URL != "" && !strings.Contains(t, p.Link.URL) {
		t = strings.TrimSpace(t + "\n\n" + p.Link.URL)
	}
	return t
}

func (a *Account) Preview(_ context.Context, p social.Post) (social.Rendered, error) {
	r := social.Rendered{Text: text(p), Images: p.Images, Link: p.Link}
	if len(p.Images) > 1 {
		r.Images = p.Images[:1]
		r.Warnings = append(r.Warnings, "only the first image is posted")
	}
	if n := utf8.RuneCountInString(r.Text); n > 500 {
		r.Warnings = append(r.Warnings, fmt.Sprintf("the text has %d characters, Threads allows 500", n))
	}
	return r, nil
}

func wrap(err error) error {
	if errors.Is(err, api.ErrRateLimited) {
		return fmt.Errorf("%w: %w", social.ErrRateLimited, err)
	}
	return err
}

// Publish creates an image container (Threads fetches the image from its
// URL itself) or a text container, waits until it is ready, publishes it and
// resolves the permalink.
func (a *Account) Publish(ctx context.Context, p social.Post) (social.Result, error) {
	tok, userID, err := a.account()
	if err != nil {
		return social.Result{}, err
	}
	var creationID string
	if len(p.Images) > 0 && p.Images[0].URL != "" {
		creationID, err = api.CreateImageContainer(userID, tok, p.Images[0].URL, text(p))
	} else {
		creationID, err = api.CreateTextContainer(userID, tok, text(p))
	}
	if err != nil {
		return social.Result{}, fmt.Errorf("create container: %w", wrap(err))
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
	id, err := api.PublishContainer(userID, tok, creationID)
	if err != nil {
		return social.Result{}, fmt.Errorf("publish container: %w", wrap(err))
	}
	permalink, err := api.GetPermalink(id, tok)
	if err != nil {
		a.ctx.Log().Printf("%s: permalink for %s: %v", a.ctx.InstanceName(), id, err)
	}
	return social.Result{ID: id, URL: permalink}, nil
}

func (a *Account) Delete(_ context.Context, ref social.Result) error {
	err := api.DeleteMedia(ref.ID, a.token())
	if errors.Is(err, api.ErrNotFound) {
		return nil
	}
	return wrap(err)
}

// ResolveMention mentions a Threads username without a lookup (Meta has
// none); never the posting account.
func (a *Account) ResolveMention(ctx context.Context, profileURL string) (social.Mention, error) {
	acc, ok := profiles.Threads(profileURL)
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
		return social.Stats{}, errors.New("threads: missing media id")
	}
	n, err := api.MediaLikeCount(ref.ID, a.token())
	if err != nil {
		return social.Stats{}, wrap(err)
	}
	return social.Stats{Likes: n}, nil
}

func (a *Account) ListReplies(_ context.Context, ref social.Result) ([]social.Reply, error) {
	replies, err := api.ListReplies(ref.ID, a.token())
	if err != nil {
		return nil, wrap(err)
	}
	out := make([]social.Reply, 0, len(replies))
	for _, r := range replies {
		authorURL := ""
		if r.From.Username != "" {
			authorURL = "https://www.threads.net/@" + r.From.Username
		}
		out = append(out, social.Reply{ID: r.ID, Author: r.From.Username, AuthorURL: authorURL, Body: r.Text, URL: r.Permalink, PostedAt: r.Timestamp.Time})
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
	r.Admin.GET("/tokens", func(c *gin.Context) {
		out := []metatoken.Status{}
		for _, in := range mc.Instances() {
			out = append(out, in.Instance.(*Account).keeper.Status())
		}
		c.JSON(http.StatusOK, out)
	})
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

func importLegacy(mc companion.ModuleContext, l *companion.Legacy) error {
	return metatoken.ImportLegacy(mc, l, "threads", func(in companion.InstanceRef) *metatoken.Keeper {
		return in.Instance.(*Account).keeper
	})
}
