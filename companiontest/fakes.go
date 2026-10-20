package companiontest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/capability/feed"
	"github.com/LNA-DEV/HomePageCompanion/capability/geo"
	"github.com/LNA-DEV/HomePageCompanion/capability/media"
	"github.com/LNA-DEV/HomePageCompanion/capability/notify"
	"github.com/LNA-DEV/HomePageCompanion/capability/social"
	"github.com/LNA-DEV/HomePageCompanion/companion"
)

// Fakes for every capability. Configure them in companion.yaml like any
// building block (fake.publisher: { instances: { main: {} } }) and reach them
// with Instance[*FakePublisher](h, "fake.publisher:main").
func init() {
	companion.Register(companion.Info{ID: "fake.publisher", New: func() companion.Instance { return new(FakePublisher) },
		Provides: []companion.Capability{social.PublisherV1, social.InteractionsV1}})
	companion.Register(companion.Info{ID: "fake.source", New: func() companion.Instance { return new(FakeSource) },
		Provides: []companion.Capability{feed.SourceV1}})
	companion.Register(companion.Info{ID: "fake.router", New: func() companion.Instance { return new(FakeRouter) },
		Provides: []companion.Capability{geo.RoutingV1}})
	companion.Register(companion.Info{ID: "fake.notifier", New: func() companion.Instance { return new(FakeNotifier) },
		Provides: []companion.Capability{notify.NotifyV1}})
	companion.Register(companion.Info{ID: "fake.media", New: func() companion.Instance { return new(FakeMedia) },
		Provides: []companion.Capability{media.ImageV1}})
}

// FakePublisher stores what it is asked to publish and answers with
// chosen errors.
type FakePublisher struct {
	Platform      string `json:"platform"`
	MaxLength     int    `json:"maxLength"`
	RequiresImage bool   `json:"requiresImage"`

	mu        sync.Mutex
	Published []social.Post
	// Err, when set, fails the next publishes; ErrOnce only the next one.
	Err     error
	ErrOnce error
	Likes   map[string]int // by Result.ID
	Replies map[string][]social.Reply
	// Profiles maps a profile URL to its mention; others are not a profile.
	Profiles map[string]social.Mention
	Posts    []social.ListedPost
	n        int
}

func (f *FakePublisher) Describe(context.Context) (social.Limits, error) {
	p := f.Platform
	if p == "" {
		p = "fake"
	}
	max := f.MaxLength
	if max == 0 {
		max = 500
	}
	return social.Limits{Platform: p, MaxLength: max, RequiresImage: f.RequiresImage, MentionProfiles: []string{p}, LinkCard: "auto"}, nil
}

func (f *FakePublisher) Preview(_ context.Context, p social.Post) (social.Rendered, error) {
	return social.Rendered{Text: p.Text, Images: p.Images, Link: p.Link}, nil
}

func (f *FakePublisher) Publish(_ context.Context, p social.Post) (social.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ErrOnce != nil {
		err := f.ErrOnce
		f.ErrOnce = nil
		return social.Result{}, err
	}
	if f.Err != nil {
		return social.Result{}, f.Err
	}
	if f.RequiresImage && len(p.Images) == 0 {
		return social.Result{}, social.ErrNeedsImage
	}
	f.n++
	f.Published = append(f.Published, p)
	id := fmt.Sprintf("post-%d", f.n)
	return social.Result{ID: id, URL: "https://fake.example/" + id}, nil
}

func (f *FakePublisher) ResolveMention(_ context.Context, profileURL string) (social.Mention, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if m, ok := f.Profiles[profileURL]; ok {
		return m, nil
	}
	return social.Mention{}, social.ErrNotAProfile
}

func (f *FakePublisher) ListPosts(context.Context) ([]social.ListedPost, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]social.ListedPost(nil), f.Posts...), nil
}

func (f *FakePublisher) GetStats(_ context.Context, ref social.Result) (social.Stats, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return social.Stats{}, f.Err
	}
	return social.Stats{Likes: f.Likes[ref.ID]}, nil
}

