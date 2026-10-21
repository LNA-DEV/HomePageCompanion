// Package profiles reads profile URLs of people in a photo (lna:profile)
// into the account they name, before any lookup, and resolves fediverse
// accounts through WebFinger. The networks' modules turn them into mentions.
package profiles

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/LNA-DEV/HomePageCompanion/lib/webfinger"
)

// Account is what a profile URL names: the scheme://host the URL points at,
// and the username (Instagram, Threads, Bluesky) or acct (user@domain,
// fediverse).
type Account struct {
	Base, Name string
}

// metaUsername is an Instagram username; Threads uses the same ones.
var metaUsername = regexp.MustCompile(`^[A-Za-z0-9._]{1,30}$`)

// instagramReserved are first path segments that are not profiles.
var instagramReserved = map[string]bool{"p": true, "reel": true, "explore": true, "stories": true}

// Instagram reads instagram.com/<user>/, with or without www.
func Instagram(raw string) (Account, bool) {
	u, segments, ok := split(raw)
	if !ok || (u.Host != "instagram.com" && u.Host != "www.instagram.com") || len(segments) == 0 {
		return Account{}, false
	}
	user := segments[0]
	if !metaUsername.MatchString(user) || instagramReserved[strings.ToLower(user)] {
		return Account{}, false
	}
	return Account{Base: u.Scheme + "://" + u.Host, Name: user}, true
}

// Threads reads threads.net/@<user> and threads.com/@<user>.
func Threads(raw string) (Account, bool) {
	u, segments, ok := split(raw)
	if !ok || len(segments) == 0 {
		return Account{}, false
	}
	switch strings.TrimPrefix(u.Host, "www.") {
	case "threads.net", "threads.com":
	default:
		return Account{}, false
	}
	user, found := strings.CutPrefix(segments[0], "@")
	if !found || !metaUsername.MatchString(user) {
		return Account{}, false
	}
	return Account{Base: u.Scheme + "://" + u.Host, Name: user}, true
}

var (
	// blueskyHandle is a handle: a domain name.
	blueskyHandle = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)+$`)
	// blueskyDID is a did:plc or did:web.
	blueskyDID = regexp.MustCompile(`^did:(plc|web):[A-Za-z0-9._:%-]+$`)
)

// Bluesky reads bsky.app/profile/<handle or DID>.
func Bluesky(raw string) (Account, bool) {
	u, segments, ok := split(raw)
	if !ok || strings.TrimPrefix(u.Host, "www.") != "bsky.app" || len(segments) != 2 || segments[0] != "profile" {
		return Account{}, false
	}
	actor := segments[1]
	if !blueskyHandle.MatchString(actor) && !blueskyDID.MatchString(actor) {
		return Account{}, false
	}
	return Account{Base: u.Scheme + "://" + u.Host, Name: actor}, true
}

// fediverseUser is a Mastodon or Pixelfed username.
var fediverseUser = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*$`)

// Fediverse reads a Mastodon or Pixelfed profile URL:
//
//	https://host/@user          → user@host
//	https://host/@user@domain   → user@domain
//	https://host/users/user     → user@host
//	https://host/user           → user@host (Pixelfed)
//
// WebFinger always goes to the host of the URL.
func Fediverse(raw string) (Account, bool) {
	u, segments, ok := split(raw)
	if !ok {
		return Account{}, false
	}
	var user, domain string
	switch {
	case len(segments) == 1 && strings.HasPrefix(segments[0], "@"):
		user, domain, _ = strings.Cut(segments[0][1:], "@")
	case len(segments) == 2 && segments[0] == "users":
		user = segments[1]
	case len(segments) == 1:
		user = segments[0]
	default:
		return Account{}, false
	}
	if domain == "" {
		domain = u.Host
	}
	if !fediverseUser.MatchString(user) || strings.ContainsAny(domain, "/@") {
		return Account{}, false
	}
	return Account{Base: u.Scheme + "://" + u.Host, Name: user + "@" + domain}, true
}

// FediverseMention asks the host of the profile URL for the canonical acct.
// The target's server resolves "@user@domain" again on its own when the post
// is created.
func FediverseMention(ctx context.Context, a Account) (mention, acct string, err error) {
	acct, err = webfinger.Lookup(ctx, a.Base, a.Name)
	if err != nil {
		return "", "", err
	}
	return "@" + acct, acct, nil
}

// FediverseSelf returns the posting account's canonical acct, from its
// username and the instance it posts on.
func FediverseSelf(ctx context.Context, instance, username string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(instance))
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("instance %q has no host", instance)
	}
	return webfinger.Lookup(ctx, u.Scheme+"://"+u.Host, username+"@"+u.Host)
}

// split parses an absolute http(s) URL and returns its lowercase host and
// its non-empty path segments.
func split(raw string) (*url.URL, []string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, nil, false
	}
	u.Host = strings.ToLower(u.Host)
	var segments []string
	for _, s := range strings.Split(u.Path, "/") {
		if s != "" {
			segments = append(segments, s)
		}
	}
	return u, segments, true
}
