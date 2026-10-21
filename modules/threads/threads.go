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

// Account is one Threads account.
type Account struct {
	UserID      string `json:"userId"`
	AccessToken string `json:"accessToken" companion:"secret"`

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
	if a.UserID == "" {
		return errors.New("userId is required")
	}
	return nil
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
		body := "The Threads token of " + a.ctx.InstanceName() + " has expired. Put a new one into .env."
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
	return social.Limits{Platform: "threads", Account: a.UserID, MaxLength: 500, LengthUnit: "utf8-emoji", MaxHashtags: 1,
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
	tok := a.token()
	var creationID string
	var err error
	if len(p.Images) > 0 && p.Images[0].URL != "" {
		creationID, err = api.CreateImageContainer(a.UserID, tok, p.Images[0].URL, text(p))
	} else {
		creationID, err = api.CreateTextContainer(a.UserID, tok, text(p))
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
	id, err := api.PublishContainer(a.UserID, tok, creationID)
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
