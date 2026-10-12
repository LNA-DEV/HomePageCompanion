package autouploader

import (
	"reflect"
	"testing"

	"github.com/mmcdole/gofeed"
)

// peopleFeed is the fixture from handover §2 (docs/concepts/
// autouploader-model-tags.md), plus the edge cases peopleFromEntry handles.
const peopleFeed = `<?xml version="1.0" encoding="utf-8"?>
<rss version="2.0" xmlns:media="http://search.yahoo.com/mrss/" xmlns:lna="https://lna-dev.net/ns/gallery/1">
<channel>
  <title>Gallery</title>
  <item>
    <title>Group</title>
    <guid isPermaLink="false">6885f633-group</guid>
    <media:credit role="performer">Jane &amp; Doe</media:credit>
    <lna:model slug="jane-doe" name="Jane &amp; Doe"
               url="https://lna-dev.net/en/gallery/models/jane-doe/"
               autotag="true">
      <lna:profile platform="instagram" url="https://www.instagram.com/janedoe/"/>
      <lna:profile platform="mastodon"  url="https://mastodon.social/@janedoe"/>
      <lna:profile platform="website"   url="https://janedoe.example/"/>
    </lna:model>
    <lna:model slug="max-muster" name="  Max   Muster " url="https://lna-dev.net/en/gallery/models/max-muster/" autotag="TRUE"/>
    <lna:model slug="nameless" name=" " url="https://lna-dev.net/en/gallery/models/nameless/" autotag="true"/>
    <lna:model slug="markus-penninger" name="Markus Penninger" url="https://lna-dev.net/en/gallery/models/markus-penninger/" autotag="false"/>
    <lna:model slug="no-autotag" name="No Autotag" url="https://lna-dev.net/en/gallery/models/no-autotag/"/>
  </item>
  <item>
    <title>Landscape</title>
    <guid isPermaLink="false">6885f633-landscape</guid>
  </item>
</channel>
</rss>`

func TestPeopleFromEntry(t *testing.T) {
	feed, err := gofeed.NewParser().ParseString(peopleFeed)
	if err != nil {
		t.Fatal(err)
	}

	want := []Person{
		{Slug: "jane-doe", Name: "Jane & Doe", Autotag: true, Profiles: []Profile{
			{Platform: "instagram", URL: "https://www.instagram.com/janedoe/"},
			{Platform: "mastodon", URL: "https://mastodon.social/@janedoe"},
			{Platform: "website", URL: "https://janedoe.example/"},
		}},
		// Only exactly "true" counts as consent.
		{Slug: "max-muster", Name: "Max Muster", Autotag: false},
		// "nameless" is skipped: a person without a name cannot be named.
		{Slug: "markus-penninger", Name: "Markus Penninger", Autotag: false},
		{Slug: "no-autotag", Name: "No Autotag", Autotag: false},
	}
	if got := peopleFromEntry(feed.Items[0]); !reflect.DeepEqual(got, want) {
		t.Errorf("people = %+v\nwant     %+v", got, want)
	}

	if got := peopleFromEntry(feed.Items[1]); len(got) != 0 {
		t.Errorf("item without lna:model has people: %+v", got)
	}
	if got := peopleFromEntry(nil); got != nil {
		t.Errorf("nil entry has people: %+v", got)
	}
}

func TestPlainCredits(t *testing.T) {
	people := []Person{{Slug: "a", Name: "A", Autotag: true}, {Slug: "b", Name: "B"}}
	want := []Credit{{Slug: "a", Name: "A"}, {Slug: "b", Name: "B"}}
	if got := plainCredits(people); !reflect.DeepEqual(got, want) {
		t.Errorf("plainCredits = %+v, want %+v", got, want)
	}
}
