package basemap

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// Store is what the update job needs from the bucket: a resumable multipart
// upload plus the few reads and deletes around it. Keys are full object keys
// ("basemap/20261005.pmtiles").
type Store interface {
	CreateUpload(ctx context.Context, key string) (string, error)
	UploadPart(ctx context.Context, key, uploadID string, number int32, data []byte) (string, error)
	Complete(ctx context.Context, key, uploadID string, parts []CompletedPart) error
	Abort(ctx context.Context, key, uploadID string) error
	Size(ctx context.Context, key string) (int64, error)
	ReadRange(ctx context.Context, key string, offset, length int64) ([]byte, error)
	Delete(ctx context.Context, key string) error
	ListUploads(ctx context.Context, prefix string) ([]Upload, error)
}

// CompletedPart is one finished part of a multipart upload.
type CompletedPart struct {
	Number int32
	ETag   string
}

// Upload is an unfinished multipart upload found in the bucket.
type Upload struct {
	Key       string
	UploadID  string
	Initiated time.Time
}

// ErrNotFound is returned by Size for a missing object.
var ErrNotFound = errors.New("object not found")

// OpenStore opens the bucket named by a gocloud-style URL, the same string
// the tile server opens: "s3://bucket?endpoint=…&region=…" or "file:///dir".
func OpenStore(ctx context.Context, bucketURL string) (Store, error) {
	u, err := url.Parse(bucketURL)
	if err != nil {
		return nil, err
	}
	switch u.Scheme {
	case "file":
		return &DirStore{Root: u.Path}, nil
	case "s3":
		return openS3(ctx, u)
	}
	return nil, fmt.Errorf("unsupported bucket URL scheme %q", u.Scheme)
}

// ---- S3 ----

// S3Store talks to any S3-compatible service through the AWS SDK.
type S3Store struct {
	Client *s3.Client
	Bucket string
}

func openS3(ctx context.Context, u *url.URL) (*S3Store, error) {
	q := u.Query()
	region := q.Get("region")
	if region == "" {
		region = "us-east-1"
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return nil, err
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
	return &S3Store{Client: client, Bucket: u.Host}, nil
}

func (s *S3Store) CreateUpload(ctx context.Context, key string) (string, error) {
	out, err := s.Client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket:      aws.String(s.Bucket),
		Key:         aws.String(key),
		ContentType: aws.String("application/vnd.pmtiles"),
	})
	if err != nil {
		return "", err
	}
	return aws.ToString(out.UploadId), nil
}

func (s *S3Store) UploadPart(ctx context.Context, key, uploadID string, number int32, data []byte) (string, error) {
	out, err := s.Client.UploadPart(ctx, &s3.UploadPartInput{
		Bucket:        aws.String(s.Bucket),
		Key:           aws.String(key),
		UploadId:      aws.String(uploadID),
		PartNumber:    aws.Int32(number),
		Body:          bytes.NewReader(data),
		ContentLength: aws.Int64(int64(len(data))),
	})
	if err != nil {
		return "", err
	}
	return aws.ToString(out.ETag), nil
}

func (s *S3Store) Complete(ctx context.Context, key, uploadID string, parts []CompletedPart) error {
	cp := make([]types.CompletedPart, len(parts))
	for i, p := range parts {
		cp[i] = types.CompletedPart{PartNumber: aws.Int32(p.Number), ETag: aws.String(p.ETag)}
	}
	_, err := s.Client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket:          aws.String(s.Bucket),
		Key:             aws.String(key),
		UploadId:        aws.String(uploadID),
		MultipartUpload: &types.CompletedMultipartUpload{Parts: cp},
	})
	return err
}

func (s *S3Store) Abort(ctx context.Context, key, uploadID string) error {
	_, err := s.Client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
		Bucket: aws.String(s.Bucket), Key: aws.String(key), UploadId: aws.String(uploadID),
	})
	return err
}

func (s *S3Store) Size(ctx context.Context, key string) (int64, error) {
	out, err := s.Client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.Bucket), Key: aws.String(key)})
	if err != nil {
		var nf *types.NotFound
		if errors.As(err, &nf) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	return aws.ToInt64(out.ContentLength), nil
}

