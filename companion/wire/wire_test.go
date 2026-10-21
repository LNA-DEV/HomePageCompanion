package wire_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/capability/media"
	"github.com/LNA-DEV/HomePageCompanion/capability/notify"
	"github.com/LNA-DEV/HomePageCompanion/capability/social"
	"github.com/LNA-DEV/HomePageCompanion/companion/wire"
)

// publisher implements social.publisher/v1 with listPosts but not delete.
type publisher struct {
	posted []social.Post
	err    error
}

func (p *publisher) Describe(context.Context) (social.Limits, error) {
	return social.Limits{Platform: "lemmy", MaxLength: 10000}, nil
}
func (p *publisher) Preview(_ context.Context, post social.Post) (social.Rendered, error) {
	return social.Rendered{Text: "preview: " + post.Text}, nil
}
func (p *publisher) Publish(_ context.Context, post social.Post) (social.Result, error) {
	if p.err != nil {
		return social.Result{}, p.err
	}
	p.posted = append(p.posted, post)
	return social.Result{ID: "42", URL: "https://lemmy.example/post/42"}, nil
}
func (p *publisher) ResolveMention(_ context.Context, profileURL string) (social.Mention, error) {
	if !strings.Contains(profileURL, "/u/") {
		return social.Mention{}, fmt.Errorf("%s: %w", profileURL, social.ErrNotAProfile)
	}
	return social.Mention{Text: "@jane@lemmy.example", Account: "jane@lemmy.example"}, nil
}
func (p *publisher) ListPosts(context.Context) ([]social.ListedPost, error) {
	return []social.ListedPost{{Ref: social.Result{ID: "1"}, Text: "old"}}, nil
}

// serve answers the operations of one server under /capabilities/<cap>/.
func serve(t *testing.T, s *wire.Server) *httptest.Server {
	t.Helper()
	prefix := "/capabilities/" + s.Capability + "/"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		op, ok := strings.CutPrefix(r.URL.Path, prefix)
		if !ok || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		s.Serve(w, r, op)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func clientFor(t *testing.T, capName string, impl any) (any, *wire.Server) {
	t.Helper()
	a, ok := wire.Lookup(capName)
	if !ok {
		t.Fatalf("no adapter for %s", capName)
	}
	s := a.Server(impl)
	if s == nil {
		t.Fatalf("%T does not implement %s", impl, capName)
	}
	srv := serve(t, s)
	return a.Client(&wire.HTTP{URL: srv.URL + "/capabilities/" + capName}, s.Operations()), s
}

func TestPublisherRoundTrip(t *testing.T) {
	impl := &publisher{}
	c, s := clientFor(t, "social.publisher/v1", impl)
	if got := strings.Join(s.Operations(), ","); got != "describe,listPosts,preview,publish,resolveMention" {
		t.Fatalf("operations %s", got)
	}
	p := c.(social.Publisher)
	ctx := context.Background()

	lim, err := p.Describe(ctx)
	if err != nil || lim.Platform != "lemmy" || lim.MaxLength != 10000 {
		t.Fatalf("describe %+v %v", lim, err)
	}
	post := social.Post{ItemID: "photo-1", Kind: "photo", Text: "Watzmann", Images: []social.Image{{URL: "https://example.com/a.jpg", Alt: "a mountain"}},
		Link: &social.LinkCard{URL: "https://example.com/p/1"}, IdempotencyKey: "k1"}
	res, err := p.Publish(ctx, post)
	if err != nil || res.ID != "42" || res.URL != "https://lemmy.example/post/42" {
		t.Fatalf("publish %+v %v", res, err)
	}
	if got := impl.posted[0]; got.Text != "Watzmann" || got.Images[0].Alt != "a mountain" || got.Link.URL != "https://example.com/p/1" || got.IdempotencyKey != "k1" {
		t.Fatalf("arrived %+v", got)
	}
	if r, _ := p.Preview(ctx, post); r.Text != "preview: Watzmann" {
		t.Fatalf("preview %+v", r)
	}
	if m, err := p.ResolveMention(ctx, "https://lemmy.example/u/jane"); err != nil || m.Account != "jane@lemmy.example" {
		t.Fatalf("mention %+v %v", m, err)
	}

	// The capability's sentinel errors survive the trip.
	if _, err := p.ResolveMention(ctx, "https://example.com/about"); !errors.Is(err, social.ErrNotAProfile) || !strings.Contains(err.Error(), "example.com/about") {
		t.Fatalf("not a profile: %v", err)
	}
	impl.err = fmt.Errorf("lemmy: %w", social.ErrRateLimited)
	if _, err := p.Publish(ctx, post); !errors.Is(err, social.ErrRateLimited) || social.Classify(err).Cause != social.CauseTransient {
		t.Fatalf("rate limited: %v", err)
	}
	impl.err = &social.StatusError{Status: 401, Err: errors.New("login refused")}
	if _, err := p.Publish(ctx, post); social.Classify(err).Cause != social.CauseAccount || social.Classify(err).HTTPStatus != 401 {
		t.Fatalf("401 through the wire: %v %+v", err, social.Classify(err))
	}

	// listPosts is offered, delete is not: the client says so by its type.
	l, ok := c.(social.Lister)
	if !ok {
		t.Fatal("client does not list")
	}
	if posts, err := l.ListPosts(ctx); err != nil || len(posts) != 1 || posts[0].Text != "old" {
		t.Fatalf("list %+v %v", posts, err)
	}
	if _, ok := c.(social.Deleter); ok {
		t.Fatal("client deletes although the provider does not")
	}
	a, _ := wire.Lookup("social.publisher/v1")
	if _, ok := a.Client(&wire.HTTP{}, []string{"describe", "delete"}).(social.Deleter); !ok {
		t.Fatal("delete offered, client cannot delete")
	}
}

type notifier struct{ got []string }

func (n *notifier) Send(_ context.Context, to string, m notify.Message) error {
	n.got = append(n.got, to+":"+m.Title)
	return nil
}
func (n *notifier) Broadcast(_ context.Context, m notify.Message) error {
	n.got = append(n.got, "*:"+m.Title)
	return nil
}

func TestSeveralArgumentsAndNoResult(t *testing.T) {
	impl := &notifier{}
	c, _ := clientFor(t, "notify/v1", impl)
	n := c.(notify.Notifier)
	if err := n.Send(context.Background(), "sub-1", notify.Message{Title: "hi"}); err != nil {
		t.Fatal(err)
	}
	if err := n.Broadcast(context.Background(), notify.Message{Title: "all"}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(impl.got, ",") != "sub-1:hi,*:all" {
		t.Fatalf("got %v", impl.got)
	}
}

type resizer struct{ lim media.Limits }

func (r *resizer) Resize(_ context.Context, data []byte, lim media.Limits) ([]byte, error) {
	r.lim = lim
	return bytes.ToUpper(data), nil
}
func (r *resizer) ReadExif(_ context.Context, data []byte) (media.Metadata, error) {
	return media.Metadata{Model: string(data)}, nil
}

// Binary data travels as the raw body, never inside JSON.
func TestRawBodies(t *testing.T) {
	impl := &resizer{}
	c, _ := clientFor(t, "media.image/v1", impl)
	m := c.(media.Image)
	out, err := m.Resize(context.Background(), []byte("jpeg bytes"), media.Limits{MaxBytes: 1000, MaxLongEdge: 2048})
	if err != nil || string(out) != "JPEG BYTES" || impl.lim.MaxLongEdge != 2048 {
		t.Fatalf("resize %q %v %+v", out, err, impl.lim)
	}
	md, err := m.ReadExif(context.Background(), []byte("X-T5"))
	if err != nil || md.Model != "X-T5" {
		t.Fatalf("exif %+v %v", md, err)
	}
}

func TestErrorsWithoutAProperAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/describe") {
			http.Error(w, "<html>bad gateway</html>", http.StatusBadGateway)
			return
		}
		io.WriteString(w, "not json")
	}))
	defer srv.Close()
	a, _ := wire.Lookup("social.publisher/v1")
	p := a.Client(&wire.HTTP{URL: srv.URL}, nil).(social.Publisher)
	_, err := p.Describe(context.Background())
	var we *wire.Error
	if !errors.As(err, &we) || we.Code != wire.CodeUnavailable || !strings.Contains(err.Error(), "502") {
		t.Fatalf("502: %v", err)
	}
	if _, err := p.Publish(context.Background(), social.Post{}); !errors.As(err, &we) || we.Code != wire.CodeUnavailable {
		t.Fatalf("not json: %v", err)
	}
	// An operation the server does not offer.
	s := (&publisher{})
	srv2 := serve(t, func() *wire.Server { x, _ := wire.Lookup("social.publisher/v1"); return x.Server(s) }())
	d := a.Client(&wire.HTTP{URL: srv2.URL + "/capabilities/social.publisher/v1"}, []string{"delete"}).(social.Deleter)
	if err := d.Delete(context.Background(), social.Result{ID: "1"}); !errors.As(err, &we) || we.Code != wire.CodeUnsupported {
		t.Fatalf("unsupported: %v", err)
	}
}

