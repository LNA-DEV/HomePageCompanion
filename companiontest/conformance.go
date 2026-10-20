package companiontest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/capability/social"
	"github.com/LNA-DEV/HomePageCompanion/companion"
)

// BlobConformance checks a bucket against storage.blob/v1: every blob
// module must pass it. The bucket should be empty; part is the size of
// every multipart part but the last (S3 wants at least 5 MiB).
func BlobConformance(t *testing.T, b companion.Bucket, part int) {
	t.Helper()
	ctx := context.Background()
	if part <= 0 {
		part = 1024
	}

	t.Run("put get stat", func(t *testing.T) {
		if err := b.Put(ctx, "a/one.txt", strings.NewReader("hello"), 5, "text/plain"); err != nil {
			t.Fatal(err)
		}
		rc, info, err := b.Get(ctx, "a/one.txt")
		if err != nil {
			t.Fatal(err)
		}
		got, _ := io.ReadAll(rc)
		rc.Close()
		if string(got) != "hello" || info.Size != 5 {
			t.Fatalf("get: %q %+v", got, info)
		}
		st, err := b.Stat(ctx, "a/one.txt")
		if err != nil || st.Size != 5 {
			t.Fatalf("stat: %+v %v", st, err)
		}
		if _, err := b.Stat(ctx, "a/missing.txt"); !errors.Is(err, companion.ErrNotFound) {
			t.Fatalf("missing: %v", err)
		}
		if _, _, err := b.Get(ctx, "a/missing.txt"); !errors.Is(err, companion.ErrNotFound) {
			t.Fatalf("get missing: %v", err)
		}
	})

	t.Run("range", func(t *testing.T) {
		b.Put(ctx, "r.bin", bytes.NewReader([]byte("0123456789")), 10, "")
		got, err := b.ReadRange(ctx, "r.bin", 3, 4)
		if err != nil || string(got) != "3456" {
			t.Fatalf("range: %q %v", got, err)
		}
	})

	t.Run("list and delete", func(t *testing.T) {
		b.Put(ctx, "l/x.txt", strings.NewReader("x"), 1, "")
		b.Put(ctx, "l/y.txt", strings.NewReader("y"), 1, "")
		objs, err := b.List(ctx, "l/")
		if err != nil || len(objs) != 2 || objs[0].Key != "l/x.txt" {
			t.Fatalf("list: %+v %v", objs, err)
		}
		if err := b.Delete(ctx, "l/x.txt"); err != nil {
			t.Fatal(err)
		}
		if err := b.Delete(ctx, "l/never.txt"); err != nil {
			t.Fatalf("deleting a missing object: %v", err)
		}
		objs, _ = b.List(ctx, "l/")
		if len(objs) != 1 {
			t.Fatalf("after delete: %+v", objs)
		}
	})

	t.Run("multipart", func(t *testing.T) {
		first := bytes.Repeat([]byte("a"), part)
		last := []byte("tail")
		id, err := b.CreateUpload(ctx, "m/big.bin", "application/octet-stream")
		if err != nil {
			t.Fatal(err)
		}
		ups, err := b.ListUploads(ctx, "m/")
		if err != nil || len(ups) != 1 || ups[0].Key != "m/big.bin" {
			t.Fatalf("list uploads: %+v %v", ups, err)
		}
		e1, err := b.UploadPart(ctx, "m/big.bin", id, 1, first)
		if err != nil {
			t.Fatal(err)
		}
		e2, err := b.UploadPart(ctx, "m/big.bin", id, 2, last)
		if err != nil {
			t.Fatal(err)
		}
		if err := b.CompleteUpload(ctx, "m/big.bin", id, []companion.CompletedPart{{Number: 1, ETag: e1}, {Number: 2, ETag: e2}}); err != nil {
			t.Fatal(err)
		}
		st, err := b.Stat(ctx, "m/big.bin")
		if err != nil || st.Size != int64(part+len(last)) {
			t.Fatalf("assembled: %+v %v", st, err)
		}
		tail, _ := b.ReadRange(ctx, "m/big.bin", int64(part), 4)
		if string(tail) != "tail" {
			t.Fatalf("tail %q", tail)
		}
		id2, _ := b.CreateUpload(ctx, "m/aborted.bin", "")
		if err := b.AbortUpload(ctx, "m/aborted.bin", id2); err != nil {
			t.Fatal(err)
		}
		if ups, _ := b.ListUploads(ctx, "m/"); len(ups) != 0 {
			t.Fatalf("uploads left: %+v", ups)
		}
		// An aborted upload takes no more parts, and says so.
		if _, err := b.UploadPart(ctx, "m/aborted.bin", id2, 1, []byte("x")); !errors.Is(err, companion.ErrUploadGone) {
			t.Fatalf("part of an aborted upload: %v, want ErrUploadGone", err)
		}
		if err := b.CompleteUpload(ctx, "m/aborted.bin", id2, []companion.CompletedPart{{Number: 1, ETag: e1}}); !errors.Is(err, companion.ErrUploadGone) {
			t.Fatalf("completing an aborted upload: %v, want ErrUploadGone", err)
		}
	})

	t.Run("cleanup", func(t *testing.T) {
		objs, _ := b.List(ctx, "")
		for _, o := range objs {
			b.Delete(ctx, o.Key)
		}
	})
}

// PublisherConformance checks a social.publisher/v1 provider against a
// recorded or fake network: Describe answers, a post with an image goes
// out and comes back as a Result, and a post without an image is refused
// with ErrNeedsImage where the target requires one.
func PublisherConformance(t *testing.T, p social.Publisher, image string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	lim, err := p.Describe(ctx)
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if lim.Platform == "" || lim.MaxLength <= 0 {
		t.Fatalf("describe: %+v", lim)
	}
	post := social.Post{ItemID: "conformance-1", Kind: "photo", Text: "Conformance test", Images: []social.Image{{URL: image, Alt: "alt"}}}
	if r, err := p.Preview(ctx, post); err != nil || r.Text == "" {
		t.Fatalf("preview: %+v %v", r, err)
	}
	res, err := p.Publish(ctx, post)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if res.ID == "" {
		t.Fatalf("publish returned no ID: %+v", res)
	}
	if lim.RequiresImage {
		_, err := p.Publish(ctx, social.Post{ItemID: "conformance-2", Kind: "article", Text: "No image"})
		if !errors.Is(err, social.ErrNeedsImage) {
			t.Fatalf("a post without an image on %s: %v, want ErrNeedsImage", lim.Platform, err)
		}
	}
	if _, err := p.ResolveMention(ctx, "https://example.invalid/not-a-profile"); err == nil {
		t.Fatal(fmt.Sprintf("%s resolves a non-profile URL", lim.Platform))
	}
}
