// Package microblog owns the lifecycle of locally-authored microblog posts:
// creation, federation fan-out, retry, and delete. The actual platform HTTP
// calls live in dedicated *api packages (mastodonapi today).
package microblog

import (
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/autouploader"
	"github.com/LNA-DEV/HomePageCompanion/blueskyapi"
	"github.com/LNA-DEV/HomePageCompanion/config"
	"github.com/LNA-DEV/HomePageCompanion/database"
	"github.com/LNA-DEV/HomePageCompanion/mastodonapi"
	"github.com/LNA-DEV/HomePageCompanion/models"
	"gorm.io/gorm"
)

// publishResult is the platform-neutral outcome of a federation attempt. URL
// is the human-clickable address (for MicroblogPublication.PostUrl), StatusID
// is the remote identifier needed to fetch likes / replies later
// (MicroblogPublication.PostId), and VersionID is an optional secondary
// identifier (Bluesky's CID).
type publishResult struct {
	URL       string
	StatusID  string
	VersionID string
}

// MaxBodyChars is Mastodon's default ceiling. Posts longer than this are
// rejected at create-time with a 400 from the admin endpoint.
const MaxBodyChars = 500

// PublicationResult is what Create / Retry return for each target attempt
// so the admin UI can show immediate per-platform feedback.
type PublicationResult struct {
	Publication models.MicroblogPublication
	Err         error
}

// Create persists the local post, then fans out to every configured target
// in config.Data.Microblog.PublishTo. Each platform's outcome is captured in
// a MicroblogPublication row; failed attempts also produce an UploadAttempt
// row so the existing /uploads page surfaces them.
func Create(body, contentWarning, imageURL, imageAltText string) (*models.MicroblogPost, []PublicationResult, error) {
	body = strings.TrimSpace(body)
	if body == "" && strings.TrimSpace(imageURL) == "" {
		return nil, nil, errors.New("microblog: post must have body or image")
	}
	if len([]rune(body)) > MaxBodyChars {
		return nil, nil, fmt.Errorf("microblog: body exceeds %d characters", MaxBodyChars)
	}

	post, err := createPostRow(body, contentWarning, imageURL, imageAltText)
	if err != nil {
		return nil, nil, err
	}

	results := fanOut(post)
	return post, results, nil
}

// createPostRow inserts a new MicroblogPost with a freshly generated slug.
// On the (astronomically unlikely) slug collision we retry a few times before
// giving up.
func createPostRow(body, contentWarning, imageURL, imageAltText string) (*models.MicroblogPost, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		post := models.MicroblogPost{
			Slug:           newSlug(),
			Body:           body,
			ContentWarning: contentWarning,
			ImageURL:       imageURL,
			ImageAltText:   imageAltText,
		}
		if err := database.Db.Create(&post).Error; err != nil {
			lastErr = err
			continue
		}
		return &post, nil
	}
	return nil, fmt.Errorf("microblog: failed to allocate slug: %w", lastErr)
}

// fanOut publishes the given post to each configured target. Returns one
// PublicationResult per target attempted. Targets whose platform is not
// recognised are skipped (and not reported — they are a config error).
func fanOut(post *models.MicroblogPost) []PublicationResult {
	results := make([]PublicationResult, 0)
	for _, targetName := range config.Data.Microblog.PublishTo {
		target, ok := resolveTarget(targetName)
		if !ok {
			log.Printf("microblog: target %q not configured; skipping", targetName)
			continue
		}
		pub := models.MicroblogPublication{
			PostID:     post.ID,
			TargetName: target.Name,
			Platform:   target.Platform,
			Success:    false,
		}
		if err := database.Db.Create(&pub).Error; err != nil {
			log.Printf("microblog: persisting publication row failed: %v", err)
			results = append(results, PublicationResult{Publication: pub, Err: err})
			continue
		}
		res := publishOnce(post, &pub, target)
		results = append(results, res)
	}
	return results
}

// publishOnce performs the actual platform call for a single publication row
// and updates that row with the outcome. Records an UploadAttempt for /uploads
// observability regardless of outcome.
func publishOnce(post *models.MicroblogPost, pub *models.MicroblogPublication, target config.Target) PublicationResult {
	var (
		result *publishResult
		err    error
	)
	switch target.Platform {
	case "mastodon":
		result, err = publishToMastodon(post, target)
	case "bluesky":
		result, err = publishToBluesky(post, target)
	default:
		err = fmt.Errorf("microblog: unsupported platform %q", target.Platform)
	}

	httpStatus := 0
	if err == nil && result != nil {
		pub.Success = true
		pub.ErrorMessage = ""
		pub.PostUrl = strPtr(result.URL)
		pub.ExternalID = strPtr(result.StatusID)
		if result.VersionID != "" {
			pub.VersionId = strPtr(result.VersionID)
		} else {
			pub.VersionId = nil
		}
		log.Printf("microblog: published slug=%s to %s (%s): %s", post.Slug, target.Platform, target.Name, result.URL)
	} else {
		pub.Success = false
		if err != nil {
			pub.ErrorMessage = err.Error()
		}
		log.Printf("microblog: publish slug=%s to %s (%s) failed: %v", post.Slug, target.Platform, target.Name, err)
	}
	if saveErr := database.Db.Save(pub).Error; saveErr != nil {
		log.Printf("microblog: persisting publication update failed: %v", saveErr)
	}

	autouploader.RecordAttemptWithSource(
		"microblog",
		"microblog/"+post.Slug,
		post.Slug,
		target.Platform,
		target.Name,
		err,
		httpStatus,
	)
	return PublicationResult{Publication: *pub, Err: err}
}

