// Package blobs3 is the blob.s3 module: storage.blob/v1 on any
// S3-compatible store. It is the only building block that knows the S3
// credentials; everyone else gets an area "<prefix>/<id>/" through the core.
package blobs3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"strings"

	"github.com/LNA-DEV/HomePageCompanion/capability/storage"
	"github.com/LNA-DEV/HomePageCompanion/companion"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

func init() {
	companion.Register(companion.Info{
		ID:       "blob.s3",
		Name:     "S3 bucket",
		New:      func() companion.Instance { return new(Store) },
		Provides: []companion.Capability{storage.BlobV1},
		Order:    -90,
	})
}

// DefaultPrefix is the companion's folder when prefix is empty.
const DefaultPrefix = "home-page-companion"

// Store is one bucket.
type Store struct {
	// BucketURL is "s3://<bucket>?endpoint=https://…&region=…", the form
	// config.yaml's storage.bucketUrl used.
	BucketURL string `json:"bucketUrl"`
	// Prefix is the companion's folder in the bucket. Nothing outside it is
	// ever written or deleted.
	Prefix string `json:"prefix"`
	// AccessKeyID and SecretAccessKey; when empty, the AWS default chain
	// (AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY) applies.
	AccessKeyID     string `json:"accessKeyId" companion:"secret"`
	SecretAccessKey string `json:"secretAccessKey" companion:"secret"`
	// Versioned: the bucket keeps old versions itself, so backups may leave
	// the files out.
	Versioned bool `json:"versioned"`

	client *s3.Client
	bucket string
}

func (s *Store) Validate() error {
	if s.BucketURL == "" {
		return errors.New("blob.s3: bucketUrl is required")
	}
	return nil
}

func (s *Store) Provision(ctx companion.Context) error {
	if s.Prefix == "" {
		s.Prefix = DefaultPrefix
	}
	if s.BucketURL == "" {
		return nil
	}
	c, bucket, err := NewClient(context.Background(), s.BucketURL, s.AccessKeyID, s.SecretAccessKey)
	if err != nil {
		return err
	}
	s.client, s.bucket = c, bucket
	return nil
}

// NewClient opens the bucket named by a bucket URL.
func NewClient(ctx context.Context, bucketURL, keyID, secret string) (*s3.Client, string, error) {
	u, err := url.Parse(bucketURL)
	if err != nil {
		return nil, "", err
	}
	if u.Scheme != "s3" || u.Host == "" {
		return nil, "", fmt.Errorf("blob.s3: %q is not an s3://<bucket> URL", redactURL(bucketURL))
	}
	q := u.Query()
	region := q.Get("region")
	if region == "" {
		region = "us-east-1"
	}
	opts := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(region)}
	if keyID != "" && secret != "" {
		opts = append(opts, awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(keyID, secret, "")))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, "", err
	}
	pathStyle := q.Get("use_path_style") == "true" || q.Get("s3ForcePathStyle") == "true"
	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		if ep := q.Get("endpoint"); ep != "" {
			o.BaseEndpoint = aws.String(ep)
		}
		o.UsePathStyle = pathStyle
		// The SDK's default (since early 2025) adds CRC32 checksums to every
		// upload; several S3-compatible services rejected them. Only send
		// what an operation strictly requires.
		o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
		o.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
	})
	return client, u.Host, nil
}

func redactURL(raw string) string {
	if i := strings.IndexByte(raw, '?'); i >= 0 {
		return raw[:i]
	}
	return raw
}

func (s *Store) Bucket(area string) companion.Bucket {
	return &Bucket{Client: s.client, Name: s.bucket, prefix: strings.Trim(path.Join(s.Prefix, area), "/")}
}

func (s *Store) Durable() bool { return s.Versioned }

var _ storage.Blob = (*Store)(nil)

// Bucket is a view of one S3 bucket under a prefix.
type Bucket struct {
	Client *s3.Client
	Name   string
	prefix string
}

// NewBucket is a view without the module; for tests and tools.
func NewBucket(client *s3.Client, name, prefix string) *Bucket {
	return &Bucket{Client: client, Name: name, prefix: strings.Trim(prefix, "/")}
}

