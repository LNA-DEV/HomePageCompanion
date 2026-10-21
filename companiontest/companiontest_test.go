package companiontest

import (
	"context"
	"testing"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/capability/social"
)

func TestHarnessWithFakes(t *testing.T) {
	h := New(t, `
modules:
  fake.publisher: { instances: { main: { requiresImage: true } } }
  fake.source: { instances: { photos: {} } }
`)
	pub := Instance[*FakePublisher](h, "fake.publisher:main")
	if _, err := pub.Publish(context.Background(), social.Post{Text: "no image"}); err != social.ErrNeedsImage {
		t.Fatalf("requiresImage: %v", err)
	}
	r, err := pub.Publish(context.Background(), social.Post{Text: "hi", Images: []social.Image{{URL: "x"}}})
	if err != nil || r.ID != "post-1" || len(pub.Posted()) != 1 {
		t.Fatalf("publish: %v %v", r, err)
	}
	pub.SetLikes("post-1", 7)
	if s, _ := pub.GetStats(context.Background(), r); s.Likes != 7 {
		t.Fatalf("likes %d", s.Likes)
	}
	src := Instance[*FakeSource](h, "fake.source:photos")
	src.SetItems(Photo("a", time.Now(), "A"), Article("b", time.Now(), "B"))
	items, _ := src.ListItems(context.Background())
	if len(items) != 2 || items[1].Image != nil {
		t.Fatalf("items %v", items)
	}
	if w := h.Admin("GET", "/api/core/instances", nil); w.Code != 200 {
		t.Fatalf("instances: %d", w.Code)
	}
}
