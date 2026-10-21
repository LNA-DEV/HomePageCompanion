// Package metatoken keeps an Instagram or Threads access token alive.
// Meta's long-lived tokens expire 60 days after they were issued or last
// refreshed, and an expired one cannot be refreshed any more. The operator
// puts the token into .env; a Keeper keeps the current one in the secret
// store, uses it in place of the configured one, and refreshes it once a
// week. A new token in .env replaces the stored one.
//
// Design and decisions: docs/concepts/meta-token-refresh.md.
package metatoken

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/companion"
)

// RefreshEvery is how old a token's last refresh may get before RunDue
// renews it. Meta's floor is 24 hours and a token lives 60 days, so a week
// leaves about 53 days of daily retries.
const RefreshEvery = 7 * 24 * time.Hour

// ExpiringWithin is how close to its expiry a token counts as expiring.
const ExpiringWithin = 14 * 24 * time.Hour

const maxErrorLen = 500

// Status values.
const (
	StatusOK       = "ok"
	StatusExpiring = "expiring"
	StatusExpired  = "expired"
	StatusUnknown  = "unknown" // not refreshed yet
)

// Row is a token's metadata; the token itself is in the secret store.
type Row struct {
	Instance string `gorm:"primaryKey"`
	SeedHash string
	// Account and AccountID are set for an account connected through
	// OAuth (Connect); empty for a token from the config.
	Account       string
	AccountID     string
	RefreshedAt   *time.Time
	ExpiresAt     *time.Time
	LastAttemptAt *time.Time
	LastError     string
	UpdatedAt     time.Time
}

func (Row) TableName() string { return "meta_tokens" }

// Refresher trades a token for a new one and its lifetime.
type Refresher func(ctx context.Context, token string) (string, time.Duration, error)

// Keeper keeps one instance's token.
type Keeper struct {
	ctx      companion.Context
	seed     string
	refresh  Refresher
	mu       sync.Mutex
	now      func() time.Time
	disabled error
}

// New prepares the keeper for an instance whose configured token is seed.
func New(ctx companion.Context, seed string, refresh Refresher) (*Keeper, error) {
	k := &Keeper{ctx: ctx, seed: seed, refresh: refresh, now: time.Now}
	if ctx.DB() == nil {
		return nil, errors.New("metatoken: no database")
	}
	if err := ctx.DB().AutoMigrate(&Row{}); err != nil {
		return nil, err
	}
	return k, nil
}

func (k *Keeper) secretName() string { return "token:" + k.ctx.InstanceName() }

func seedHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (k *Keeper) row() (Row, bool) {
	var r Row
	ok := k.ctx.DB().Where("instance = ?", k.ctx.InstanceName()).Limit(1).Find(&r).RowsAffected > 0
	return r, ok
}

// Token is the token to use now: the stored, refreshed or connected one,
// unless the configured token changed since it was stored, or the store is
// unavailable (then the configured one).
func (k *Keeper) Token() string {
	k.mu.Lock()
	defer k.mu.Unlock()
	r, ok := k.row()
	hash := seedHash(k.seed)
	if k.seed == "" && !(ok && r.SeedHash == hash) {
		return ""
	}
	if ok && r.SeedHash == hash {
		if tok, found, err := k.ctx.Secrets().Get(k.secretName()); err == nil && found {
			return tok
		} else if err != nil {
			k.ctx.Log().Printf("%s: stored token does not decrypt (key changed?), using the configured one: %v", k.ctx.InstanceName(), err)
		}
	} else if ok {
		k.ctx.Log().Printf("%s: new token in the config, replacing the stored one", k.ctx.InstanceName())
	}
	if err := k.store(k.seed, Row{Instance: k.ctx.InstanceName(), SeedHash: hash}); err != nil {
		k.ctx.Log().Printf("%s: storing the token failed, using the configured one: %v", k.ctx.InstanceName(), err)
	}
	return k.seed
}

func (k *Keeper) store(token string, r Row) error {
	if err := k.ctx.Secrets().Put(k.secretName(), token); err != nil {
		return err
	}
	return k.ctx.DB().Save(&r).Error
}

// Seed stores a token that is already further along than the configured
// one, e.g. a refreshed token moved over from an old installation.
func (k *Keeper) Seed(token string, refreshed, expires *time.Time) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.store(token, Row{Instance: k.ctx.InstanceName(), SeedHash: seedHash(k.seed), RefreshedAt: refreshed, ExpiresAt: expires})
}

