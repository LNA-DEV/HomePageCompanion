package autouploader

import (
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/database"
	"github.com/LNA-DEV/HomePageCompanion/models"
)

const maxErrorMessageLen = 2048

// statusInErr extracts the first 3-digit HTTP status appearing in an error
// message. Returns 0 when none is found. Useful because the platform
// publishers compose errors like "failed to publish post, status: 401, body: ..."
var statusInErr = regexp.MustCompile(`status[:= ]\s*(\d{3})`)

// RecordAttempt persists a single upload attempt. err == nil means success.
// httpStatus is best-effort: if zero, RecordAttempt tries to parse a status
// out of the error message. Existing callers use Source = "autouploader"
// implicitly via the model's column default.
func RecordAttempt(connectionName, itemID, platform, targetName string, err error, httpStatus int) {
	RecordAttemptWithSource("autouploader", connectionName, itemID, platform, targetName, err, httpStatus)
}

// RecordAttemptWithSource is the explicit variant used by callers outside
// the RSS autouploader (e.g. the microblog package) so the /uploads page
// can distinguish where the attempt originated.
func RecordAttemptWithSource(source, connectionName, itemID, platform, targetName string, err error, httpStatus int) {
	att := models.UploadAttempt{
		Source:         source,
		ConnectionName: connectionName,
		ItemID:         itemID,
		Platform:       platform,
		TargetName:     targetName,
		Success:        err == nil,
		HTTPStatus:     httpStatus,
		CreatedAt:      time.Now(),
	}
	if err != nil {
		msg := err.Error()
		if att.HTTPStatus == 0 {
			if m := statusInErr.FindStringSubmatch(msg); len(m) == 2 {
				if s := parseStatus(m[1]); s > 0 {
					att.HTTPStatus = s
				}
			}
		}
		att.ErrorCode = classifyError(att.HTTPStatus, msg)
		if len(msg) > maxErrorMessageLen {
			msg = msg[:maxErrorMessageLen] + "…"
		}
		att.ErrorMessage = msg
	}
	if database.Db != nil {
		database.Db.Create(&att)
	}
}

func parseStatus(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func classifyError(status int, msg string) string {
	lower := strings.ToLower(msg)

	// Message-based detection runs first because some servers — notably
	// Pixelfed (Laravel default) — return 500 for genuine auth failures
	// with bodies like {"error":"Unauthenticated."}. Trusting the status
	// alone would misclassify those as `server`.
	switch {
	case strings.Contains(lower, "unauth"), // unauthorized | unauthenticated
		strings.Contains(lower, "invalid token"),
		strings.Contains(lower, "expired"),
		strings.Contains(lower, "forbidden"),
		strings.Contains(lower, "authentication"):
		return "auth"
	case strings.Contains(lower, "rate limit"), strings.Contains(lower, "too many"):
		return "rate_limited"
	case strings.Contains(lower, "no such host"), strings.Contains(lower, "timeout"),
		strings.Contains(lower, "connection refused"), strings.Contains(lower, "i/o timeout"):
		return "network"
	}

	// Fall back to HTTP status codes.
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return "auth"
	case http.StatusTooManyRequests:
		return "rate_limited"
	}
	if status >= 500 && status < 600 {
		return "server"
	}
	if status >= 400 && status < 500 {
		return "client"
	}
	return "unknown"
}
