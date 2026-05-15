package autouploader

import "testing"

// TestClassifyError pins the behaviour of the error classifier so future
// edits don't accidentally re-introduce the Pixelfed misclassification
// (HTTP 500 with body `{"error":"Unauthenticated."}` was being tagged
// `server` because the 5xx fallback ran before the body keywords).
func TestClassifyError(t *testing.T) {
	cases := []struct {
		name   string
		status int
		msg    string
		want   string
	}{
		// The headline case: Pixelfed/Laravel returns 500 for auth failures
		// with an "Unauthenticated." body. Body wins over status code.
		{
			"Pixelfed 500 Unauthenticated body",
			500,
			`pixelfed upload failed, status: 500 body: {"error":"Unauthenticated."}`,
			"auth",
		},

		// Plain HTTP status routing still works.
		{"plain 401", 401, "Unauthorized", "auth"},
		{"plain 403", 403, "forbidden", "auth"},
		{"plain 429", 429, "Too many requests", "rate_limited"},

		// 5xx with a non-auth body remains a server failure.
		{"plain 500 outage body", 500, "internal server error", "server"},
		{"plain 502 no auth hint", 502, "Bad gateway", "server"},

		// Network classification.
		{"network: no such host", 0, "dial tcp: lookup bsky.social: no such host", "network"},
		{"network: timeout", 0, "context deadline exceeded (i/o timeout)", "network"},

		// Body-only signals still beat unknown status codes.
		{"expired in body", 200, "token has expired", "auth"},
		{"rate limit phrase", 0, "we got a rate limit reply", "rate_limited"},

		// Nothing matched at all.
		{"unknown", 0, "something happened", "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyError(tc.status, tc.msg)
			if got != tc.want {
				t.Fatalf("classifyError(%d, %q) = %q, want %q",
					tc.status, tc.msg, got, tc.want)
			}
		})
	}
}
