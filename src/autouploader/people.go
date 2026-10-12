package autouploader

import (
	"log"
	"strings"

	"github.com/mmcdole/gofeed"
)

// Person is one lna:model block of a feed item: someone shown in the photo.
// See docs/concepts/autouploader-model-tags.md for the feed contract. Nothing
// here is stored; the feed is read again on every publish.
type Person struct {
	Slug     string
	Name     string
	Autotag  bool      // the feed's autotag attribute is exactly "true"
	Profiles []Profile // feed order
}

// Profile is one lna:profile of a Person. Platform is a lowercase key such as
// "instagram" or "mastodon"; keys we cannot tag ("website") never match a
// target. URL is as the author typed it.
type Profile struct{ Platform, URL string }

// peopleFromEntry reads the lna:model blocks of an item in feed order. An item
// without blocks has no people. A block without a name is skipped, because
// the person cannot be named.
func peopleFromEntry(entry *gofeed.Item) []Person {
	if entry == nil {
		return nil
	}
	var people []Person
	for _, m := range entry.Extensions["lna"]["model"] {
		slug := strings.TrimSpace(m.Attrs["slug"])
		name := strings.Join(strings.Fields(m.Attrs["name"]), " ")
		if name == "" {
			log.Printf("autouploader: %s: model %q has no name, skipped", entry.GUID, slug)
			continue
		}
		p := Person{Slug: slug, Name: name, Autotag: m.Attrs["autotag"] == "true"}
		for _, pr := range m.Children["profile"] {
			p.Profiles = append(p.Profiles, Profile{
				Platform: strings.ToLower(strings.TrimSpace(pr.Attrs["platform"])),
				URL:      strings.TrimSpace(pr.Attrs["url"]),
			})
		}
		people = append(people, p)
	}
	return people
}

// Credit is one person in the caption's names line: the name, plus the
// mention text when the person is tagged on the target ("@janedoe",
// "@jane@mastodon.social"). An empty Mention names the person only.
// Account is who the mention resolved to: the DID on Bluesky, the username
// on Instagram and Threads, the canonical acct on the fediverse.
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
func plainCredits(people []Person) []Credit {
	credits := make([]Credit, 0, len(people))
	for _, p := range people {
		credits = append(credits, Credit{Slug: p.Slug, Name: p.Name})
	}
	return credits
}
