package autouploader

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/blueskyapi"
	"github.com/LNA-DEV/HomePageCompanion/config"
	"github.com/LNA-DEV/HomePageCompanion/instagramapi"
	"github.com/LNA-DEV/HomePageCompanion/mastodonapi"
	"github.com/LNA-DEV/HomePageCompanion/pixelfedapi"
	"github.com/LNA-DEV/HomePageCompanion/threadsapi"
	"github.com/LNA-DEV/HomePageCompanion/webfinger"
)

// lookupTimeout caps every network lookup made for a mention. A timeout
// counts as "does not resolve".
const lookupTimeout = 5 * time.Second

// instagramMaxMentions is Instagram's limit of @ tags per caption. Further
// people are named without a mention.
const instagramMaxMentions = 20

// account is what a profile URL names, before any lookup: the scheme://host
// the URL points at, and the username (Instagram, Threads) or acct
// (user@domain, fediverse).
type account struct {
	base, name string
}

// mentionTarget is how one target platform turns profiles into mentions.
type mentionTarget struct {
	// profiles are the lna:profile platforms usable on the target, preferred
	// first.
	profiles []string
	// maxMentions caps mentions per post (0 = unlimited).
	maxMentions int
	// parse reads a profile URL without network.
	parse func(profileURL string) (account, bool)
	// resolve returns the mention text and an identity to compare with self.
	resolve func(ctx context.Context, a account) (mention, id string, err error)
	// self returns the posting account's identity.
	self func(ctx context.Context) (string, error)
}

// resolveCredits names every person of the item and, where the connection
// tags models and the person agreed, mentions their profile on the target.
// A profile that does not resolve never fails the upload: the person is
// named without a mention, and the reason is logged by slug.
func resolveCredits(guid string, people []Person, conn config.Connection, target config.Target) []Credit {
	if !conn.TagModels {
		return plainCredits(people)
	}
	mt, ok := mentionTargetFor(target)
	if !ok {
		return plainCredits(people)
	}
	return resolveWith(guid, target.Platform, people, mt)
}

func resolveWith(guid, platform string, people []Person, mt mentionTarget) []Credit {
	credits := plainCredits(people)
	notTagged := func(slug, reason string) {
		log.Printf("autouploader: %s on %s: model %s not tagged: %s", guid, platform, slug, reason)
	}

	// Who could be mentioned at all: consent and a profile for the target.
	accounts := make([]account, len(people))
	candidates := 0
	for i, p := range people {
		if !p.Autotag {
			continue
		}
		a, ok := pickProfile(p, mt)
		if !ok {
			continue
		}
		accounts[i] = a
		candidates++
	}
	if candidates == 0 {
		return credits
	}

	// Never mention yourself (handover §4.3). Without our own identity, no
	// one is mentioned.
	ctx, cancel := context.WithTimeout(context.Background(), lookupTimeout)
	self, err := mt.self(ctx)
	cancel()
	if err != nil {
		log.Printf("autouploader: %s on %s: no mentions, own identity unknown: %v", guid, platform, err)
		return credits
	}
	self = strings.ToLower(self)

	mentions := 0
	for i, p := range people {
		if accounts[i].name == "" {
			continue
		}
		if mt.maxMentions > 0 && mentions >= mt.maxMentions {
			notTagged(p.Slug, fmt.Sprintf("more than %d mentions", mt.maxMentions))
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), lookupTimeout)
		mention, id, err := mt.resolve(ctx, accounts[i])
		cancel()
		switch {
		case err != nil:
			notTagged(p.Slug, err.Error())
		case strings.ToLower(id) == self:
			notTagged(p.Slug, "profile is the posting account")
		default:
			credits[i].Mention, credits[i].Account = mention, id
			mentions++
		}
	}
	return credits
}

// pickProfile returns the first profile of the person that the target can
// use, in the target's order of preference, then feed order.
func pickProfile(p Person, mt mentionTarget) (account, bool) {
	for _, platform := range mt.profiles {
		for _, pr := range p.Profiles {
			if pr.Platform != platform {
				continue
			}
			if a, ok := mt.parse(pr.URL); ok {
				return a, true
			}
		}
	}
	return account{}, false
}

// mentionTargetFor returns how the target's platform mentions people.
func mentionTargetFor(target config.Target) (mentionTarget, bool) {
	switch target.Platform {
	case "bluesky":
		// target.Username must be a handle, not an email: it is looked up
		// as is, to avoid a second login just to learn the DID.
		return mentionTarget{
			profiles: []string{"bluesky"},
			parse:    parseBlueskyURL,
			resolve:  blueskyMention,
			self: func(ctx context.Context) (string, error) {
				p, err := blueskyapi.GetProfile(ctx, target.Username)
				if err != nil {
					return "", err
				}
				return p.Did, nil
			},
		}, true
	case "instagram":
		return mentionTarget{
			profiles:    []string{"instagram"},
			maxMentions: instagramMaxMentions,
			parse:       parseInstagramURL,
			resolve:     metaMention,
			self: func(ctx context.Context) (string, error) {
				return instagramapi.Me(ctx, target.AccessToken)
			},
		}, true
	case "threads":
		return mentionTarget{
			profiles: []string{"threads"},
			parse:    parseThreadsURL,
			resolve:  metaMention,
			self: func(ctx context.Context) (string, error) {
				return threadsapi.Me(ctx, target.AccessToken)
			},
		}, true
	case "mastodon":
		return mentionTarget{
			profiles: []string{"mastodon", "pixelfed"},
			parse:    parseFediverseURL,
			resolve:  fediverseMention,
			self: func(ctx context.Context) (string, error) {
				a, err := mastodonapi.VerifyCredentials(ctx, target.InstanceUrl, target.PAT)
				if err != nil {
					return "", err
				}
				return fediverseSelf(ctx, target.InstanceUrl, a.Username)
			},
		}, true
	case "pixelfed":
		return mentionTarget{
			profiles: []string{"pixelfed", "mastodon"},
			parse:    parseFediverseURL,
			resolve:  fediverseMention,
			self: func(ctx context.Context) (string, error) {
				a, err := pixelfedapi.VerifyCredentials(ctx, target.InstanceUrl, target.PAT)
				if err != nil {
					return "", err
				}
				return fediverseSelf(ctx, target.InstanceUrl, a.Username)
			},
		}, true
	default:
		return mentionTarget{}, false
	}
}

