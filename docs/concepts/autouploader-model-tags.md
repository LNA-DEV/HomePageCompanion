# Handover: tagging models from the gallery feed

**Status:** handover from Home-Page, 2026-10-12. The feed side is **built in Home-Page, not committed, not deployed**. Until it is deployed, no live feed item carries the elements below, so the companion must treat their absence as "nothing to do". A build of the Home-Page working tree (`.test-site/en/gallery/index.xml`) has them already, and gofeed v1.3.0 parses them exactly as §3 shows (checked against that build).
**Scope:** this repository only. The feed format below is the contract between the two repos; change it in both or not at all.

## 1. Goal

When a gallery photo shows a person who agreed to be tagged, the autouploader mentions that person on the target platform. The person, their profiles and their consent are maintained in Home-Page (`data/models.yaml`). The companion reads them from the gallery RSS feed and nowhere else.

## 2. What the feed will carry

The gallery feed (`/<lang>/gallery/index.xml`) declares one extra namespace and adds, per item, one block per person in the photo:

```xml
<rss version="2.0" xmlns:media="http://search.yahoo.com/mrss/"
     xmlns:lna="https://lna-dev.net/ns/gallery/1" …>
  …
  <item>
    <title>…</title>
    <guid isPermaLink="false">6885f633-…</guid>
    …
    <media:credit role="performer">Jane Doe</media:credit>
    <lna:model slug="jane-doe" name="Jane Doe"
               url="https://lna-dev.net/en/gallery/models/jane-doe/"
               autotag="true">
      <lna:profile platform="instagram" url="https://www.instagram.com/janedoe/"/>
      <lna:profile platform="mastodon"  url="https://mastodon.social/@janedoe"/>
      <lna:profile platform="website"   url="https://janedoe.example/"/>
    </lna:model>
  </item>
```

| Field | Meaning |
|---|---|
| `slug` | The person's stable id. Never changes, never reused for someone else. |
| `name` | Display name, the same in every language. **May change at any time**, so read it each run and never store it. |
| `url` | Absolute URL of the person's page on the site, in the feed's language. |
| `autotag` | **Always present**, exactly `"true"` or `"false"`. Consent to being tagged by the uploader. |
| `lna:profile/@platform` | Lowercase key from `links[].name` in `data/models.yaml`: `instagram`, `threads`, `bluesky`, `mastodon`, `pixelfed`, and keys you cannot tag such as `website` and `github`. Ignore keys you do not know. |
| `lna:profile/@url` | Absolute profile URL **as the author typed it**, not normalised (`www.` or not, trailing slash or not). |

Guarantees from the feed side:

- **Only people with `visibility: public` appear.** `hidden` and `unlisted` people never do. A person missing from the feed is a person who must not be named.
- The profiles are **always** listed, whatever `autotag` says. `autotag` alone decides whether to tag.
- An item can have several `lna:model` blocks (a group photo), in the order the photo lists them. Profiles keep their order from `data/models.yaml`.
- An item without people has no `lna:model` and no `media:credit`.
- `guid`, `link`, `title`, `description` and `category` do not change. The alt-text regex on `description` keeps working.
- `media:credit role="performer"` carries the same names in standard Media RSS. It is informational; read `lna:model`.
- Enforced by the Home-Page build, which stops otherwise: `autotag:` in the data is a YAML bool (anything else is an error and, should it get through, reads as `"false"`), and every profile has a platform and an absolute `http(s)://` URL.

## 3. Reading it with gofeed (v1.3.0)

```go
for _, m := range entry.Extensions["lna"]["model"] {
    slug, name := m.Attrs["slug"], m.Attrs["name"]
    tag := m.Attrs["autotag"] == "true"
    for _, p := range m.Children["profile"] {
        platform, profileURL := p.Attrs["platform"], p.Attrs["url"]
        …
    }
}
```

- gofeed stores extensions under the **prefix declared in the feed**, because the namespace is not one of its canonical ones (`internal/shared/extparser.go`, `PrefixForNamespace`). So `lna` is part of the contract, not just the URI.
- Attributes are keyed by their local name. A missing key reads as `""`, which counts as "do not tag".
- `media:credit` is under the canonical prefix: `entry.Extensions["media"]["credit"]`.

## 4. Rules agreed in Home-Page

1. **Tag only when `autotag == "true"` exactly.** Missing, empty or anything else means no tag.
2. **The feed is the only source, read each run.** Do not keep names or profiles keyed by slug across runs. Removing someone in Home-Page means they disappear from the feed, and from then on they must not be tagged. Caching a *resolution* (a Bluesky DID for a handle) keyed by the profile URL is fine.
3. Never mention the posting account itself. The owner's own record (`lukas-nagel`) lists his own profiles. It has `autotag: false`, but do not rely on that alone.

Left to the companion: whether an untagged person's name goes into the caption as plain text (the site shows it on the photo), which profile a target uses, where mentions rank when a caption gets too long (Bluesky 300, Threads 500), and what happens when a handle does not resolve. My suggestions:

- Use the profile whose `platform` matches the target. Mastodon and Pixelfed both speak ActivityPub, so either fediverse profile can be mentioned from either target; prefer the same platform.
- Drop hashtags and the EXIF line before mentions.
- If a handle does not resolve, post without the mention and log it. Do not fail the upload.

## 5. From profile URL to mention (not verified against the APIs)

| Platform | Profile URL | Mention |
|---|---|---|
| Instagram | `instagram.com/<user>/` | `@user` in the caption. The Graph API's `user_tags` (a tag on the photo) needs x/y positions, which the feed does not have. |
| Threads | `threads.net/@<user>` or `threads.com/@<user>` | `@user` in the text |
| Bluesky | `bsky.app/profile/<handle or did>` | Mention facet with the DID, from `com.atproto.identity.resolveHandle` |
| Mastodon | `https://<host>/@<user>` | `@user@host` in the status |
| Pixelfed | `https://<host>/<user>` | `@user@host` in the status |

For the fediverse, the web host and the WebFinger domain can differ. The robust way is to ask the target's own instance to resolve the profile URL (Mastodon API search with `resolve=true`) and use the `acct` it returns.

## 6. Data at handover (2026-10-12)

- Two people, both `public`, **neither with `autotag: true`**:
  - `lukas-nagel` (the owner) with `website`, `mastodon`, `pixelfed`, `bluesky` and `github`, on 4 photos.
  - `markus-penninger` with no links, on 12 photos.
- All 16 photos are outside the archive, so all of them are in the feed.
- Once deployed, the feed carries `lna:model` blocks but nothing to tag. That makes it a good live test of the "present but not allowed" path. Use the example in §2 as the fixture for the tagging path.
