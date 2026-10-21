package blobs3

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/companion"
	"github.com/LNA-DEV/HomePageCompanion/companiontest"
	"github.com/aws/smithy-go"
	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"
)

// TestConformanceAgainstARealBucket runs storage.blob/v1's conformance
// suite against an S3 service. It is skipped unless BLOB_S3_TEST_URL names
// a bucket, e.g.
//
//	BLOB_S3_TEST_URL='s3://lna-dev?endpoint=https://nbg1.your-objectstorage.com&region=nbg1' \
//	AWS_ACCESS_KEY_ID=… AWS_SECRET_ACCESS_KEY=… go test -run Conformance .
//
// Everything it writes is under "spike/blob-test/<time>/" and removed again.
// BLOB_S3_TEST_PART_MIB sets the first part (5 MiB, S3's minimum, when unset).
func TestConformanceAgainstARealBucket(t *testing.T) {
	bucketURL := os.Getenv("BLOB_S3_TEST_URL")
	if bucketURL == "" {
		t.Skip("BLOB_S3_TEST_URL not set")
	}
	client, name, err := NewClient(context.Background(), bucketURL, os.Getenv("AWS_ACCESS_KEY_ID"), os.Getenv("AWS_SECRET_ACCESS_KEY"))
	if err != nil {
		t.Fatal(err)
	}
	part := 5
	if v, err := strconv.Atoi(os.Getenv("BLOB_S3_TEST_PART_MIB")); err == nil && v >= 5 {
		part = v
	}
	prefix := fmt.Sprintf("spike/blob-test/%d", time.Now().UnixNano())
	companiontest.BlobConformance(t, NewBucket(client, name, prefix), part<<20)
}

func TestRejectsOtherURLs(t *testing.T) {
	for _, u := range []string{"file:///tmp", "s3://", "https://bucket.example.com"} {
		if _, _, err := NewClient(context.Background(), u, "", ""); err == nil {
			t.Errorf("%q accepted", u)
		}
	}
}

func TestValidateNeedsABucket(t *testing.T) {
	if (&Store{}).Validate() == nil {
		t.Fatal("no bucket URL accepted")
	}
}

// TestConformanceAgainstFakeS3 runs the suite against gofakes3, an S3
// server in the test process, so the S3 code path runs on every push.
func TestConformanceAgainstFakeS3(t *testing.T) {
	backend := s3mem.New()
	faker := gofakes3.New(backend)
	srv := httptest.NewServer(faker.Server())
	defer srv.Close()
	if err := backend.CreateBucket("test-bucket"); err != nil {
		t.Fatal(err)
	}
	client, name, err := NewClient(context.Background(), "s3://test-bucket?endpoint="+srv.URL+"&region=us-east-1&use_path_style=true", "key", "secret")
	if err != nil {
		t.Fatal(err)
	}
	companiontest.BlobConformance(t, NewBucket(client, name, "home-page-companion/test"), 5<<20)
}

func TestClassifyMarksWhatNoRetryFixes(t *testing.T) {
	api := func(code string) error { return &smithy.GenericAPIError{Code: code, Message: "m"} }
	for _, c := range []struct {
		err    error
		upload bool
		want   error
	}{
		{api("NoSuchUpload"), true, companion.ErrUploadGone},
		{api("AccessDenied"), true, companion.ErrUploadGone},
		{api("AccessDenied"), false, companion.ErrDenied},
		{api("InvalidAccessKeyId"), false, companion.ErrDenied},
		{api("SignatureDoesNotMatch"), true, companion.ErrDenied},
		{api("NoSuchBucket"), false, companion.ErrDenied},
		{api("SlowDown"), true, nil},
		{errors.New("connection reset"), true, nil},
	} {
		got := classify(c.err, c.upload)
		if c.want == nil {
			if got != c.err {
				t.Errorf("%v: changed to %v", c.err, got)
			}
			continue
		}
		var ae smithy.APIError
		if !errors.Is(got, c.want) || !errors.As(got, &ae) || !strings.Contains(got.Error(), ae.ErrorCode()) {
			t.Errorf("%v (upload %v): %v, want %v with the S3 error inside", c.err, c.upload, got, c.want)
		}
	}
	if classify(nil, true) != nil {
		t.Error("nil is an error")
	}
}