func (b *Bucket) Prefix() string { return b.prefix }

func (b *Bucket) full(key string) string { return path.Join(b.prefix, strings.TrimPrefix(key, "/")) }

func (b *Bucket) rel(full string) string {
	return strings.TrimPrefix(strings.TrimPrefix(full, b.prefix), "/")
}

func notFound(err error) bool {
	var nf *types.NotFound
	var nk *types.NoSuchKey
	var ae smithy.APIError
	return errors.As(err, &nf) || errors.As(err, &nk) || (errors.As(err, &ae) && (ae.ErrorCode() == "NotFound" || ae.ErrorCode() == "NoSuchKey"))
}

// classify marks the answers no retry fixes: an upload that takes no more
// parts (NoSuchUpload; AccessDenied on an upload started with other
// credentials) and refused credentials or a missing bucket. The S3 error
// stays inside, for errors.As and the message.
func classify(err error, upload bool) error {
	var ae smithy.APIError
	if err == nil || !errors.As(err, &ae) {
		return err
	}
	switch ae.ErrorCode() {
	case "NoSuchUpload":
		return fmt.Errorf("%w: %w", companion.ErrUploadGone, err)
	case "AccessDenied":
		if upload {
			return fmt.Errorf("%w: %w", companion.ErrUploadGone, err)
		}
		return fmt.Errorf("%w: %w", companion.ErrDenied, err)
	case "InvalidAccessKeyId", "SignatureDoesNotMatch", "NoSuchBucket":
		return fmt.Errorf("%w: %w", companion.ErrDenied, err)
	}
	return err
}

func (b *Bucket) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	in := &s3.PutObjectInput{Bucket: aws.String(b.Name), Key: aws.String(b.full(key)), Body: r}
	if size >= 0 {
		in.ContentLength = aws.Int64(size)
	}
	if contentType != "" {
		in.ContentType = aws.String(contentType)
	}
	if size < 0 {
		data, err := io.ReadAll(r)
		if err != nil {
			return err
		}
		in.Body, in.ContentLength = bytes.NewReader(data), aws.Int64(int64(len(data)))
	}
	_, err := b.Client.PutObject(ctx, in)
	return classify(err, false)
}

func (b *Bucket) Get(ctx context.Context, key string) (io.ReadCloser, companion.ObjectInfo, error) {
	out, err := b.Client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(b.Name), Key: aws.String(b.full(key))})
	if err != nil {
		if notFound(err) {
			return nil, companion.ObjectInfo{}, companion.ErrNotFound
		}
		return nil, companion.ObjectInfo{}, classify(err, false)
	}
	info := companion.ObjectInfo{Key: key, Size: aws.ToInt64(out.ContentLength), ContentType: aws.ToString(out.ContentType),
		ModTime: aws.ToTime(out.LastModified), ETag: aws.ToString(out.ETag)}
	return out.Body, info, nil
}

func (b *Bucket) Stat(ctx context.Context, key string) (companion.ObjectInfo, error) {
	out, err := b.Client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(b.Name), Key: aws.String(b.full(key))})
	if err != nil {
		if notFound(err) {
			return companion.ObjectInfo{}, companion.ErrNotFound
		}
		return companion.ObjectInfo{}, classify(err, false)
	}
	return companion.ObjectInfo{Key: key, Size: aws.ToInt64(out.ContentLength), ContentType: aws.ToString(out.ContentType),
		ModTime: aws.ToTime(out.LastModified), ETag: aws.ToString(out.ETag)}, nil
}

func (b *Bucket) ReadRange(ctx context.Context, key string, offset, length int64) ([]byte, error) {
	out, err := b.Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(b.Name),
		Key:    aws.String(b.full(key)),
		Range:  aws.String(fmt.Sprintf("bytes=%d-%d", offset, offset+length-1)),
	})
	if err != nil {
		if notFound(err) {
			return nil, companion.ErrNotFound
		}
		return nil, classify(err, false)
	}
	defer out.Body.Close()
	return io.ReadAll(out.Body)
}