// metaMention mentions an Instagram or Threads username. Meta has no lookup
// for arbitrary accounts, so nothing is checked: a wrong URL gives a dead
// mention.
func metaMention(_ context.Context, a account) (string, string, error) {
	return "@" + a.name, a.name, nil
}

// blueskyMention looks up the handle or DID from the profile URL. The
// mention shows the current handle; the facet carries the DID.
func blueskyMention(ctx context.Context, a account) (string, string, error) {
	p, err := blueskyapi.GetProfile(ctx, a.name)
	if err != nil {
		return "", "", err
	}
	if p.Handle == "handle.invalid" {
		return "", "", fmt.Errorf("%s has no valid handle", p.Did)
	}
	return "@" + p.Handle, p.Did, nil
}

// fediverseMention asks the host of the profile URL for the canonical acct.
// The target's server resolves "@user@domain" again on its own when the post
// is created.
func fediverseMention(ctx context.Context, a account) (string, string, error) {
	acct, err := webfinger.Lookup(ctx, a.base, a.name)
	if err != nil {
		return "", "", err
	}
	return "@" + acct, acct, nil
}

// fediverseSelf returns the posting account's canonical acct, from its
// username and the instance it posts on.
func fediverseSelf(ctx context.Context, instance, username string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(instance))
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("instance %q has no host", instance)
	}
	return webfinger.Lookup(ctx, u.Scheme+"://"+u.Host, username+"@"+u.Host)
}

// metaUsername is an Instagram username; Threads uses the same ones.
var metaUsername = regexp.MustCompile(`^[A-Za-z0-9._]{1,30}$`)

// instagramReserved are first path segments that are not profiles.
var instagramReserved = map[string]bool{"p": true, "reel": true, "explore": true, "stories": true}

// parseInstagramURL reads instagram.com/<user>/, with or without www.
func parseInstagramURL(raw string) (account, bool) {
	u, segments, ok := splitProfileURL(raw)
	if !ok || (u.Host != "instagram.com" && u.Host != "www.instagram.com") || len(segments) == 0 {
		return account{}, false
	}
	user := segments[0]
	if !metaUsername.MatchString(user) || instagramReserved[strings.ToLower(user)] {
		return account{}, false
	}
	return account{base: u.Scheme + "://" + u.Host, name: user}, true
}

// parseThreadsURL reads threads.net/@<user> and threads.com/@<user>.
func parseThreadsURL(raw string) (account, bool) {
	u, segments, ok := splitProfileURL(raw)
	if !ok || len(segments) == 0 {
		return account{}, false
	}
	switch strings.TrimPrefix(u.Host, "www.") {
	case "threads.net", "threads.com":
	default:
		return account{}, false
	}
	user, found := strings.CutPrefix(segments[0], "@")
	if !found || !metaUsername.MatchString(user) {
		return account{}, false
	}
	return account{base: u.Scheme + "://" + u.Host, name: user}, true
}

var (
	// blueskyHandle is a handle: a domain name.
	blueskyHandle = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)+$`)
	// blueskyDID is a did:plc or did:web.
	blueskyDID = regexp.MustCompile(`^did:(plc|web):[A-Za-z0-9._:%-]+$`)
)

// parseBlueskyURL reads bsky.app/profile/<handle or DID>.
func parseBlueskyURL(raw string) (account, bool) {
	u, segments, ok := splitProfileURL(raw)
	if !ok || strings.TrimPrefix(u.Host, "www.") != "bsky.app" || len(segments) != 2 || segments[0] != "profile" {
		return account{}, false
	}
	actor := segments[1]
	if !blueskyHandle.MatchString(actor) && !blueskyDID.MatchString(actor) {
		return account{}, false
	}
	return account{base: u.Scheme + "://" + u.Host, name: actor}, true
}

// fediverseUser is a Mastodon or Pixelfed username.
var fediverseUser = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*$`)

// parseFediverseURL reads a Mastodon or Pixelfed profile URL:
//
//	https://host/@user          → user@host
//	https://host/@user@domain   → user@domain
//	https://host/users/user     → user@host
//	https://host/user           → user@host (Pixelfed)
//
// WebFinger always goes to the host of the URL.
func parseFediverseURL(raw string) (account, bool) {
	u, segments, ok := splitProfileURL(raw)
	if !ok {
		return account{}, false
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
		return account{}, false
	}
	if domain == "" {
		domain = u.Host
	}
	if !fediverseUser.MatchString(user) || strings.ContainsAny(domain, "/@") {
		return account{}, false
	}
	return account{base: u.Scheme + "://" + u.Host, name: user + "@" + domain}, true
}

// splitProfileURL parses an absolute http(s) URL and returns its lowercase
// host and its non-empty path segments.
func splitProfileURL(raw string) (*url.URL, []string, bool) {
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
