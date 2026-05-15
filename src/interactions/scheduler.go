package interactions

import (
	"fmt"
	"log"
	"math"
	"math/rand"
	"sort"
	"sync"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/config"
	"github.com/LNA-DEV/HomePageCompanion/database"
	"github.com/LNA-DEV/HomePageCompanion/models"
)

// Pacing constants. Tuned so that even a large catalog distributes work over
// hours rather than seconds, and never burst-floods a platform.
const (
	TargetCycleSeconds    = 6 * 3600
	TickIntervalSeconds   = 60
	MinPerTick            = 1
	MaxPerTick            = 10
	InterRequestBaseDelay = 500 * time.Millisecond
	InterRequestJitter    = 500 * time.Millisecond
	MinSamePlatformGap    = 1 * time.Second
)

// runMu serialises ticks and manual sweeps so two refresh loops never run
// concurrently and overwhelm a platform.
var runMu sync.Mutex

// lastPlatformCall tracks the in-process timestamp of the most recent
// outbound call per platform. Used by sleepBetween to enforce
// MinSamePlatformGap. Resets after restart, which is fine — gaps are still
// honoured within a single process lifetime.
var (
	lastPlatformCallMu sync.Mutex
	lastPlatformCall   = make(map[string]time.Time)
)

// PairKind identifies the type of work a Pair represents. The scheduler
// dispatches on this in refreshOne so the same pacing logic covers both
// RSS-driven autouploads and the microblog feature.
type PairKind string

const (
	KindRSSLike       PairKind = "rss-like"
	KindMicroblogLike PairKind = "mblog-like"
	KindMicroblogReply PairKind = "mblog-comment"
)

// Pair is one unit of fetching work.
//
// For KindRSSLike: refresh likes for ItemID (= AutoUploadItem.ItemID) on
// (Platform, TargetName).
//
// For KindMicroblogLike / KindMicroblogReply: ItemID is the local
// MicroblogPost.Slug; PublicationID points at the federated row so the
// handler can use the remote PostId without re-querying.
type Pair struct {
	Kind          PairKind
	ItemID        string
	Platform      string
	TargetName    string
	LastUpdated   time.Time // zero = never fetched, prioritised first
	PublicationID uint      // set for microblog kinds; 0 for RSS
}

// RunTick is the cron entry point. It refreshes the K most-stale pairs, where
// K is sized so that with the current tick interval we'd cover the whole
// catalog within TargetCycleSeconds. If another refresh is already running
// (manual sweep or a previous slow tick) this is a no-op.
func RunTick() {
	if !runMu.TryLock() {
		log.Print("interactions tick: busy, skipping")
		return
	}
	defer runMu.Unlock()

	pairs, err := buildPairs()
	if err != nil {
		log.Printf("interactions tick: buildPairs error: %v", err)
		return
	}
	if len(pairs) == 0 {
		return
	}

	k := perTickCount(len(pairs))
	if k > len(pairs) {
		k = len(pairs)
	}

	refreshed := 0
	for i, p := range pairs[:k] {
		if i > 0 {
			sleepBetween(p.Platform)
		}
		if err := refreshOne(p); err != nil {
			log.Printf("interactions tick: %s/%s/%s failed: %v", p.Platform, p.TargetName, p.ItemID, err)
			continue
		}
		refreshed++
	}
	log.Printf("interactions tick: refreshed %d/%d stale pair(s) (catalog=%d)", refreshed, k, len(pairs))
}

// FetchAllThrottled walks the entire pair list with the same pacing as a tick
// but with no per-tick cap. Used by the manual /admin/interactions/fetch
// trigger; the HTTP handler launches it in a goroutine and returns 202.
func FetchAllThrottled() {
	if !runMu.TryLock() {
		log.Print("interactions: full sweep requested but another refresh is busy; skipping")
		return
	}
	defer runMu.Unlock()

	pairs, err := buildPairs()
	if err != nil {
		log.Printf("interactions: full sweep buildPairs error: %v", err)
		return
	}
	if len(pairs) == 0 {
		log.Print("interactions: full sweep — nothing to do")
		return
	}
	log.Printf("interactions: full throttled sweep started over %d pair(s)", len(pairs))

	ok := 0
	for i, p := range pairs {
		if i > 0 {
			sleepBetween(p.Platform)
		}
		if err := refreshOne(p); err != nil {
			log.Printf("interactions sweep: %s/%s/%s failed: %v", p.Platform, p.TargetName, p.ItemID, err)
			continue
		}
		ok++
	}
	log.Printf("interactions: full throttled sweep done (%d/%d succeeded)", ok, len(pairs))
}

