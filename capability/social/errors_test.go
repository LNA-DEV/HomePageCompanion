package social

import (
	"errors"
	"fmt"
	"testing"
)

// The classifier keeps the Pixelfed case: HTTP 500 with the body
// {"error":"Unauthenticated."} is an account problem, not an outage.
func TestClassify(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		code   string
		cause  string
		status int
	}{
		{"pixelfed 500 unauthenticated", errors.New(`pixelfed upload failed, status: 500 body: {"error":"Unauthenticated."}`), "auth", CauseAccount, 500},
		{"plain 401", &StatusError{401, errors.New("nope")}, "auth", CauseAccount, 401},
		{"plain 403", errors.New("status: 403 forbidden"), "auth", CauseAccount, 403},
		{"plain 429", &StatusError{429, errors.New("slow down")}, "rate_limited", CauseTransient, 429},
		{"rate limit sentinel", fmt.Errorf("mastodon: %w", ErrRateLimited), "rate_limited", CauseTransient, 0},
		{"not connected", fmt.Errorf("pixelfed main: %w", ErrNotConnected), "auth", CauseAccount, 0},
		{"500 outage", errors.New("status: 500 internal server error"), "server", CauseTransient, 500},
		{"502", &StatusError{502, errors.New("bad gateway")}, "server", CauseTransient, 502},
		{"no such host", errors.New("dial tcp: lookup bsky.social: no such host"), "network", CauseTransient, 0},
		{"timeout", errors.New("context deadline exceeded (i/o timeout)"), "network", CauseTransient, 0},
		{"expired in body", errors.New("token has expired"), "auth", CauseAccount, 0},
		{"rate limit phrase", errors.New("we got a rate limit reply"), "rate_limited", CauseTransient, 0},
		{"image too large", errors.New("status: 400 file too large"), "content", CauseContent, 400},
		{"needs an image", fmt.Errorf("instagram: %w", ErrNeedsImage), "content", CauseContent, 0},
		{"other 4xx", errors.New("status: 400 invalid caption"), "client", CauseContent, 400},
		{"unknown", errors.New("something happened"), "unknown", CauseTransient, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(tc.err)
			if got.Code != tc.code || got.Cause != tc.cause || got.HTTPStatus != tc.status {
				t.Fatalf("Classify(%v) = %+v, want %s/%s/%d", tc.err, got, tc.code, tc.cause, tc.status)
			}
		})
	}
	if (Classify(nil) != Failure{}) {
		t.Fatal("nil error classified")
	}
}