func TestSignature(t *testing.T) {
	key := []byte("service key")
	now := time.Unix(1_800_000_000, 0)
	req := func(instance, body string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/capabilities/social.publisher/v1/publish?x=1", strings.NewReader(body))
		r.Header.Set(wire.InstanceHeader, instance)
		return r
	}
	r := req("main", `{"text":"hi"}`)
	wire.Sign(r, []byte(`{"text":"hi"}`), key, now)
	if err := wire.Verify(r, []byte(`{"text":"hi"}`), key, now.Add(time.Minute)); err != nil {
		t.Fatalf("valid signature refused: %v", err)
	}
	for name, check := range map[string]func() error{
		"other body": func() error { return wire.Verify(r, []byte(`{"text":"ho"}`), key, now) },
		"other key":  func() error { return wire.Verify(r, []byte(`{"text":"hi"}`), []byte("other"), now) },
		"too old":    func() error { return wire.Verify(r, []byte(`{"text":"hi"}`), key, now.Add(10*time.Minute)) },
		"other instance": func() error {
			r2 := req("other", "")
			r2.Header.Set(wire.SignatureHeader, r.Header.Get(wire.SignatureHeader))
			return wire.Verify(r2, []byte(`{"text":"hi"}`), key, now)
		},
		"unsigned": func() error { return wire.Verify(req("main", ""), nil, key, now) },
	} {
		if err := check(); !errors.Is(err, wire.ErrBadSignature) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestMeasure(t *testing.T) {
	a, _ := wire.Lookup("social.publisher/v1")
	var seen []string
	impl := &publisher{err: errors.New("down")}
	m := a.Measure(impl, func(op string, start time.Time, err error) {
		seen = append(seen, fmt.Sprintf("%s:%v", op, err != nil))
	})
	p := m.(social.Publisher)
	p.Describe(context.Background())
	p.Publish(context.Background(), social.Post{})
	m.(social.Lister).ListPosts(context.Background())
	if _, ok := m.(social.Deleter); ok {
		t.Fatal("measured wrapper deletes although the implementation does not")
	}
	if strings.Join(seen, ",") != "describe:false,publish:true,listPosts:false" {
		t.Fatalf("observed %v", seen)
	}
	if got := a.Measure("not a publisher", nil); got != "not a publisher" {
		t.Fatalf("wrapped a foreign value: %v", got)
	}
}
