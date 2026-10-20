package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func clientIPVia(t *testing.T, proxies []string, remote, forwarded string) string {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	if err := r.SetTrustedProxies(proxies); err != nil {
		t.Fatal(err)
	}
	var got string
	r.GET("/", func(c *gin.Context) { got = c.ClientIP() })
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = remote + ":40000"
	if forwarded != "" {
		req.Header.Set("X-Forwarded-For", forwarded)
	}
	r.ServeHTTP(httptest.NewRecorder(), req)
	return got
}

// A client must not choose its own IP by sending X-Forwarded-For (the native
// like deduplication hashes it; webmention.md §3.2). Behind the proxy on the
// private network, the address the proxy appended counts.
func TestTrustedProxies(t *testing.T) {
	for _, tc := range []struct {
		name, env, remote, forwarded, want string
	}{
		{"default: direct client spoofing", "", "203.0.113.5", "9.9.9.9", "203.0.113.5"},
		{"default: behind the compose proxy", "", "172.18.0.2", "9.9.9.9, 198.51.100.7", "198.51.100.7"},
		{"default: proxy sets the header", "", "172.18.0.2", "198.51.100.7", "198.51.100.7"},
		{"none: never trust the header", "none", "172.18.0.2", "198.51.100.7", "172.18.0.2"},
		{"list: only the listed proxy", "10.1.2.3", "172.18.0.2", "198.51.100.7", "172.18.0.2"},
		{"list: the listed proxy", "10.1.2.3, 172.18.0.0/16", "172.18.0.2", "198.51.100.7", "198.51.100.7"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proxies, err := trustedProxies(tc.env)
			if err != nil {
				t.Fatal(err)
			}
			if got := clientIPVia(t, proxies, tc.remote, tc.forwarded); got != tc.want {
				t.Fatalf("client ip %s, want %s", got, tc.want)
			}
		})
	}
	if _, err := trustedProxies("not-an-ip"); err == nil {
		t.Fatal("a malformed entry is accepted")
	}
}