// perTickCount returns how many pairs to refresh in a single tick so that the
// whole catalog rotates through within TargetCycleSeconds.
func perTickCount(total int) int {
	if total <= 0 {
		return 0
	}
	desired := math.Ceil(float64(total) * float64(TickIntervalSeconds) / float64(TargetCycleSeconds))
	k := int(desired)
	if k < MinPerTick {
		k = MinPerTick
	}
	if k > MaxPerTick {
		k = MaxPerTick
	}
	return k
}

// buildPairs collects every (item, platform, target) tuple we know about and
// looks up the existing Interaction row to find the last-update timestamp.
// The returned slice is sorted by LastUpdated ASC so the stalest entries are
// at the front.
func buildPairs() ([]Pair, error) {
	var items []models.AutoUploadItem
	if err := database.Db.Find(&items).Error; err != nil {
		return nil, err
	}

	pairs := make([]Pair, 0, len(items))
	for _, item := range items {
		var target config.Target
		for _, t := range config.Data.Targets {
			if t.Platform == item.Platform {
				target = t
				break
			}
		}
		if target.Name == "" {
			continue
		}

		p := Pair{
			Kind:       KindRSSLike,
			ItemID:     item.ItemID,
			Platform:   item.Platform,
			TargetName: target.Name,
		}

		var existing models.Interaction
		err := database.Db.
			Where("source = ? AND item_id = ? AND platform = ? AND target_name = ?", "rss", item.ItemID, item.Platform, target.Name).
			First(&existing).Error
		if err == nil {
			p.LastUpdated = existing.UpdatedAt
		}
		// On error (including "record not found"), leave LastUpdated as zero
		// so this pair sorts to the front.

		pairs = append(pairs, p)
	}

	// Microblog: one pair per (publication × likes) and one per
	// (publication × comments). Each kind tracks its own freshness so likes
	// and replies refresh on independent staleness.
	var mpubs []models.MicroblogPublication
	database.Db.Where("success = ?", true).Find(&mpubs)
	for _, pub := range mpubs {
		var post models.MicroblogPost
		if err := database.Db.First(&post, pub.PostID).Error; err != nil {
			continue
		}
		// Likes pair.
		likePair := Pair{
			Kind:          KindMicroblogLike,
			ItemID:        post.Slug,
			Platform:      pub.Platform,
			TargetName:    pub.TargetName,
			PublicationID: pub.ID,
		}
		var existing models.Interaction
		if err := database.Db.
			Where("source = ? AND item_id = ? AND platform = ? AND target_name = ?", "microblog", post.Slug, pub.Platform, pub.TargetName).
			First(&existing).Error; err == nil {
			likePair.LastUpdated = existing.UpdatedAt
		}
		pairs = append(pairs, likePair)

		// Comments pair (separate cadence).
		commentPair := Pair{
			Kind:          KindMicroblogReply,
			ItemID:        post.Slug,
			Platform:      pub.Platform,
			TargetName:    pub.TargetName,
			PublicationID: pub.ID,
		}
		if pub.CommentsRefreshedAt != nil {
			commentPair.LastUpdated = *pub.CommentsRefreshedAt
		}
		pairs = append(pairs, commentPair)
	}

	sort.Slice(pairs, func(i, j int) bool {
		return pairs[i].LastUpdated.Before(pairs[j].LastUpdated)
	})
	return pairs, nil
}

