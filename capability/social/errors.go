package social

import (
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// Failure causes, as the Problems page groups them.
const (
	CauseAccount   = "account"   // token expired, no permission
	CauseContent   = "content"   // image too large, format, rejected text
	CauseTransient = "transient" // network, 5xx, rate limit: retried automatically
)

// StatusError carries the HTTP status of a refused call.
type StatusError struct {
	Status int
	Err    error
}

func (e *StatusError) Error() string { return e.Err.Error() }
func (e *StatusError) Unwrap() error { return e.Err }

// statusInErr extracts the first 3-digit HTTP status in an error message;
// the network clients compose errors like "… status: 401, body: …".
var statusInErr = regexp.MustCompile(`status[:= ]\s*(\d{3})`)

// Failure is a classified error.
type Failure struct {
	Code       string // auth | rate_limited | network | server | client | content | unknown
	Cause      string // account | content | transient
	HTTPStatus int
}

// Classify sorts an error from a publish or a stats call. The message is
// read first, because some servers (Pixelfed, Laravel's default) answer
// genuine auth failures with 500 and {"error":"Unauthenticated."}.
func Classify(err error) Failure {
	if err == nil {
		return Failure{}
	}
	status := 0
	var se *StatusError
	if errors.As(err, &se) {
		status = se.Status
	}
	msg := err.Error()
	if status == 0 {
		if m := statusInErr.FindStringSubmatch(msg); len(m) == 2 {
			status, _ = strconv.Atoi(m[1])
		}
	}
	code := classifyCode(status, msg)
	if errors.Is(err, ErrRateLimited) {
		code = "rate_limited"
	}
	if errors.Is(err, ErrNeedsImage) {
		code = "content"
	}
	return Failure{Code: code, Cause: causeOf(code), HTTPStatus: status}
}

func classifyCode(status int, msg string) string {
	lower := strings.ToLower(msg)
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
		strings.Contains(lower, "connection refused"), strings.Contains(lower, "i/o timeout"),
		strings.Contains(lower, "connection reset"), strings.Contains(lower, "unexpected eof"):
		return "network"
	case strings.Contains(lower, "too large"), strings.Contains(lower, "unsupported"),
		strings.Contains(lower, "no image"), strings.Contains(lower, "needs an image"):
		return "content"
	}

	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return "auth"
	case http.StatusTooManyRequests:
		return "rate_limited"
	case http.StatusRequestEntityTooLarge, http.StatusUnsupportedMediaType, http.StatusUnprocessableEntity:
		return "content"
	}
	if status >= 500 && status < 600 {
		return "server"
	}
	if status >= 400 && status < 500 {
		return "client"
	}
	return "unknown"
}

func causeOf(code string) string {
	switch code {
	case "auth":
		return CauseAccount
	case "client", "content":
		return CauseContent
	default:
		return CauseTransient
	}
}