// publishToMastodon performs the media upload (when applicable) followed by
// the status create.
func publishToMastodon(post *models.MicroblogPost, target config.Target) (*publishResult, error) {
	var mediaIDs []string
	if strings.TrimSpace(post.ImageURL) != "" {
		imageBytes, err := autouploader.DownloadImageBytes(post.ImageURL)
		if err != nil {
			return nil, fmt.Errorf("download image for microblog: %w", err)
		}
		media, err := mastodonapi.UploadMedia(target.InstanceUrl, target.PAT, imageBytes, post.ImageAltText)
		if err != nil {
			return nil, err
		}
		mediaIDs = []string{media.ID}
	}
	status, err := mastodonapi.CreateStatus(target.InstanceUrl, target.PAT, post.Body, post.ContentWarning, mediaIDs, "public")
	if err != nil {
		return nil, err
	}
	return &publishResult{URL: status.URL, StatusID: status.ID}, nil
}

// publishToBluesky logs into Bluesky, optionally uploads an image, and
// creates a post. Bluesky has no native content-warning concept so the CW is
// prepended to the post body. Returns the human-clickable bsky.app URL,
// the AT URI (as StatusID), and the post CID (as VersionID).
func publishToBluesky(post *models.MicroblogPost, target config.Target) (*publishResult, error) {
	session, err := blueskyapi.Login(target.Username, target.PAT)
	if err != nil {
		return nil, err
	}

	var blob *blueskyapi.BlobRef
	if strings.TrimSpace(post.ImageURL) != "" {
		imageBytes, err := autouploader.DownloadImageBytes(post.ImageURL)
		if err != nil {
			return nil, fmt.Errorf("download image for microblog: %w", err)
		}
		blob, err = blueskyapi.UploadImage(session, imageBytes, "image/jpeg")
		if err != nil {
			return nil, err
		}
	}

	text := post.Body
	if strings.TrimSpace(post.ContentWarning) != "" {
		text = "CW: " + post.ContentWarning + "\n\n" + text
	}

	ref, err := blueskyapi.CreatePost(session, text, post.ImageAltText, blob, time.Now())
	if err != nil {
		return nil, err
	}

	return &publishResult{
		URL:       blueskyapi.PostWebURL(session.Handle, ref.URI),
		StatusID:  ref.URI,
		VersionID: ref.CID,
	}, nil
}

// Retry re-runs the platform call for an existing publication row. If the
// publication already succeeded it returns an error rather than wasting an
// API call.
func Retry(publicationID uint) (PublicationResult, error) {
	var pub models.MicroblogPublication
	if err := database.Db.First(&pub, publicationID).Error; err != nil {
		return PublicationResult{}, err
	}
	if pub.Success {
		return PublicationResult{Publication: pub}, errors.New("microblog: publication already succeeded")
	}
	var post models.MicroblogPost
	if err := database.Db.First(&post, pub.PostID).Error; err != nil {
		return PublicationResult{}, err
	}
	target, ok := resolveTarget(pub.TargetName)
	if !ok {
		return PublicationResult{Publication: pub}, fmt.Errorf("microblog: target %q no longer configured", pub.TargetName)
	}
	return publishOnce(&post, &pub, target), nil
}

// Delete removes a local post. For each successful publication it attempts a
// best-effort remote delete (404 is treated as success). Returns the first
// non-network error encountered.
func Delete(postID uint) error {
	var post models.MicroblogPost
	if err := database.Db.First(&post, postID).Error; err != nil {
		return err
	}

	var pubs []models.MicroblogPublication
	database.Db.Where("post_id = ?", postID).Find(&pubs)

	for _, pub := range pubs {
		if !pub.Success || pub.ExternalID == nil {
			continue
		}
		target, ok := resolveTarget(pub.TargetName)
		if !ok {
			log.Printf("microblog: cannot remote-delete %s/%s (target gone)", pub.Platform, pub.TargetName)
			continue
		}
		switch pub.Platform {
		case "mastodon":
			if err := mastodonapi.DeleteStatus(target.InstanceUrl, target.PAT, *pub.ExternalID); err != nil && !errors.Is(err, mastodonapi.ErrNotFound) {
				log.Printf("microblog: remote delete on %s (%s) failed: %v", pub.Platform, pub.TargetName, err)
			}
		case "bluesky":
			session, loginErr := blueskyapi.Login(target.Username, target.PAT)
			if loginErr != nil {
				log.Printf("microblog: bluesky login for delete failed: %v", loginErr)
				break
			}
			if err := blueskyapi.DeleteRecord(session, *pub.ExternalID); err != nil {
				log.Printf("microblog: bluesky delete on %s failed: %v", pub.TargetName, err)
			}
		default:
			log.Printf("microblog: no remote-delete implementation for platform %q", pub.Platform)
		}
	}

	return database.Db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("post_id = ?", postID).Delete(&models.MicroblogComment{}).Error; err != nil {
			return err
		}
		if err := tx.Where("post_id = ?", postID).Delete(&models.MicroblogPublication{}).Error; err != nil {
			return err
		}
		return tx.Delete(&models.MicroblogPost{}, postID).Error
	})
}

// resolveTarget returns the named config.Target, or false when missing.
func resolveTarget(name string) (config.Target, bool) {
	for _, t := range config.Data.Targets {
		if t.Name == name {
			return t, true
		}
	}
	return config.Target{}, false
}

// newSlug builds a slug like 20260515T101530Z-3z6rmt that sorts roughly by
// creation time and is filesystem-safe.
func newSlug() string {
	ts := time.Now().UTC().Format("20060102T150405Z")
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		// rand should never fail on Linux; fall back to a time-only slug
		// rather than panic.
		return ts
	}
	suffix := strings.ToLower(strings.TrimRight(base32.StdEncoding.EncodeToString(b[:]), "="))
	return ts + "-" + suffix
}

func strPtr(s string) *string { return &s }
