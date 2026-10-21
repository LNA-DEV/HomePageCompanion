package basemap

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/companion"
	"golang.org/x/sync/errgroup"
	"gorm.io/gorm"
)

// ErrRunning is returned when a run is already in progress.
var ErrRunning = errors.New("a basemap update is already running")

// Job copies a daily build into the blob store and switches the site over to
// it.
//
//  1. resume a version left in "copying"/"verifying", or find the newest build;
//  2. read its header and metadata and refuse it before copying if it is not
//     what the style is written for (Inspection.Check);
//  3. copy it part by part — a ranged GET into an UploadPart — recording each
//     finished part, so a crash or restart resumes at the next missing one;
//  4. complete the upload, compare size and header with the source, and read
//     the TileJSON and sample tiles through the serving path;
//  5. activate it; the previous version is retained for RetainDays.
type Job struct {
	// Bucket is the module's blob area, looked up per run so a reload that
	// changed the blob module takes effect.
	Bucket      func() companion.Bucket
	DB          func() *gorm.DB
	Log         *log.Logger
	Source      Source
	SchemaMajor int
	PartSize    int64
	Concurrency int
	RetainDays  int
	Now         func() time.Time
	// Verify reads a version through the serving path (service.verifyServing).
	Verify func(ctx context.Context, version string) error
	// OnChange runs after a version was activated or removed.
	OnChange func()
	// Retries per part and the pause between them (5 and 2 s when zero).
	Retries    int
	RetryPause time.Duration

	mu       sync.Mutex
	running  bool
	progress Progress
	lastErr  string
	lastRun  *time.Time
	dbMu     sync.Mutex
}

// Progress is the state of the running copy, for the admin page.
type Progress struct {
	Version    string    `json:"version"`
	Phase      string    `json:"phase"`
	PartsDone  int       `json:"partsDone"`
	PartsTotal int       `json:"partsTotal"`
	BytesDone  int64     `json:"bytesDone"`
	BytesTotal int64     `json:"bytesTotal"`
	StartedAt  time.Time `json:"startedAt"`
	// BytesThisRun counts only what this run copied, for the transfer rate.
	BytesThisRun int64 `json:"bytesThisRun"`
}

// Status is the job's state as the admin page shows it.
type Status struct {
	Running  bool       `json:"running"`
	Progress *Progress  `json:"progress,omitempty"`
	LastErr  string     `json:"lastError,omitempty"`
	LastRun  *time.Time `json:"lastRun,omitempty"`
}

func (j *Job) Status() Status {
	j.mu.Lock()
	defer j.mu.Unlock()
	st := Status{Running: j.running, LastErr: j.lastErr, LastRun: j.lastRun}
	if j.running {
		p := j.progress
		st.Progress = &p
	}
	return st
}

func (j *Job) begin() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.running {
		return false
	}
	j.running = true
	j.progress = Progress{StartedAt: j.Now().UTC(), Phase: "starting"}
	return true
}

func (j *Job) end(err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.running = false
	now := j.Now().UTC()
	j.lastRun = &now
	j.lastErr = ""
	if err != nil {
		j.lastErr = err.Error()
	}
}

func (j *Job) setPhase(phase string) {
	j.mu.Lock()
	j.progress.Phase = phase
	j.mu.Unlock()
	j.Log.Print(phase)
}

// key is a version's object, relative to the module's area; the tile server
// reads "<version>.pmtiles".
func objectKey(version string) string { return version + ".pmtiles" }

// Run performs one update. It returns nil when the newest build is already
// active.
func (j *Job) Run(ctx context.Context) (err error) {
	if !j.begin() {
		return ErrRunning
	}
	defer func() {
		if err != nil {
			j.Log.Printf("update failed: %v", err)
		}
		j.end(err)
	}()

	// A resumed upload the bucket no longer accepts is given up and the build
	// copied afresh, once, in the same run.
	for attempt := 0; ; attempt++ {
		restart, err := j.runOnce(ctx)
		if !restart || attempt > 0 {
			return err
		}
		j.Log.Printf("%v — starting the copy over", err)
	}
}