func (b *Bucket) List(ctx context.Context, prefix string) ([]companion.ObjectInfo, error) {
	p := b.prefix + "/"
	if prefix != "" {
		p = b.full(prefix)
	}
	var out []companion.ObjectInfo
	in := &s3.ListObjectsV2Input{Bucket: aws.String(b.Name), Prefix: aws.String(p)}
	for {
		page, err := b.Client.ListObjectsV2(ctx, in)
		if err != nil {
			return nil, classify(err, false)
		}
		for _, o := range page.Contents {
			out = append(out, companion.ObjectInfo{Key: b.rel(aws.ToString(o.Key)), Size: aws.ToInt64(o.Size),
				ModTime: aws.ToTime(o.LastModified), ETag: aws.ToString(o.ETag)})
		}
		if !aws.ToBool(page.IsTruncated) {
			return out, nil
		}
		in.ContinuationToken = page.NextContinuationToken
	}
}

func (b *Bucket) Delete(ctx context.Context, key string) error {
	_, err := b.Client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(b.Name), Key: aws.String(b.full(key))})
	return classify(err, false)
}

func (b *Bucket) CreateUpload(ctx context.Context, key, contentType string) (string, error) {
	in := &s3.CreateMultipartUploadInput{Bucket: aws.String(b.Name), Key: aws.String(b.full(key))}
	if contentType != "" {
		in.ContentType = aws.String(contentType)
	}
	out, err := b.Client.CreateMultipartUpload(ctx, in)
	if err != nil {
		return "", classify(err, false)
	}
	return aws.ToString(out.UploadId), nil
}

func (b *Bucket) UploadPart(ctx context.Context, key, uploadID string, number int32, data []byte) (string, error) {
	out, err := b.Client.UploadPart(ctx, &s3.UploadPartInput{
		Bucket:        aws.String(b.Name),
		Key:           aws.String(b.full(key)),
		UploadId:      aws.String(uploadID),
		PartNumber:    aws.Int32(number),
		Body:          bytes.NewReader(data),
		ContentLength: aws.Int64(int64(len(data))),
	})
	if err != nil {
		return "", classify(err, true)
	}
	return aws.ToString(out.ETag), nil
}

func (b *Bucket) CompleteUpload(ctx context.Context, key, uploadID string, parts []companion.CompletedPart) error {
	cp := make([]types.CompletedPart, len(parts))
	for i, p := range parts {
		cp[i] = types.CompletedPart{PartNumber: aws.Int32(p.Number), ETag: aws.String(p.ETag)}
	}
	_, err := b.Client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket:          aws.String(b.Name),
		Key:             aws.String(b.full(key)),
		UploadId:        aws.String(uploadID),
		MultipartUpload: &types.CompletedMultipartUpload{Parts: cp},
	})
	return classify(err, true)
}

func (b *Bucket) AbortUpload(ctx context.Context, key, uploadID string) error {
	_, err := b.Client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
		Bucket: aws.String(b.Name), Key: aws.String(b.full(key)), UploadId: aws.String(uploadID),
	})
	return classify(err, true)
}

func (b *Bucket) ListUploads(ctx context.Context, prefix string) ([]companion.Upload, error) {
	p := b.prefix + "/"
	if prefix != "" {
		p = b.full(prefix)
	}
	var out []companion.Upload
	in := &s3.ListMultipartUploadsInput{Bucket: aws.String(b.Name), Prefix: aws.String(p)}
	for {
		page, err := b.Client.ListMultipartUploads(ctx, in)
		if err != nil {
			return nil, classify(err, false)
		}
		for _, u := range page.Uploads {
			out = append(out, companion.Upload{Key: b.rel(aws.ToString(u.Key)), UploadID: aws.ToString(u.UploadId), Initiated: aws.ToTime(u.Initiated)})
		}
		if !aws.ToBool(page.IsTruncated) {
			return out, nil
		}
		in.KeyMarker, in.UploadIdMarker = page.NextKeyMarker, page.NextUploadIdMarker
	}
}