// Connect stores the long-lived token of an account connected through
// OAuth. It is refreshed like a configured one, and like the stored one it
// gives way to a new token in the config.
func (k *Keeper) Connect(token string, expires time.Time, account, accountID string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	now := k.now()
	return k.store(token, Row{Instance: k.ctx.InstanceName(), SeedHash: seedHash(k.seed), RefreshedAt: &now, ExpiresAt: &expires,
		Account: account, AccountID: accountID})
}

// Disconnect forgets the stored token; the configured one applies again.
func (k *Keeper) Disconnect() error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if err := k.ctx.Secrets().Delete(k.secretName()); err != nil {
		return err
	}
	return k.ctx.DB().Where("instance = ?", k.ctx.InstanceName()).Delete(&Row{}).Error
}

// Connected is the connected account, if the token in use is one.
func (k *Keeper) Connected() (account, accountID string, ok bool) {
	k.Token() // a new configured token replaces the connection first
	r, found := k.row()
	if !found || r.Account == "" {
		return "", "", false
	}
	return r.Account, r.AccountID, true
}

// RunDue refreshes the token when its last refresh is RefreshEvery old or
// it was never refreshed (Meta refuses one younger than 24 hours; the next
// run tries again). A failure keeps the old token.
func (k *Keeper) RunDue(ctx context.Context) error {
	k.Token()
	r, ok := k.row()
	if !ok {
		return nil
	}
	if r.RefreshedAt != nil && k.now().Sub(*r.RefreshedAt) < RefreshEvery {
		return nil
	}
	return k.RefreshNow(ctx)
}

// RefreshNow refreshes regardless of age.
func (k *Keeper) RefreshNow(ctx context.Context) error {
	token := k.Token()
	if token == "" {
		return errors.New("no token configured")
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	r, _ := k.row()
	now := k.now()
	newToken, ttl, err := k.refresh(ctx, token)
	if err != nil {
		msg := err.Error()
		if len(msg) > maxErrorLen {
			msg = msg[:maxErrorLen] + "…"
		}
		r.LastAttemptAt, r.LastError = &now, msg
		k.ctx.DB().Save(&r)
		k.ctx.Log().Printf("%s: token refresh failed: %s", k.ctx.InstanceName(), msg)
		return fmt.Errorf("refresh: %s", msg)
	}
	expires := now.Add(ttl)
	r.RefreshedAt, r.ExpiresAt, r.LastAttemptAt, r.LastError = &now, &expires, &now, ""
	if err := k.store(newToken, r); err != nil {
		return fmt.Errorf("storing the refreshed token, keeping the old one: %w", err)
	}
	k.ctx.Log().Printf("%s: token refreshed, expires %s", k.ctx.InstanceName(), expires.Format(time.DateOnly))
	return nil
}

// Status describes the token for the UI; never the token itself.
type Status struct {
	Instance      string     `json:"instance"`
	Status        string     `json:"status"`
	Source        string     `json:"source,omitempty"` // config | refreshed | connected
	Account       string     `json:"account,omitempty"`
	AccountID     string     `json:"accountId,omitempty"`
	RefreshedAt   *time.Time `json:"refreshedAt,omitempty"`
	ExpiresAt     *time.Time `json:"expiresAt,omitempty"`
	DaysLeft      *int       `json:"daysLeft,omitempty"`
	LastAttemptAt *time.Time `json:"lastAttemptAt,omitempty"`
	LastError     string     `json:"lastError,omitempty"`
}

// Status reports the token's state.
func (k *Keeper) Status() Status {
	s := Status{Instance: k.ctx.InstanceName(), Status: StatusUnknown}
	r, ok := k.row()
	if !ok {
		return s
	}
	now := k.now()
	s.Source = "config"
	if r.RefreshedAt != nil {
		s.Source = "refreshed"
	}
	if r.Account != "" {
		s.Source, s.Account, s.AccountID = "connected", r.Account, r.AccountID
	}
	s.RefreshedAt, s.ExpiresAt, s.LastAttemptAt, s.LastError = r.RefreshedAt, r.ExpiresAt, r.LastAttemptAt, r.LastError
	if r.ExpiresAt != nil {
		days := int(math.Floor(r.ExpiresAt.Sub(now).Hours() / 24))
		s.DaysLeft = &days
		k.ctx.Metrics().Set("token_days_left", float64(days), "instance", k.ctx.InstanceName())
	}
	switch {
	case r.ExpiresAt != nil && !now.Before(*r.ExpiresAt), LooksExpired(r.LastError):
		s.Status = StatusExpired
	case r.ExpiresAt == nil:
		s.Status = StatusUnknown
	case r.ExpiresAt.Sub(now) < ExpiringWithin:
		s.Status = StatusExpiring
	default:
		s.Status = StatusOK
	}
	return s
}

// LooksExpired matches Meta's answer for a dead token: OAuthException code
// 190 (not 1900 or 19000), or the word "expired".
func LooksExpired(msg string) bool {
	return strings.Contains(strings.ToLower(msg), "expired") || code190.MatchString(msg)
}

var code190 = regexp.MustCompile(`"code"\s*:\s*190\b`)

// SetClock replaces the clock; for tests.
func (k *Keeper) SetClock(now func() time.Time) { k.now = now }

// UnsealLegacy decrypts a token the pre-module companion stored in
// platform_tokens with TOKEN_ENCRYPTION_KEY: base64(nonce ‖ AES-256-GCM),
// the target name as additional data.
func UnsealLegacy(keyB64, target, stored string) (string, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(keyB64))
	if err != nil {
		return "", fmt.Errorf("TOKEN_ENCRYPTION_KEY is not base64: %w", err)
	}
	if len(key) != 32 {
		return "", fmt.Errorf("TOKEN_ENCRYPTION_KEY must be 32 bytes, has %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	raw, err := base64.StdEncoding.DecodeString(stored)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("stored token too short")
	}
	plain, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], []byte(target))
	if err != nil {
		return "", errors.New("stored token does not decrypt with TOKEN_ENCRYPTION_KEY")
	}
	return string(plain), nil
}

