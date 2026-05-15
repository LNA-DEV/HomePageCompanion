package interactions

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/blueskyapi"
	"github.com/LNA-DEV/HomePageCompanion/config"
	"github.com/LNA-DEV/HomePageCompanion/database"
	"github.com/LNA-DEV/HomePageCompanion/mastodonapi"
	"github.com/LNA-DEV/HomePageCompanion/models"
)

// refreshMicroblogLike pulls the like count for a federated microblog
// publication and upserts an Interaction row scoped to Source="microblog".
func refreshMicroblogLike(p Pair) error {
	pub, target, err := loadMicroblogPair(p)
	if err != nil {
		return err
	}
	cfg := DefaultRetryConfig()
	switch pub.Platform {
	case "mastodon":
		resp, e := RetryWithBackoff(cfg, func() (*mastodonapi.LikesResponse, error) {
			r, err := mastodonapi.ListStatusLikes(target.InstanceUrl, target.PAT, *pub.ExternalID)
			if errors.Is(err, mastodonapi.ErrRateLimited) {
				return nil, ErrRateLimited
			}
			return r, err
		})
		if e != nil {
			return e
		}
		now := time.Now()
		pub.LikesRefreshedAt = &now
		database.Db.Save(pub)
		return upsertInteraction("microblog", p.ItemID, pub.Platform, pub.TargetName, len(resp.Accounts))
	case "bluesky":
		if pub.VersionId == nil || *pub.VersionId == "" {
			return fmt.Errorf("bluesky microblog publication %d missing CID", pub.ID)
		}
		session, loginErr := blueskyapi.Login(target.Username, target.PAT)
		if loginErr != nil {
			return translateBluesky(loginErr)
		}
		resp, e := RetryWithBackoff(cfg, func() (*blueskyapi.LikesResponse, error) {
			r, err := blueskyapi.ListPostLikes(session, *pub.ExternalID, *pub.VersionId)
			if errors.Is(err, blueskyapi.ErrRateLimited) {
				return nil, ErrRateLimited
			}
			return r, err
		})
		if e != nil {
			return e
		}
		now := time.Now()
		pub.LikesRefreshedAt = &now
		database.Db.Save(pub)
		return upsertInteraction("microblog", p.ItemID, pub.Platform, pub.TargetName, len(resp.Likes))
	default:
		return fmt.Errorf("microblog likes: unsupported platform %q", pub.Platform)
	}
}

