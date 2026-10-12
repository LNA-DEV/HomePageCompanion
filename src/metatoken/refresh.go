package metatoken

import (
	"log"
	"sync"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/config"
	"github.com/LNA-DEV/HomePageCompanion/database"
	"github.com/LNA-DEV/HomePageCompanion/instagramapi"
	"github.com/LNA-DEV/HomePageCompanion/models"
	"github.com/LNA-DEV/HomePageCompanion/threadsapi"
)

// RefreshEvery is how old a token's last refresh may get before RunDue
// renews it (D2). Meta's floor is 24 hours and a token lives 60 days, so a
// week leaves about 53 days of daily retries.
const RefreshEvery = 7 * 24 * time.Hour

const maxErrorLen = 500

// refreshers call Meta's refresh endpoint, per platform.
var refreshers = map[string]func(token string) (string, time.Duration, error){
	"instagram": func(token string) (string, time.Duration, error) {
		r, err := instagramapi.RefreshToken(token)
		return r.AccessToken, time.Duration(r.ExpiresIn) * time.Second, err
	},
	"threads": func(token string) (string, time.Duration, error) {
		r, err := threadsapi.RefreshToken(token)
		return r.AccessToken, time.Duration(r.ExpiresIn) * time.Second, err
	},
}

// jobMu serialises the daily run and the admin's "refresh now".
var jobMu sync.Mutex

// RunDue refreshes every token whose last refresh is RefreshEvery old, or
// that was never refreshed (a seed of unknown age; Meta refuses one younger
// than 24 hours, and the next run tries again). A failure keeps the old
// token. Cron entry point; does nothing while refresh is disabled.
func RunDue() {
	refresh("", func(row models.PlatformToken, now time.Time) bool {
		return row.RefreshedAt == nil || now.Sub(*row.RefreshedAt) >= RefreshEvery
	})
}

// RefreshNow refreshes target's token regardless of its age. It reports
// false when target is not a configured Instagram/Threads target with a
// stored token.
func RefreshNow(target string) bool {
	return refresh(target, func(models.PlatformToken, time.Time) bool { return true })
}

// refresh runs over the stored tokens of configured targets (only one, when
// only is set) and refreshes those due says. It reloads the config when a
// token changed, which puts the new one in place through Apply.
func refresh(only string, due func(models.PlatformToken, time.Time) bool) (found bool) {
	if sealer == nil || database.Db == nil {
		return false
	}

	changed := func() bool {
		jobMu.Lock()
		defer jobMu.Unlock()

		configured := map[string]bool{}
		for _, t := range config.Data.Targets {
			if refreshable(t.Platform) && t.AccessToken != "" {
				configured[t.Name] = true
			}
		}

		var rows []models.PlatformToken
		if err := database.Db.Order("target_name").Find(&rows).Error; err != nil {
			log.Printf("metatoken: listing tokens failed: %v", err)
			return false
		}

		changed := false
		now := time.Now()
		for _, row := range rows {
			if !configured[row.TargetName] || (only != "" && row.TargetName != only) {
				continue
			}
			found = true
			if due(row, now) && refreshOne(row, now) {
				changed = true
			}
		}
		return changed
	}()

	if changed {
		reloadConfig()
	}
	return found
}

// reloadConfig is config.LoadConfig, which panics on a broken config.yaml,
// made safe for callers outside cron's recovery (the startup timer, the
// admin handler). The new token is stored either way and takes effect on
// the next reload that works.
func reloadConfig() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("metatoken: config reload after refresh failed: %v", r)
		}
	}()
	config.LoadConfig()
}

// refreshOne refreshes one row and reports whether it now holds a new token.
// The new token is stored before anything uses it.
func refreshOne(row models.PlatformToken, now time.Time) bool {
	token, err := unseal(row.TargetName, row.AccessToken)
	if err != nil {
		// Apply re-seeds it from .env on the next config load.
		log.Printf("metatoken: %s: stored token does not decrypt, skipping", row.TargetName)
		return false
	}

	newToken, ttl, err := refreshers[row.Platform](token)
	if err != nil {
		msg := err.Error()
		if len(msg) > maxErrorLen {
			msg = msg[:maxErrorLen] + "…"
		}
		log.Printf("metatoken: %s: refresh failed: %s", row.TargetName, msg)
		if _, uerr := update(row, map[string]any{"last_attempt_at": now, "last_error": msg}); uerr != nil {
			log.Printf("metatoken: %s: recording the failure failed: %v", row.TargetName, uerr)
		}
		return false
	}

	sealed, err := seal(row.TargetName, newToken)
	if err != nil {
		log.Printf("metatoken: %s: encrypting the refreshed token failed, keeping the old one: %v", row.TargetName, err)
		return false
	}
	expires := now.Add(ttl)
	n, err := update(row, map[string]any{
		"access_token":    sealed,
		"refreshed_at":    now,
		"expires_at":      expires,
		"last_attempt_at": now,
		"last_error":      "",
	})
	switch {
	case err != nil:
		log.Printf("metatoken: %s: storing the refreshed token failed, keeping the old one: %v", row.TargetName, err)
		return false
	case n == 0:
		log.Printf("metatoken: %s: re-seeded from .env meanwhile, dropping the refreshed token", row.TargetName)
		return false
	}
	log.Printf("metatoken: %s: token refreshed, expires %s", row.TargetName, expires.Format(time.DateOnly))
	return true
}

// update writes to row only while it still descends from the same .env
// token, so a refresh racing a re-seed never overwrites the new seed.
func update(row models.PlatformToken, fields map[string]any) (int64, error) {
	res := database.Db.Model(&models.PlatformToken{}).
		Where("id = ? AND seed_hash = ?", row.ID, row.SeedHash).
		Updates(fields)
	return res.RowsAffected, res.Error
}
