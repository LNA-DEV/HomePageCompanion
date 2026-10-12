// Package metatoken keeps the Instagram and Threads access tokens alive.
// Meta's long-lived tokens expire 60 days after they were issued or last
// refreshed, and an expired one cannot be refreshed any more. The companion
// reads them from .env; this package keeps the current one, encrypted, in
// platform_tokens, puts it in place of the .env token on every config load
// (Apply), and refreshes it once a week (RunDue).
//
// Design and decisions: docs/concepts/meta-token-refresh.md.
package metatoken

import (
	"crypto/sha256"
	"encoding/hex"
	"log"
	"os"
	"sync"

	"github.com/LNA-DEV/HomePageCompanion/config"
	"github.com/LNA-DEV/HomePageCompanion/database"
	"github.com/LNA-DEV/HomePageCompanion/models"
)

// applyMu keeps two config loads (startup and the 5-minute cron) from
// seeding the same row at once.
var applyMu sync.Mutex

// Init reads TOKEN_ENCRYPTION_KEY, which config.LoadConfig has put into the
// environment from .env. Without a valid key refresh stays off: nothing is
// stored, and the .env tokens are used as before.
func Init() {
	_ = setKey(os.Getenv(keyEnv))
	if sealer == nil {
		log.Printf("metatoken: token refresh disabled: %s", disabledReason)
	}
}

// Enabled reports whether tokens are stored and refreshed.
func Enabled() bool { return sealer != nil }

// refreshable reports whether a target's platform has its tokens kept here.
func refreshable(platform string) bool {
	_, ok := refreshers[platform]
	return ok
}

// Apply is config.AfterLoad. For every Instagram/Threads target with a token
// in .env it puts the stored token in its place (concept §5.2). A target
// whose .env token changed since its row was seeded, or whose row does not
// decrypt, is re-seeded from .env. On a database error the .env token stays.
func Apply(cfg *config.Config) {
	if sealer == nil || database.Db == nil {
		return
	}
	applyMu.Lock()
	defer applyMu.Unlock()

	for i := range cfg.Targets {
		t := &cfg.Targets[i]
		if !refreshable(t.Platform) || t.AccessToken == "" {
			continue
		}
		token, err := current(t.Name, t.Platform, t.AccessToken)
		if err != nil {
			log.Printf("metatoken: %s: keeping the .env token: %v", t.Name, err)
			continue
		}
		t.AccessToken = token
	}
}

// current returns the token target should use, seeding its row when needed.
func current(target, platform, envToken string) (string, error) {
	hash := seedHash(envToken)

	var row models.PlatformToken
	res := database.Db.Where("target_name = ?", target).Limit(1).Find(&row)
	if res.Error != nil {
		return "", res.Error
	}
	if res.RowsAffected == 0 {
		return envToken, seed(&row, target, platform, envToken, hash)
	}
	if row.SeedHash != hash {
		log.Printf("metatoken: %s: new token in .env, replacing the stored one", target)
		return envToken, seed(&row, target, platform, envToken, hash)
	}
	token, err := unseal(target, row.AccessToken)
	if err != nil {
		log.Printf("metatoken: %s: stored token does not decrypt (key changed?), re-seeding from .env", target)
		return envToken, seed(&row, target, platform, envToken, hash)
	}
	return token, nil
}

// seed (re)writes row to hold the .env token, as if it had never been
// refreshed: its age is unknown, so the next RunDue tries straight away.
func seed(row *models.PlatformToken, target, platform, token, hash string) error {
	sealed, err := seal(target, token)
	if err != nil {
		return err
	}
	row.TargetName = target
	row.Platform = platform
	row.AccessToken = sealed
	row.SeedHash = hash
	row.RefreshedAt = nil
	row.ExpiresAt = nil
	row.LastAttemptAt = nil
	row.LastError = ""
	return database.Db.Save(row).Error
}

// seedHash identifies the .env token a row descends from. The token is
// random, so its hash does not reveal it.
func seedHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