// refreshMicroblogComments pulls the descendants tree for a federated
// microblog publication and upserts MicroblogComment rows. (Platform,
// ExternalID) is the dedupe key so the operation is idempotent.
func refreshMicroblogComments(p Pair) error {
	pub, target, err := loadMicroblogPair(p)
	if err != nil {
		return err
	}
	cfg := DefaultRetryConfig()
	switch pub.Platform {
	case "mastodon":
		ctx, e := RetryWithBackoff(cfg, func() (*mastodonapi.Context, error) {
			r, err := mastodonapi.ListStatusContext(target.InstanceUrl, target.PAT, *pub.ExternalID)
			if errors.Is(err, mastodonapi.ErrRateLimited) {
				return nil, ErrRateLimited
			}
			return r, err
		})
		if e != nil {
			return e
		}
		for _, s := range ctx.Descendants {
			c := models.MicroblogComment{
				PostID:     pub.PostID,
				Platform:   pub.Platform,
				ExternalID: s.ID,
				Author:     s.Account.Acct,
				AuthorURL:  s.Account.URL,
				AvatarURL:  firstNonEmpty(s.Account.AvatarStatic, s.Account.Avatar),
				Body:       s.Content,
				PostedAt:   s.CreatedAt,
				ImportedAt: time.Now(),
			}
			// Idempotent upsert keyed on (platform, external_id).
			var existing models.MicroblogComment
			err := database.Db.
				Where("platform = ? AND external_id = ?", c.Platform, c.ExternalID).
				First(&existing).Error
			if err == nil {
				existing.Author = c.Author
				existing.AuthorURL = c.AuthorURL
				existing.AvatarURL = c.AvatarURL
				existing.Body = c.Body
				existing.ImportedAt = c.ImportedAt
				database.Db.Save(&existing)
			} else {
				database.Db.Create(&c)
			}
		}
		now := time.Now()
		pub.CommentsRefreshedAt = &now
		database.Db.Save(pub)
		return nil
	case "bluesky":
		session, loginErr := blueskyapi.Login(target.Username, target.PAT)
		if loginErr != nil {
			return translateBluesky(loginErr)
		}
		view, e := RetryWithBackoff(cfg, func() (*blueskyapi.ThreadView, error) {
			r, err := blueskyapi.GetPostThread(session, *pub.ExternalID, 1)
			if errors.Is(err, blueskyapi.ErrRateLimited) {
				return nil, ErrRateLimited
			}
			return r, err
		})
		if e != nil {
			return e
		}
		for _, reply := range view.Thread.Replies {
			p := reply.Thread.Post
			if p.URI == "" {
				continue
			}
			c := models.MicroblogComment{
				PostID:     pub.PostID,
				Platform:   "bluesky",
				ExternalID: p.URI,
				Author:     p.Author.Handle,
				AuthorURL:  "https://bsky.app/profile/" + p.Author.Handle,
				AvatarURL:  p.Author.Avatar,
				Body:       p.Record.Text,
				PostedAt:   p.Record.CreatedAt,
				ImportedAt: time.Now(),
			}
			var existing models.MicroblogComment
			err := database.Db.
				Where("platform = ? AND external_id = ?", c.Platform, c.ExternalID).
				First(&existing).Error
			if err == nil {
				existing.Author = c.Author
				existing.AuthorURL = c.AuthorURL
				existing.AvatarURL = c.AvatarURL
				existing.Body = c.Body
				existing.ImportedAt = c.ImportedAt
				database.Db.Save(&existing)
			} else {
				database.Db.Create(&c)
			}
		}
		now := time.Now()
		pub.CommentsRefreshedAt = &now
		database.Db.Save(pub)
		return nil
	default:
		return fmt.Errorf("microblog comments: unsupported platform %q", pub.Platform)
	}
}

// translateBluesky converts blueskyapi.ErrRateLimited into the interactions
// package's local sentinel so RetryWithBackoff can pace responses uniformly.
func translateBluesky(err error) error {
	if errors.Is(err, blueskyapi.ErrRateLimited) {
		return ErrRateLimited
	}
	return err
}

// ErrRefreshBusy is returned by RefreshSinglePost when another paced refresh
// (cron tick or manual full sweep) is currently holding runMu. Callers should
// surface this as a "try again" message rather than a hard failure.
var ErrRefreshBusy = errors.New("interactions: refresh busy")

