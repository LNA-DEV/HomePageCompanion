package redact

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const secret = "SECRET-TOKEN-123"

// TestURLErrorFromTransport runs a real request against a closed server: the
// error net/http returns carries the full URL until URLError strips it.
func TestURLErrorFromTransport(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	base := srv.URL
	srv.Close()

	_, err := http.Get(base + "/v1.0/me?access_token=" + secret + "&fields=id")
	if err == nil {
		t.Fatal("expected a connection error")
	}
	if !strings.Contains(err.Error(), secret) {
		t.Fatalf("precondition: net/http no longer puts the query in errors: %v", err)
	}

	err = URLError(err)
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("token still in error: %v", err)
	}
	if !strings.Contains(err.Error(), "/v1.0/me") {
		t.Fatalf("path should stay for debugging: %v", err)
	}
}

func TestURLErrorFromParse(t *testing.T) {
	_, err := url.Parse("https://graph.threads.net/x?access_token=" + secret + "\x7f")
	if err == nil {
		t.Fatal("expected a parse error")
	}
	if got := URLError(err).Error(); strings.Contains(got, secret) {
		t.Fatalf("token still in error: %v", got)
	}
}

func TestURLErrorWrapped(t *testing.T) {
	inner := &url.Error{Op: "Get", URL: "https://x/y?access_token=" + secret, Err: errors.New("boom")}
	err := URLError(errors.Join(errors.New("context"), inner))
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("token still in wrapped error: %v", err)
	}
}

func TestURLErrorPassThrough(t *testing.T) {
	if URLError(nil) != nil {
		t.Fatal("nil must stay nil")
	}
	plain := errors.New("threads publish status 400 body: {}")
	if got := URLError(plain); got != plain {
		t.Fatalf("plain error changed: %v", got)
	}
}
