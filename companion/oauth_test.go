package companion_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/companion"
)

func TestAuthCodeURL(t *testing.T) {
	c := companion.OAuthClient{AuthURL: "https://social.example/oauth/authorize", ClientID: "id", Scopes: []string{"read", "write:media"}, PKCE: true,
		Params: map[string]string{"force_login": "true"}}
	verifier, challenge := companion.NewPKCE()
	u, err := url.Parse(c.AuthCodeURL("https://companion.example/api/b/mastodon/oauth/callback", "st", challenge))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	want := map[string]string{"response_type": "code", "client_id": "id", "scope": "read write:media", "state": "st",
		"redirect_uri": "https://companion.example/api/b/mastodon/oauth/callback", "code_challenge_method": "S256", "force_login": "true"}
	for k, v := range want {
		if q.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, q.Get(k), v)
		}
	}
	sum := sha256.Sum256([]byte(verifier))
	if q.Get("code_challenge") != base64.RawURLEncoding.EncodeToString(sum[:]) || len(verifier) < 43 {
		t.Fatalf("challenge %q for verifier %q", q.Get("code_challenge"), verifier)
	}
	// Meta joins scopes with a comma and has no PKCE here.
	meta := companion.OAuthClient{AuthURL: "https://www.instagram.com/oauth/authorize?enable_fb_login=0", ClientID: "app", Scopes: []string{"a", "b"}, ScopeSep: ","}
	m, _ := url.Parse(meta.AuthCodeURL("https://x/cb", "s", "ignored"))
	if m.Query().Get("scope") != "a,b" || m.Query().Get("code_challenge") != "" || m.Query().Get("enable_fb_login") != "0" {
		t.Fatalf("meta url %s", m)
	}
}

func TestExchangeAndRefresh(t *testing.T) {
	var forms []url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		forms = append(forms, r.PostForm)
		w.Header().Set("Content-Type", "application/json")
		if r.PostForm.Get("grant_type") == "refresh_token" {
			w.Write([]byte(`{"access_token":"new","token_type":"Bearer","expires_in":"3600"}`))
			return
		}
		w.Write([]byte(`{"access_token":"tok","refresh_token":"ref","token_type":"Bearer","scope":"read write","expires_in":7200,"created_at":1}`))
	}))
	defer srv.Close()
	c := companion.OAuthClient{TokenURL: srv.URL, ClientID: "id", ClientSecret: "secret", PKCE: true}
	before := time.Now()
	tok, err := c.Exchange(context.Background(), srv.Client(), "the-code", "https://x/cb", "the-verifier")
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "tok" || tok.RefreshToken != "ref" || tok.Scope != "read write" || tok.ExpiresAt.Before(before.Add(7199*time.Second)) {
		t.Fatalf("token %+v", tok)
	}
	f := forms[0]
	for k, v := range map[string]string{"grant_type": "authorization_code", "code": "the-code", "redirect_uri": "https://x/cb",
		"client_id": "id", "client_secret": "secret", "code_verifier": "the-verifier"} {
		if f.Get(k) != v {
			t.Errorf("form %s = %q, want %q", k, f.Get(k), v)
		}
	}
	// A refresh that answers no new refresh token keeps the old one.
	r, err := c.Refresh(context.Background(), srv.Client(), "ref")
	if err != nil || r.AccessToken != "new" || r.RefreshToken != "ref" || r.ExpiresAt.IsZero() {
		t.Fatalf("refresh %+v %v", r, err)
	}
	if forms[1].Get("refresh_token") != "ref" || forms[1].Get("code_verifier") != "" {
		t.Fatalf("refresh form %v", forms[1])
	}
}

func TestTokenAnswers(t *testing.T) {
	// Instagram wraps the token; Threads has a number for user_id.
	tok, err := companion.ParseOAuthToken([]byte(`{"data":[{"access_token":"ig","user_id":"17841","permissions":"instagram_business_basic"}]}`), time.Now())
	if err != nil || tok.AccessToken != "ig" || !strings.Contains(string(tok.Raw), "17841") || !tok.ExpiresAt.IsZero() {
		t.Fatalf("wrapped %+v %v", tok, err)
	}
	tok, err = companion.ParseOAuthToken([]byte(`{"access_token":"th","user_id":12345,"scope":["a","b"]}`), time.Now())
	if err != nil || tok.Scope != "a b" {
		t.Fatalf("scope list %+v %v", tok, err)
	}
	if _, err := companion.ParseOAuthToken([]byte(`{"token_type":"Bearer"}`), time.Now()); err == nil {
		t.Fatal("no access_token accepted")
	}
}

func TestTokenErrors(t *testing.T) {
	for body, want := range map[string]string{
		`{"error":"invalid_grant","error_description":"The provided authorization grant is invalid"}`:  "invalid_grant: The provided authorization grant is invalid",
		`{"error":{"message":"Invalid verification code format.","type":"OAuthException","code":100}}`: "OAuthException: Invalid verification code format. (code 100)",
		`{"error_type":"OAuthException","code":400,"error_message":"Invalid redirect_uri"}`:            "OAuthException: Invalid redirect_uri",
		`<html>bad gateway</html>`: "<html>bad gateway</html>",
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(body))
		}))
		_, err := companion.OAuthClient{TokenURL: srv.URL, ClientID: "id"}.Exchange(context.Background(), srv.Client(), "c", "r", "")
		srv.Close()
		var oe *companion.OAuthError
		if !errors.As(err, &oe) || oe.Status != 400 || !strings.HasSuffix(err.Error(), want) {
			t.Errorf("%s: %v", body, err)
		}
	}
}