// runOnce is one pass of Run. restart reports that it gave up a resumed upload
// the bucket refused, so a fresh copy may succeed.
func (j *Job) runOnce(ctx context.Context) (restart bool, err error) {
	v, b, resumed, err := j.resumeOrStart(ctx)
	if err != nil || v == nil {
		return false, err
	}
	j.mu.Lock()
	j.progress.Version, j.progress.PartsTotal, j.progress.BytesTotal = v.Version, v.PartsTotal, v.Size
	j.mu.Unlock()
	if resumed {
		j.setPhase(fmt.Sprintf("resuming %s", v.Version))
	}

	if v.Status == StatusCopying {
		j.setPhase(fmt.Sprintf("copying %s (%d parts of %d MiB)", v.Version, v.PartsTotal, v.PartSize>>20))
		if err := j.copyParts(ctx, v, b); err != nil {
			if errors.Is(err, errSourceChanged) || errors.Is(err, errSourceGone) || uploadUnusable(err) {
				j.fail(ctx, v, err)
				return resumed && uploadUnusable(err), err
			}
			// Anything else is worth resuming: keep the parts, note the error.
			j.save(v, func(v *Version) { v.Error = err.Error() })
			return false, err
		}
		j.setPhase(fmt.Sprintf("completing %s", v.Version))
		if err := j.complete(ctx, v); err != nil {
			if uploadUnusable(err) {
				j.fail(ctx, v, err)
				return resumed, err
			}
			j.save(v, func(v *Version) { v.Error = err.Error() })
			return false, err
		}
	}

	j.setPhase(fmt.Sprintf("verifying %s", v.Version))
	if err := j.verify(ctx, v, b); err != nil {
		err = fmt.Errorf("verification of %s failed: %w", v.Version, err)
		j.fail(ctx, v, err)
		_ = j.Bucket().Delete(ctx, v.Key)
		return false, err
	}

	j.setPhase(fmt.Sprintf("activating %s", v.Version))
	if err := j.activate(v); err != nil {
		return false, err
	}
	j.Log.Printf("%s (schema %s, OSM %s) is now active", v.Version, v.SchemaVersion, v.OSMTime)
	return false, nil
}

// resumeOrStart picks up an interrupted version, or inspects the newest build
// and creates its row and upload. A nil version means there is nothing to do.
func (j *Job) resumeOrStart(ctx context.Context) (*Version, Build, bool, error) {
	var v Version
	if j.DB().Where("status IN ?", []string{StatusCopying, StatusVerifying}).
		Order("id DESC").Limit(1).Find(&v).RowsAffected > 0 {
		b, err := j.Source.Head(ctx, v.Version)
		switch {
		case err == nil && b.Size == v.Size && (v.SourceETag == "" || b.ETag == v.SourceETag):
			return &v, b, true, nil
		case err == nil || errors.Is(err, ErrNotFound):
			// Gone or changed at the source: this copy can never finish.
			j.fail(ctx, &v, errSourceGone)
		default:
			return nil, Build{}, false, fmt.Errorf("checking the interrupted copy %s: %w", v.Version, err)
		}
	}

	j.setPhase("looking for the newest build")
	b, err := j.Source.Find(ctx, j.Now())
	if err != nil {
		return nil, Build{}, false, err
	}
	var existing Version
	if j.DB().Where("version = ?", b.Version).Limit(1).Find(&existing).RowsAffected > 0 &&
		(existing.Status == StatusActive || existing.Status == StatusRetained) {
		j.Log.Printf("build %s is already %s, nothing to do", b.Version, existing.Status)
		return nil, Build{}, false, nil
	}

	j.setPhase(fmt.Sprintf("inspecting %s", b.Version))
	in, err := j.Source.Inspect(ctx, b)
	if err != nil {
		return nil, Build{}, false, fmt.Errorf("inspecting %s: %w", b.Version, err)
	}
	if err := in.Check(j.SchemaMajor); err != nil {
		return nil, Build{}, false, fmt.Errorf("refusing build %s: %w", b.Version, err)
	}

	key := objectKey(b.Version)
	uploadID, err := j.Bucket().CreateUpload(ctx, key, "application/vnd.pmtiles")
	if err != nil {
		return nil, Build{}, false, fmt.Errorf("creating the upload: %w", err)
	}
	parts := int((b.Size + j.PartSize - 1) / j.PartSize)
	if parts > 10000 {
		_ = j.Bucket().AbortUpload(ctx, key, uploadID)
		return nil, Build{}, false, fmt.Errorf("%d parts exceed S3's 10,000; raise partSizeMiB", parts)
	}
	v = Version{
		Version: b.Version, Key: key, Size: b.Size, SourceURL: b.URL, SourceETag: b.ETag,
		SchemaVersion: in.SchemaVersion(), OSMTime: in.OSMTime(),
		Status: StatusCopying, UploadID: uploadID, PartSize: j.PartSize, PartsTotal: parts,
	}
	if existing.ID != 0 {
		// A failed earlier attempt at the same build: start it over.
		j.DB().Where("version_id = ?", existing.ID).Delete(&Part{})
		v.ID, v.CreatedAt = existing.ID, existing.CreatedAt
	}
	if err := j.DB().Save(&v).Error; err != nil {
		_ = j.Bucket().AbortUpload(ctx, key, uploadID)
		return nil, Build{}, false, err
	}
	return &v, b, false, nil
}