// LegacyRow is a row of the old platform_tokens table.
type LegacyRow struct {
	TargetName  string
	AccessToken string
	SeedHash    string
	RefreshedAt *time.Time
	ExpiresAt   *time.Time
}

// SealLegacy is the old encryption, for tests of the migration (with a
// fixed nonce, which is fine only there).
func SealLegacy(keyB64, target, token string) (string, error) {
	key, _ := base64.StdEncoding.DecodeString(keyB64)
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	out := gcm.Seal(nonce, nonce, []byte(token), []byte(target))
	return base64.StdEncoding.EncodeToString(out), nil
}

// ImportLegacy moves the refreshed tokens of one platform from the old
// platform_tokens table (encrypted with TOKEN_ENCRYPTION_KEY) into the
// instances' keepers. A token that was never refreshed is the .env one and
// needs nothing.
func ImportLegacy(mc companion.ModuleContext, l *companion.Legacy, platform string, keeper func(companion.InstanceRef) *Keeper) error {
	if !l.HasTable("platform_tokens") {
		return nil
	}
	var rows []LegacyRow
	if err := l.DB.Raw("SELECT target_name, access_token, seed_hash, refreshed_at, expires_at FROM platform_tokens WHERE platform = ?", platform).Scan(&rows).Error; err != nil {
		return err
	}
	key := os.Getenv("TOKEN_ENCRYPTION_KEY")
	for _, r := range rows {
		if r.RefreshedAt == nil {
			l.Reportf("%s: %s: token was never refreshed, the one in .env is used", platform, r.TargetName)
			continue
		}
		var target *companion.InstanceRef
		for _, in := range mc.Instances() {
			if in.Name == companion.LegacyName(r.TargetName) {
				in := in
				target = &in
			}
		}
		if target == nil {
			l.Reportf("%s: %s: no instance of that name, refreshed token left behind", platform, r.TargetName)
			continue
		}
		if key == "" {
			l.Reportf("%s: %s: TOKEN_ENCRYPTION_KEY not set, refreshed token left behind (the one in .env is used)", platform, r.TargetName)
			continue
		}
		tok, err := UnsealLegacy(key, r.TargetName, r.AccessToken)
		if err != nil {
			l.Reportf("%s: %s: %v, the one in .env is used", platform, r.TargetName, err)
			continue
		}
		l.Reportf("%s: %s: refreshed token moved into the secret store", platform, r.TargetName)
		if l.Check {
			continue
		}
		if err := keeper(*target).Seed(tok, r.RefreshedAt, r.ExpiresAt); err != nil {
			return err
		}
	}
	return nil
}
