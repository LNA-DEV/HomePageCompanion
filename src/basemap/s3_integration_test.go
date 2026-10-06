package basemap

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"
)

// TestS3StoreAgainstARealBucket runs the store against an actual S3 service.
// It is skipped unless BASEMAP_S3_TEST_URL names a bucket, e.g.
//
//	BASEMAP_S3_TEST_URL='s3://lna-dev?endpoint=https://nbg1.your-objectstorage.com&region=nbg1' \
//	AWS_ACCESS_KEY_ID=… AWS_SECRET_ACCESS_KEY=… go test ./basemap -run S3Store
//
// Everything it writes is under "spike/store-test/" and removed again.
func TestS3StoreAgainstARealBucket(t *testing.T) {
	bucketURL := os.Getenv("BASEMAP_S3_TEST_URL")
	if bucketURL == "" {
		t.Skip("BASEMAP_S3_TEST_URL not set")
	}
	ctx := context.Background()
	st, err := OpenStore(ctx, bucketURL)
	if err != nil {
		t.Fatal(err)
	}
	prefix := fmt.Sprintf("spike/store-test/%d/", time.Now().UnixNano())
	key := prefix + "object.bin"

	// Two parts: S3 wants at least 5 MiB for every part but the last.
	part1 := make([]byte, 5<<20)
	part2 := []byte("the tail of the object")
	rand.Read(part1)

	id, err := st.CreateUpload(ctx, key)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { st.Abort(ctx, key, id); st.Delete(ctx, key) })

	e1, err := st.UploadPart(ctx, key, id, 1, part1)
	if err != nil {
		t.Fatalf("part 1: %v", err)
	}
	e2, err := st.UploadPart(ctx, key, id, 2, part2)
	if err != nil {
		t.Fatalf("part 2: %v", err)
	}

	uploads, err := st.ListUploads(ctx, prefix)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(uploads) != 1 || uploads[0].UploadID != id || uploads[0].Initiated.IsZero() {
		t.Fatalf("uploads = %+v", uploads)
	}

	if err := st.Complete(ctx, key, id, []CompletedPart{{1, e1}, {2, e2}}); err != nil {
		t.Fatalf("complete: %v", err)
	}
	size, err := st.Size(ctx, key)
	if err != nil || size != int64(len(part1)+len(part2)) {
		t.Fatalf("size %d, %v", size, err)
	}
	// A range across the part boundary.
	got, err := st.ReadRange(ctx, key, int64(len(part1))-4, 8)
	if err != nil || !bytes.Equal(got, append(append([]byte{}, part1[len(part1)-4:]...), part2[:4]...)) {
		t.Fatalf("range across parts: %x, %v", got, err)
	}

	// An abandoned upload is listed and can be aborted.
	id2, err := st.CreateUpload(ctx, prefix+"abandoned.bin")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Abort(ctx, prefix+"abandoned.bin", id2); err != nil {
		t.Fatalf("abort: %v", err)
	}
	if uploads, _ := st.ListUploads(ctx, prefix); len(uploads) != 0 {
		t.Errorf("uploads left: %+v", uploads)
	}

	if err := st.Delete(ctx, key); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := st.Size(ctx, key); !errors.Is(err, ErrNotFound) {
		t.Errorf("size after delete: %v", err)
	}
}