func (j *Job) copyParts(ctx context.Context, v *Version, b Build) error {
	var done []Part
	j.DB().Where("version_id = ?", v.ID).Find(&done)
	have := map[int32]bool{}
	var doneBytes int64
	for _, p := range done {
		have[p.Number] = true
		doneBytes += p.Size
	}
	j.mu.Lock()
	j.progress.PartsDone, j.progress.BytesDone = len(have), doneBytes
	j.mu.Unlock()

	g, gctx := errgroup.WithContext(ctx)
	work := make(chan int32)
	for w := 0; w < max(1, j.Concurrency); w++ {
		g.Go(func() error {
			for n := range work {
				if err := j.copyPart(gctx, v, b, n); err != nil {
					return fmt.Errorf("part %d: %w", n, err)
				}
			}
			return nil
		})
	}
	g.Go(func() error {
		defer close(work)
		for n := int32(1); n <= int32(v.PartsTotal); n++ {
			if have[n] {
				continue
			}
			select {
			case work <- n:
			case <-gctx.Done():
				return nil
			}
		}
		return nil
	})
	return g.Wait()
}

func (j *Job) copyPart(ctx context.Context, v *Version, b Build, n int32) error {
	offset := int64(n-1) * v.PartSize
	length := min(v.PartSize, v.Size-offset)

	data, err := retry(ctx, j, func() ([]byte, error) { return j.Source.ReadRange(ctx, b, offset, length) })
	if err != nil {
		return err
	}
	etag, err := retry(ctx, j, func() (string, error) { return j.Bucket().UploadPart(ctx, v.Key, v.UploadID, n, data) })
	if err != nil {
		return err
	}
	j.dbMu.Lock()
	err = j.DB().Create(&Part{VersionID: v.ID, Number: n, ETag: etag, Size: length}).Error
	j.dbMu.Unlock()
	if err != nil {
		return err
	}
	j.mu.Lock()
	j.progress.PartsDone++
	j.progress.BytesDone += length
	j.progress.BytesThisRun += length
	j.mu.Unlock()
	return nil
}

// retry runs f until it succeeds, the context ends, or the error is one no
// retry can fix.
func retry[T any](ctx context.Context, j *Job, f func() (T, error)) (T, error) {
	attempts, pause := j.Retries, j.RetryPause
	if attempts <= 0 {
		attempts = 5
	}
	if pause <= 0 {
		pause = 2 * time.Second
	}
	var zero T
	var err error
	for i := 0; i < attempts; i++ {
		var v T
		if v, err = f(); err == nil {
			return v, nil
		}
		if errors.Is(err, errSourceChanged) || errors.Is(err, errSourceGone) || noRetry(err) || ctx.Err() != nil {
			return zero, err
		}
		select {
		case <-time.After(pause * time.Duration(1<<i)):
		case <-ctx.Done():
			return zero, ctx.Err()
		}
	}
	return zero, err
}

// uploadUnusable reports that the store will not take more parts for this
// multipart upload: it is gone, or it belongs to somebody else. S3 answers
// AccessDenied when the requester did not initiate the upload, which is what
// happens when the credentials changed between a copy and its resumption
// (the old key may even be deleted). Such an upload is given up, never
// retried. The blob module marks both as companion.ErrUploadGone.
func uploadUnusable(err error) bool { return errors.Is(err, companion.ErrUploadGone) }

// noRetry is an answer that waiting will not change: the upload is unusable,
// or the credentials or the bucket are wrong.
func noRetry(err error) bool { return uploadUnusable(err) || errors.Is(err, companion.ErrDenied) }

func (j *Job) complete(ctx context.Context, v *Version) error {
	var rows []Part
	j.DB().Where("version_id = ?", v.ID).Find(&rows)
	if len(rows) != v.PartsTotal {
		return fmt.Errorf("%d of %d parts recorded", len(rows), v.PartsTotal)
	}
	sort.Slice(rows, func(a, b int) bool { return rows[a].Number < rows[b].Number })
	parts := make([]companion.CompletedPart, len(rows))
	for i, r := range rows {
		parts[i] = companion.CompletedPart{Number: r.Number, ETag: r.ETag}
	}
	if err := j.Bucket().CompleteUpload(ctx, v.Key, v.UploadID, parts); err != nil {
		return err
	}
	j.save(v, func(v *Version) { v.Status, v.Error = StatusVerifying, "" })
	j.DB().Where("version_id = ?", v.ID).Delete(&Part{})
	return nil
}

