package api

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testToken = "IGAA-test-token-0001"

// withRefreshServer points RefreshURL at h for the duration of the test.
func withRefreshServer(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	prev := RefreshURL
	RefreshURL = srv.URL + "/refresh_access_token"
	t.Cleanup(func() {
		RefreshURL = prev
		srv.Close()
	})
	return srv
}

func reply(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		io.WriteString(w, body)
	}
}

func TestRefreshTokenSuccess(t *testing.T) {
	withRefreshServer(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.Method != http.MethodGet || r.URL.Path != "/refresh_access_token" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if q.Get("grant_type") != "ig_refresh_token" || q.Get("access_token") != testToken {
			t.Errorf("unexpected query %v", q)
		}
		io.WriteString(w, `{"access_token":"IGAA-new","token_type":"bearer","expires_in":5183944}`)
	})

	got, err := RefreshToken(testToken)
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != "IGAA-new" || got.ExpiresIn != 5183944 {
		t.Fatalf("got %+v", got)
	}
}

func TestRefreshTokenErrors(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		wantErr string
	}{
		{"expired token", 400, `{"error":{"message":"Error validating access token: Session has expired","type":"OAuthException","code":190}}`, "status 400"},
		{"rate limited", 429, `{}`, ErrRateLimited.Error()},
		{"no token", 200, `{"token_type":"bearer","expires_in":5183944}`, "without access_token"},
		{"no expiry", 200, `{"access_token":"IGAA-new","token_type":"bearer"}`, "without access_token or expires_in"},
		{"not json", 200, `<html>`, "decode"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withRefreshServer(t, reply(tc.status, tc.body))
			_, err := RefreshToken(testToken)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
			}
			if tc.status == 429 && !errors.Is(err, ErrRateLimited) {
				t.Fatalf("429 must be ErrRateLimited, got %v", err)
			}
		})
	}
}

// TestRefreshTokenNetworkErrorRedacted: a transport failure must not carry
// the token, which travels in the query string (concept §3.1).
func TestRefreshTokenNetworkErrorRedacted(t *testing.T) {
	srv := withRefreshServer(t, reply(200, ""))
	srv.Close()

	_, err := RefreshToken(testToken)
	if err == nil {
		t.Fatal("expected a connection error")
	}
	if strings.Contains(err.Error(), testToken) {
		t.Fatalf("token leaked into error: %v", err)
	}
}