// refreshOne performs the actual fetch+upsert for a single Pair. Dispatches
// on the Pair's Kind: RSS-driven likes use the existing RSS-platform helpers;
// microblog kinds use the mastodonapi package directly via dedicated
// helpers in microblog_likes.go.
func refreshOne(p Pair) error {
	switch p.Kind {
	case "", KindRSSLike:
		return refreshRSSLike(p)
	case KindMicroblogLike:
		return refreshMicroblogLike(p)
	case KindMicroblogReply:
		return refreshMicroblogComments(p)
	default:
		return nil
	}
}

func refreshRSSLike(p Pair) error {
	// Load the actual AutoUploadItem row so the platform helpers see the
	// stored PostUrl / PostId / VersionId. Earlier this function passed a
	// stub item with only ItemID + Platform set, which made every handler
	// short-circuit with "missing PostID" / "post URL or version ID is nil"
	// even for rows whose ids were correctly persisted.
	var item models.AutoUploadItem
	if err := database.Db.
		Where("item_id = ? AND platform = ?", p.ItemID, p.Platform).
		First(&item).Error; err != nil {
		return fmt.Errorf("rss like: load item %s/%s: %w", p.Platform, p.ItemID, err)
	}

	cfg := DefaultRetryConfig()

	var likeCount int
	var fetchErr error

	switch p.Platform {
	case "bluesky":
		result, e := RetryWithBackoff(cfg, func() (*BlueskyLikesResponse, error) {
			return handleBlueskyLikes(item, p.TargetName)
		})
		if e != nil {
			fetchErr = e
		} else {
			likeCount = len(result.Likes)
		}

	case "pixelfed":
		result, e := RetryWithBackoff(cfg, func() (*PixelfedLikesResponse, error) {
			return handlePixelfedLikes(item, p.TargetName)
		})
		if e != nil {
			fetchErr = e
		} else {
			likeCount = len(result.Accounts)
		}

	case "mastodon":
		result, e := RetryWithBackoff(cfg, func() (*MastodonLikesResponse, error) {
			return handleMastodonLikes(item, p.TargetName)
		})
		if e != nil {
			fetchErr = e
		} else {
			likeCount = len(result.Accounts)
		}

	case "instagram":
		result, e := RetryWithBackoff(cfg, func() (*InstagramLikesResponse, error) {
			return handleInstagramLikes(item, p.TargetName)
		})
		if e != nil {
			fetchErr = e
		} else {
			likeCount = result.LikeCount
		}

	default:
		return nil
	}

	if fetchErr != nil {
		return fetchErr
	}

	return upsertInteraction("rss", p.ItemID, p.Platform, p.TargetName, likeCount)
}

// upsertInteraction writes or updates the (source, item, platform, target)
// Interaction row's like count. Source is "rss" for autouploads and
// "microblog" for federated microblog posts.
func upsertInteraction(source, itemID, platform, targetName string, likeCount int) error {
	var interaction models.Interaction
	result := database.Db.
		Where("source = ? AND item_id = ? AND platform = ? AND target_name = ?", source, itemID, platform, targetName).
		First(&interaction)

	if result.Error != nil {
		interaction = models.Interaction{
			Source:     source,
			ItemID:     itemID,
			Platform:   platform,
			TargetName: targetName,
			LikeCount:  likeCount,
		}
		if err := database.Db.Create(&interaction).Error; err != nil {
			return err
		}
	} else {
		interaction.LikeCount = likeCount
		if err := database.Db.Save(&interaction).Error; err != nil {
			return err
		}
	}
	return nil
}

// sleepBetween blocks for InterRequestBaseDelay + random(0,InterRequestJitter)
// AND additionally extends the sleep until the per-platform gap is honoured.
// Updates the in-process last-call timestamp for the platform.
func sleepBetween(platform string) {
	jitter := time.Duration(rand.Int63n(int64(InterRequestJitter)))
	wait := InterRequestBaseDelay + jitter

	lastPlatformCallMu.Lock()
	if last, ok := lastPlatformCall[platform]; ok {
		needed := MinSamePlatformGap - time.Since(last)
		if needed > wait {
			wait = needed
		}
	}
	lastPlatformCallMu.Unlock()

	if wait > 0 {
		time.Sleep(wait)
	}

	lastPlatformCallMu.Lock()
	lastPlatformCall[platform] = time.Now()
	lastPlatformCallMu.Unlock()
}
