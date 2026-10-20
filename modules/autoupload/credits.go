package autoupload

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/LNA-DEV/HomePageCompanion/capability/feed"
	"github.com/LNA-DEV/HomePageCompanion/capability/social"
)

// lookupTimeout caps every network lookup made for a mention. A timeout
// counts as "does not resolve".
const lookupTimeout = 5 * time.Second

// Credit is one person in the caption's names line: the name, plus the
// mention text when the person is tagged on the target ("@janedoe",
// "@jane@mastodon.social"). Account is who the mention resolved to.
type Credit struct {
	Slug    string
	Name    string
	Mention string
	Account string
}

// label renders the credit as it appears in the names line.
func (c Credit) label() string {
	if c.Mention == "" {
		return c.Name
	}
	return c.Name + " (" + c.Mention + ")"
}

// plainCredits names every person without a mention.
func plainCredits(people []feed.Person) []Credit {
	credits := make([]Credit, 0, len(people))
	for _, p := range people {
		credits = append(credits, Credit{Slug: p.Slug, Name: p.Name})
	}
	return credits
}

// resolveCredits names every person of the item and, where the connection
// tags models and the person agreed, mentions their profile on the target.
// A profile that does not resolve never fails the upload: the person is
// named without a mention, and the reason is logged by slug.
func resolveCredits(ctx context.Context, people []feed.Person, tagModels bool, lim social.Limits, target social.Publisher, notTagged func(slug, reason string)) []Credit {
	credits := plainCredits(people)
	if !tagModels || len(people) == 0 {
		return credits
	}
	mentions := 0
	for i, p := range people {
		if !p.Autotag {
			continue
		}
		if lim.MaxMentions > 0 && mentions >= lim.MaxMentions {
			notTagged(p.Slug, fmt.Sprintf("more than %d mentions", lim.MaxMentions))
			continue
		}
		m, err := mentionOf(ctx, p, lim.MentionProfiles, target)
		switch {
		case errors.Is(err, social.ErrIdentityUnknown):
			// Never mention yourself: without our own identity, no one is
			// mentioned.
			notTagged(p.Slug, err.Error())
			return plainCredits(people)
		case errors.Is(err, social.ErrNotAProfile):
			// No profile the target can use.
		case errors.Is(err, social.ErrSelf):
			notTagged(p.Slug, "profile is the posting account")
		case err != nil:
			notTagged(p.Slug, err.Error())
		default:
			credits[i].Mention, credits[i].Account = m.Text, m.Account
			mentions++
		}
	}
	return credits
}

// mentionOf resolves the first profile of the person the target can use,
// in the target's order of preference, then feed order.
func mentionOf(ctx context.Context, p feed.Person, kinds []string, target social.Publisher) (social.Mention, error) {
	for _, kind := range kinds {
		for _, pr := range p.Profiles {
			if pr.Platform != kind {
				continue
			}
			lctx, cancel := context.WithTimeout(ctx, lookupTimeout)
			m, err := target.ResolveMention(lctx, pr.URL)
			cancel()
			if errors.Is(err, social.ErrNotAProfile) {
				continue
			}
			return m, err
		}
	}
	return social.Mention{}, social.ErrNotAProfile
}

// mentionsOf are the resolved mentions: facets on Bluesky (only where the
// text still has them), photo tags on Instagram (also for a mention the
// caption had to turn back into a name).
func mentionsOf(credits []Credit) []social.Mention {
	var out []social.Mention
	for _, c := range credits {
		if c.Mention != "" {
			out = append(out, social.Mention{Text: c.Mention, Account: c.Account})
		}
	}
	return out
}