func (f *FakePublisher) ListReplies(_ context.Context, ref social.Result) ([]social.Reply, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]social.Reply(nil), f.Replies[ref.ID]...), nil
}

// SetLikes sets a post's likes.
func (f *FakePublisher) SetLikes(id string, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Likes == nil {
		f.Likes = map[string]int{}
	}
	f.Likes[id] = n
}

// Fail makes every following call fail with err (nil: succeed again).
func (f *FakePublisher) Fail(err error) {
	f.mu.Lock()
	f.Err = err
	f.mu.Unlock()
}

// FailOnce fails the next publish.
func (f *FakePublisher) FailOnce(err error) {
	f.mu.Lock()
	f.ErrOnce = err
	f.mu.Unlock()
}

// Posted returns what was published so far.
func (f *FakePublisher) Posted() []social.Post {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]social.Post(nil), f.Published...)
}

// FakeSource is a feed whose items the test sets.
type FakeSource struct {
	mu    sync.Mutex
	items []feed.Item
	Err   error
}

// SetItems replaces the feed.
func (f *FakeSource) SetItems(items ...feed.Item) {
	f.mu.Lock()
	f.items = append([]feed.Item(nil), items...)
	f.mu.Unlock()
}

func (f *FakeSource) ListItems(context.Context) ([]feed.Item, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return nil, f.Err
	}
	return append([]feed.Item(nil), f.items...), nil
}

func (f *FakeSource) GetItem(_ context.Context, id string) (feed.Item, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, it := range f.items {
		if it.ID == id {
			return it, nil
		}
	}
	return feed.Item{}, feed.ErrNotFound
}

// Photo is a feed item with an image.
func Photo(id string, published time.Time, title string) feed.Item {
	return feed.Item{ID: id, Kind: feed.KindPhoto, Title: title, Link: "https://example.com/gallery/#" + id,
		Published: published, HasDate: true, Image: &feed.Image{URL: "https://example.com/" + id + ".jpg", Alt: "alt of " + title}}
}

// Article is a feed item without an image.
func Article(id string, published time.Time, title string) feed.Item {
	return feed.Item{ID: id, Kind: feed.KindArticle, Title: title, Summary: "About " + strings.ToLower(title),
		Link: "https://example.com/posts/" + id + "/", Published: published, HasDate: true}
}

// FakeRouter returns a straight line through the points.
type FakeRouter struct {
	mu    sync.Mutex
	Calls int
	Err   error
}

func (f *FakeRouter) Route(_ context.Context, mode string, pts []geo.Point) (geo.Route, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls++
	if f.Err != nil {
		return geo.Route{}, f.Err
	}
	if mode == "walk" {
		return geo.Route{}, geo.ErrUnroutable
	}
	var out [][2]float64
	for _, p := range pts {
		out = append(out, [2]float64{p.Lat, p.Lng})
	}
	return geo.Route{Points: out, Source: "fake"}, nil
}

// Fail makes the following routes fail with err (nil: succeed again).
func (f *FakeRouter) Fail(err error) {
	f.mu.Lock()
	f.Err = err
	f.mu.Unlock()
}

// CallCount is how often Route was called.
func (f *FakeRouter) CallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Calls
}

// FakeNotifier records messages.
type FakeNotifier struct {
	mu   sync.Mutex
	Sent []notify.Message
}

func (f *FakeNotifier) Send(_ context.Context, _ string, m notify.Message) error {
	return f.Broadcast(context.Background(), m)
}

func (f *FakeNotifier) Broadcast(_ context.Context, m notify.Message) error {
	f.mu.Lock()
	f.Sent = append(f.Sent, m)
	f.mu.Unlock()
	return nil
}

// Messages returns what was sent.
func (f *FakeNotifier) Messages() []notify.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]notify.Message(nil), f.Sent...)
}

// FakeMedia passes images through.
type FakeMedia struct{}

func (FakeMedia) Resize(_ context.Context, b []byte, _ media.Limits) ([]byte, error) { return b, nil }
func (FakeMedia) ReadExif(context.Context, []byte) (media.Metadata, error) {
	return media.Metadata{}, errors.New("fake: no metadata")
}