// RefreshSinglePost performs an on-demand refresh of the likes + comments
// for every successful publication of one microblog post, ignoring the
// staleness-based pacing the cron tick uses. Inter-request gaps are still
// honoured so platforms never see a burst.
//
// Returns ErrRefreshBusy when another paced refresh is already running.
func RefreshSinglePost(postID uint) error {
	if !runMu.TryLock() {
		return ErrRefreshBusy
	}
	defer runMu.Unlock()

	var pubs []models.MicroblogPublication
	if err := database.Db.
		Where("post_id = ? AND success = ?", postID, true).
		Find(&pubs).Error; err != nil {
		return fmt.Errorf("load publications: %w", err)
	}
	if len(pubs) == 0 {
		return nil
	}

	var post models.MicroblogPost
	if err := database.Db.First(&post, postID).Error; err != nil {
		return fmt.Errorf("load post: %w", err)
	}

	pairs := make([]Pair, 0, len(pubs)*2)
	for _, pub := range pubs {
		pairs = append(pairs, Pair{
			Kind:          KindMicroblogLike,
			ItemID:        post.Slug,
			Platform:      pub.Platform,
			TargetName:    pub.TargetName,
			PublicationID: pub.ID,
		})
		pairs = append(pairs, Pair{
			Kind:          KindMicroblogReply,
			ItemID:        post.Slug,
			Platform:      pub.Platform,
			TargetName:    pub.TargetName,
			PublicationID: pub.ID,
		})
	}

	log.Printf("microblog refresh: post %d starting (%d pair(s))", postID, len(pairs))

	ok := 0
	var firstErr error
	for i, p := range pairs {
		if i > 0 {
			sleepBetween(p.Platform)
		}
		if err := refreshOne(p); err != nil {
			log.Printf("microblog refresh: %s/%s/%s/%s failed: %v",
				p.Kind, p.Platform, p.TargetName, p.ItemID, err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		log.Printf("microblog refresh: %s/%s/%s/%s ok",
			p.Kind, p.Platform, p.TargetName, p.ItemID)
		ok++
	}
	log.Printf("microblog refresh: post %d done (%d/%d succeeded)", postID, ok, len(pairs))
	return firstErr
}

func loadMicroblogPair(p Pair) (*models.MicroblogPublication, *config.Target, error) {
	var pub models.MicroblogPublication
	if err := database.Db.First(&pub, p.PublicationID).Error; err != nil {
		return nil, nil, fmt.Errorf("microblog publication %d: %w", p.PublicationID, err)
	}
	if !pub.Success {
		return nil, nil, fmt.Errorf("microblog publication %d not yet successful", p.PublicationID)
	}

	// Resolve the target up-front so the Bluesky backfill path can log in.
	var target config.Target
	var found bool
	for _, t := range config.Data.Targets {
		if t.Name == pub.TargetName {
			target, found = t, true
			break
		}
	}
	if !found {
		return nil, nil, fmt.Errorf("microblog target %q gone from config", pub.TargetName)
	}

	// Self-heal: derive external_id from post_url when missing. This recovers
	// publication rows created before the column-collision fix; once written
	// back to the row, subsequent refreshes are normal.
	if (pub.ExternalID == nil || *pub.ExternalID == "") && pub.PostUrl != nil && *pub.PostUrl != "" {
		if id := deriveExternalID(pub.Platform, *pub.PostUrl, target); id != "" {
			pub.ExternalID = &id
			if err := database.Db.Save(&pub).Error; err != nil {
				log.Printf("microblog: backfill external_id for pub %d save failed: %v", pub.ID, err)
			} else {
				log.Printf("microblog: backfilled external_id for pub %d (%s/%s) -> %s",
					pub.ID, pub.Platform, pub.TargetName, id)
			}
		}
	}

	if pub.ExternalID == nil || *pub.ExternalID == "" {
		return nil, nil, fmt.Errorf("microblog publication %d has no remote id stored", p.PublicationID)
	}
	return &pub, &target, nil
}

// deriveExternalID reconstructs the platform-specific external identifier
// from the already-persisted post URL (and, for Bluesky, a fresh session DID).
// Returns "" when the URL is unparseable or the platform's recovery isn't
// supported.
func deriveExternalID(platform, postURL string, target config.Target) string {
	switch platform {
	case "mastodon":
		// https://<instance>/@<user>/<status_id> — last segment is the id.
		if idx := strings.LastIndex(postURL, "/"); idx >= 0 && idx < len(postURL)-1 {
			return postURL[idx+1:]
		}
	case "bluesky":
		// https://bsky.app/profile/<handle>/post/<rkey> — need DID to build AT URI.
		if idx := strings.LastIndex(postURL, "/"); idx >= 0 && idx < len(postURL)-1 {
			rkey := postURL[idx+1:]
			session, err := blueskyapi.Login(target.Username, target.PAT)
			if err != nil {
				log.Printf("microblog: bluesky login for external_id backfill failed: %v", err)
				return ""
			}
			return "at://" + session.Did + "/app.bsky.feed.post/" + rkey
		}
	}
	return ""
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