func (s *S3Store) ReadRange(ctx context.Context, key string, offset, length int64) ([]byte, error) {
	out, err := s.Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.Bucket),
		Key:    aws.String(key),
		Range:  aws.String(fmt.Sprintf("bytes=%d-%d", offset, offset+length-1)),
	})
	if err != nil {
		return nil, err
	}
	defer out.Body.Close()
	return io.ReadAll(out.Body)
}

func (s *S3Store) Delete(ctx context.Context, key string) error {
	_, err := s.Client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.Bucket), Key: aws.String(key)})
	return err
}

func (s *S3Store) ListUploads(ctx context.Context, prefix string) ([]Upload, error) {
	var out []Upload
	in := &s3.ListMultipartUploadsInput{Bucket: aws.String(s.Bucket), Prefix: aws.String(prefix)}
	for {
		page, err := s.Client.ListMultipartUploads(ctx, in)
		if err != nil {
			return nil, err
		}
		for _, u := range page.Uploads {
			out = append(out, Upload{Key: aws.ToString(u.Key), UploadID: aws.ToString(u.UploadId), Initiated: aws.ToTime(u.Initiated)})
		}
		if !aws.ToBool(page.IsTruncated) {
			return out, nil
		}
		in.KeyMarker, in.UploadIdMarker = page.NextKeyMarker, page.NextUploadIdMarker
	}
}

// ---- a directory, for development and tests ----

// DirStore keeps objects as files under Root and an upload's parts under
// Root/.uploads/<id>/, so the job runs unchanged against a local directory
// that the tile server (a file:// bucket) can serve.
type DirStore struct {
	Root string
}

func (d *DirStore) path(key string) string { return filepath.Join(d.Root, filepath.FromSlash(key)) }
func (d *DirStore) uploadDir(id string) string {
	return filepath.Join(d.Root, ".uploads", id)
}

func (d *DirStore) CreateUpload(_ context.Context, key string) (string, error) {
	id := strconv.FormatInt(time.Now().UnixNano(), 36)
	if err := os.MkdirAll(d.uploadDir(id), 0o755); err != nil {
		return "", err
	}
	return id, os.WriteFile(filepath.Join(d.uploadDir(id), "key"), []byte(key), 0o644)
}

func (d *DirStore) UploadPart(_ context.Context, _, uploadID string, number int32, data []byte) (string, error) {
	dir := d.uploadDir(uploadID)
	if _, err := os.Stat(dir); err != nil {
		return "", fmt.Errorf("no such upload %s", uploadID)
	}
	name := filepath.Join(dir, fmt.Sprintf("%05d", number))
	if err := os.WriteFile(name, data, 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("\"%d-%d\"", number, len(data)), nil
}

func (d *DirStore) Complete(_ context.Context, key, uploadID string, parts []CompletedPart) error {
	dir := d.uploadDir(uploadID)
	dst := d.path(key)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	sorted := append([]CompletedPart(nil), parts...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Number < sorted[j].Number })
	for _, p := range sorted {
		b, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("%05d", p.Number)))
		if err != nil {
			f.Close()
			return err
		}
		if _, err := f.Write(b); err != nil {
			f.Close()
			return err
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

func (d *DirStore) Abort(_ context.Context, _, uploadID string) error {
	return os.RemoveAll(d.uploadDir(uploadID))
}

func (d *DirStore) Size(_ context.Context, key string) (int64, error) {
	fi, err := os.Stat(d.path(key))
	if errors.Is(err, os.ErrNotExist) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

func (d *DirStore) ReadRange(_ context.Context, key string, offset, length int64) ([]byte, error) {
	f, err := os.Open(d.path(key))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b := make([]byte, length)
	n, err := f.ReadAt(b, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return b[:n], nil
}

func (d *DirStore) Delete(_ context.Context, key string) error {
	err := os.Remove(d.path(key))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (d *DirStore) ListUploads(_ context.Context, prefix string) ([]Upload, error) {
	entries, err := os.ReadDir(filepath.Join(d.Root, ".uploads"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Upload
	for _, e := range entries {
		key, err := os.ReadFile(filepath.Join(d.uploadDir(e.Name()), "key"))
		if err != nil || !strings.HasPrefix(string(key), prefix) {
			continue
		}
		info, _ := e.Info()
		var initiated time.Time
		if info != nil {
			initiated = info.ModTime()
		}
		out = append(out, Upload{Key: string(key), UploadID: e.Name(), Initiated: initiated})
	}
	return out, nil
}
