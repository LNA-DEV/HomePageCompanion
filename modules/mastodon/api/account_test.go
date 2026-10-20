package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestVerifyCredentials(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/accounts/verify_credentials" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer good" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		io.WriteString(w, `{"id":"1","username":"lna_dev","acct":"lna_dev"}`)
	}))
	defer srv.Close()

	a, err := VerifyCredentials(context.Background(), srv.URL+"/", "good")
	if err != nil || a.Username != "lna_dev" {
		t.Errorf("VerifyCredentials = %+v, %v", a, err)
	}
	if _, err := VerifyCredentials(context.Background(), srv.URL, "bad"); err == nil {
		t.Error("VerifyCredentials with a bad token succeeded")
	}
}