func (j *Job) verify(ctx context.Context, v *Version, b Build) error {
	info, err := j.Bucket().Stat(ctx, v.Key)
	if err != nil {
		return fmt.Errorf("size: %w", err)
	}
	if info.Size != v.Size {
		return fmt.Errorf("the bucket holds %d bytes, the source %d", info.Size, v.Size)
	}
	ours, err := j.Bucket().ReadRange(ctx, v.Key, 0, 127)
	if err != nil {
		return fmt.Errorf("reading the header back: %w", err)
	}
	theirs, err := j.Source.ReadRange(ctx, b, 0, 127)
	if err != nil {
		return fmt.Errorf("reading the source header: %w", err)
	}
	if !bytes.Equal(ours, theirs) {
		return errors.New("the copied header differs from the source's")
	}
	if j.Verify != nil {
		if err := j.Verify(ctx, v.Version); err != nil {
			return err
		}
	}
	return nil
}

func (j *Job) activate(v *Version) error {
	now := j.Now().UTC()
	deleteAfter := now.AddDate(0, 0, j.RetainDays)
	err := j.DB().Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&Version{}).
			Where("status = ? AND id <> ?", StatusActive, v.ID).
			Updates(map[string]any{"status": StatusRetained, "delete_after": deleteAfter}).Error; err != nil {
			return err
		}
		v.Status, v.Error, v.ActivatedAt, v.DeleteAfter = StatusActive, "", &now, nil
		return tx.Save(v).Error
	})
	if err != nil {
		return err
	}
	if j.OnChange != nil {
		j.OnChange()
	}
	return nil
}

// fail marks a version as failed for good and gives up its upload.
func (j *Job) fail(ctx context.Context, v *Version, cause error) {
	if v.UploadID != "" && v.Status == StatusCopying {
		if err := j.Bucket().AbortUpload(ctx, v.Key, v.UploadID); err != nil {
			j.Log.Printf("aborting upload of %s: %v", v.Version, err)
		}
	}
	j.DB().Where("version_id = ?", v.ID).Delete(&Part{})
	j.save(v, func(v *Version) { v.Status, v.Error = StatusFailed, cause.Error() })
}

func (j *Job) save(v *Version, change func(*Version)) {
	j.dbMu.Lock()
	defer j.dbMu.Unlock()
	change(v)
	if err := j.DB().Save(v).Error; err != nil {
		j.Log.Printf("saving %s: %v", v.Version, err)
	}
}

// Cleanup deletes retained versions whose time is up and aborts unfinished
// uploads in the module's area that no running copy owns and that are older
// than two days. It touches nothing outside the area. One item it cannot remove
// does not stop the others; the failures come back joined.
func (j *Job) Cleanup(ctx context.Context) (deleted, aborted int, err error) {
	now := j.Now().UTC()
	var failures []error
	var expired []Version
	j.DB().Where("status = ? AND delete_after IS NOT NULL AND delete_after < ?", StatusRetained, now).Find(&expired)
	for i := range expired {
		v := &expired[i]
		if err := j.Bucket().Delete(ctx, v.Key); err != nil {
			failures = append(failures, fmt.Errorf("deleting %s: %w", v.Key, err))
			continue
		}
		j.save(v, func(v *Version) { v.Status = StatusDeleted })
		deleted++
	}
	if deleted > 0 && j.OnChange != nil {
		j.OnChange()
	}

	// The module's area is its own: every upload in it is the job's.
	uploads, err := j.Bucket().ListUploads(ctx, "")
	if err != nil {
		return deleted, aborted, errors.Join(append(failures, fmt.Errorf("listing uploads: %w", err))...)
	}
	var live []Version
	j.DB().Where("status = ?", StatusCopying).Find(&live)
	owned := map[string]bool{}
	for _, v := range live {
		owned[v.UploadID] = true
	}
	for _, u := range uploads {
		if owned[u.UploadID] || now.Sub(u.Initiated) < 48*time.Hour {
			continue
		}
		// An upload started with credentials that were since replaced may not
		// be ours to abort any more (AccessDenied); it is reported, not fatal.
		if err := j.Bucket().AbortUpload(ctx, u.Key, u.UploadID); err != nil {
			failures = append(failures, fmt.Errorf("aborting %s (%s): %w", u.Key, u.UploadID, err))
			continue
		}
		aborted++
	}
	if deleted+aborted > 0 {
		j.Log.Printf("cleanup deleted %d version(s), aborted %d stale upload(s)", deleted, aborted)
	}
	return deleted, aborted, errors.Join(failures...)
}
