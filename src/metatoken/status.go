package metatoken

import (
	"math"
	"strings"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/config"
	"github.com/LNA-DEV/HomePageCompanion/database"
	"github.com/LNA-DEV/HomePageCompanion/models"
)

const (
	StatusOK       = "ok"
	StatusExpiring = "expiring"
	StatusExpired  = "expired"
	StatusUnknown  = "unknown"  // no row yet, or not refreshed yet
	StatusDisabled = "disabled" // no valid TOKEN_ENCRYPTION_KEY
)

// ExpiringWithin is how close to its expiry a token counts as expiring. With
// RefreshEvery at 7 days a token only gets there after about 46 days of
// failed refreshes in a row, so it always means something is broken.
const ExpiringWithin = 14 * 24 * time.Hour

// Status describes one target's token for the admin UI. It never carries the
// token or any part of it.
type Status struct {
	Name           string     `json:"name"`
	Platform       string     `json:"platform"`
	Status         string     `json:"status"`
	Source         string     `json:"source,omitempty"` // config | refreshed
	RefreshedAt    *time.Time `json:"refreshedAt,omitempty"`
	ExpiresAt      *time.Time `json:"expiresAt,omitempty"`
	DaysLeft       *int       `json:"daysLeft,omitempty"`
	LastAttemptAt  *time.Time `json:"lastAttemptAt,omitempty"`
	LastError      string     `json:"lastError,omitempty"`
	DisabledReason string     `json:"disabledReason,omitempty"`
}

// Statuses reports every configured Instagram/Threads target, in config order.
func Statuses() []Status {
	now := time.Now()
	out := []Status{}
	for _, t := range config.Data.Targets {
		if refreshable(t.Platform) {
			out = append(out, statusFor(t, now))
		}
	}
	return out
}

// StatusOf reports one target, if it is a configured Instagram/Threads one.
func StatusOf(target string) (Status, bool) {
	for _, s := range Statuses() {
		if s.Name == target {
			return s, true
		}
	}
	return Status{}, false
}

func statusFor(t config.Target, now time.Time) Status {
	s := Status{Name: t.Name, Platform: t.Platform, Status: StatusUnknown}
	if sealer == nil {
		s.Status = StatusDisabled
		s.DisabledReason = disabledReason
		return s
	}

	var row models.PlatformToken
	if database.Db == nil || database.Db.Where("target_name = ?", t.Name).Limit(1).Find(&row).RowsAffected == 0 {
		return s
	}

	s.Source = "config"
	if row.RefreshedAt != nil {
		s.Source = "refreshed"
	}
	s.RefreshedAt = row.RefreshedAt
	s.ExpiresAt = row.ExpiresAt
	s.LastAttemptAt = row.LastAttemptAt
	s.LastError = row.LastError
	if row.ExpiresAt != nil {
		days := int(math.Floor(row.ExpiresAt.Sub(now).Hours() / 24))
		s.DaysLeft = &days
	}

	switch {
	case row.ExpiresAt != nil && !now.Before(*row.ExpiresAt), looksExpired(row.LastError):
		s.Status = StatusExpired
	case row.ExpiresAt == nil:
		s.Status = StatusUnknown
	case row.ExpiresAt.Sub(now) < ExpiringWithin:
		s.Status = StatusExpiring
	default:
		s.Status = StatusOK
	}
	return s
}

// looksExpired matches Meta's answer for a dead token: OAuthException code
// 190, or the word autouploader's classifyError also keys on.
func looksExpired(msg string) bool {
	lower := strings.ToLower(msg)
	return strings.Contains(lower, "expired") || strings.Contains(lower, `"code":190`)
}
