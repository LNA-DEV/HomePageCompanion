package autoupload

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/capability/social"
	"github.com/LNA-DEV/HomePageCompanion/companion"
	"github.com/corona10/goimagehash"
	_ "golang.org/x/image/webp"
)

// hashDistanceThreshold is how far apart two perceptual hashes may be and
// still show the same photo.
const hashDistanceThreshold = 10

// backfill fills in the post ids of publications that lack them (posted
// before the companion recorded ids, or moved over from an old
// installation): it lists the target's own posts and matches their images
// to the feed's by perceptual hash. Likes can only be counted with an id.
func (c *Connection) backfill(ctx context.Context) error {
	lister, ok := c.Target.(social.Lister)
	if !ok {
		c.ctx.Log().Printf("%s: backfill: %s cannot list its posts", c.name(), c.TargetID)
		return nil
	}
	var pubs []Publication
	c.ctx.DB().Where("target = ? AND status = ? AND (post_id = '' OR post_id IS NULL)", c.TargetID, StatusPublished).Find(&pubs)
	if len(pubs) == 0 {
		return nil
	}
	type want struct {
		pub  Publication
		hash *goimagehash.ImageHash
	}
	var wants []want
	for _, p := range pubs {
		item, err := c.Source.GetItem(ctx, p.ItemID)
		if err != nil || item.Image == nil {
			continue
		}
		h, err := c.hashOf(ctx, item.Image.URL)
		if err != nil {
			c.ctx.Log().Printf("%s: backfill: hashing %s: %v", c.name(), p.ItemID, err)
			continue
		}
		wants = append(wants, want{p, h})
	}
	if len(wants) == 0 {
		return nil
	}
	posts, err := lister.ListPosts(ctx)
	if err != nil {
		return fmt.Errorf("backfill: listing posts of %s: %w", c.TargetID, err)
	}
	c.ctx.Log().Printf("%s: backfill: %d publication(s) without id, %d post(s) on %s", c.name(), len(wants), len(posts), c.TargetID)
	matched := 0
	for _, post := range posts {
		if len(post.ImageURLs) == 0 {
			continue
		}
		h, err := c.hashOf(ctx, post.ImageURLs[0])
		if err != nil {
			continue
		}
		for i, w := range wants {
			if w.hash == nil {
				continue
			}
			if d, err := h.Distance(w.hash); err == nil && d <= hashDistanceThreshold {
				c.ctx.DB().Model(&Publication{}).Where("id = ?", w.pub.ID).
					Updates(map[string]any{"post_id": post.Ref.ID, "post_url": post.Ref.URL, "version": post.Ref.Version})
				_ = c.ctx.Emit(ctx, social.EventPostPublished, social.PostPublished{ItemID: w.pub.ItemID, Source: "backfill",
					Connection: c.ctx.InstanceID(), Target: c.TargetID, Platform: w.pub.Platform, Ref: post.Ref, Time: w.pub.PublishedAt})
				wants[i].hash = nil
				matched++
				break
			}
		}
	}
	c.ctx.Log().Printf("%s: backfill: matched %d", c.name(), matched)
	return nil
}

// hashOf downloads an image (three tries on server errors) and hashes it.
func (c *Connection) hashOf(ctx context.Context, url string) (*goimagehash.ImageHash, error) {
	var data []byte
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		data, _, err = companion.Fetch(ctx, c.ctx.HTTP(), url)
		if err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(attempt) * time.Second):
		}
	}
	if err != nil {
		return nil, err
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return goimagehash.PerceptionHash(img)
}
