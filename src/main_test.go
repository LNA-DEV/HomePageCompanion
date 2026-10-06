package main

import (
	"regexp"
	"testing"
)

func TestOriginAllowed(t *testing.T) {
	domain := regexp.MustCompile(`^https?://([a-z0-9-]+\.)*` + regexp.QuoteMeta("lna-dev.net") + `(:[0-9]+)?$`)
	onion := "http://lnadevwj2vzomixiunv7i4lahwpoxh6zw56cxbce3uui5ijmwt4czpyd.onion"
	extra := []string{onion}

	for origin, want := range map[string]bool{
		"https://lna-dev.net":           true,
		"https://companion.lna-dev.net": true,
		"http://localhost:1313":         true,
		onion:                           true,
		onion + "/":                     false, // exact match only
		"https://lnadevwj2vzomixiunv7i4lahwpoxh6zw56cxbce3uui5ijmwt4czpyd.onion": false,
		"http://other.onion":              false,
		"https://lna-dev.net.example.com": false,
		"https://evil-lna-dev.net":        false,
		"":                                false,
	} {
		if got := originAllowed(origin, domain, extra); got != want {
			t.Errorf("%q: %v, want %v", origin, got, want)
		}
	}
	if originAllowed(onion, domain, nil) {
		t.Error("the onion is admitted without being configured")
	}
}
