# Concept: Naming and tagging models in autouploader posts

**Status:** **implemented, not committed, not deployed.** All decisions taken (§9), all stages built (§8, details in §12). Still open: the live tests of §11.
**Contract:** the feed format in `docs/concepts/autouploader-model-tags.md` (handover from Home-Page, 2026-10-12). This concept builds on it and does not change it.
**Scope:** this repository only. Home-Page is not touched.
**Measured:** 2026-10-12, against the code at `f42a7aa` plus the uncommitted caption reorder (hashtags last), gofeed v1.3.0 from the module cache, the live gallery feed `https://lna-dev.net/en/gallery/index.xml` (589 items, no `lna:model` yet), Home-Page's uncommitted `layouts/_default/rss.xml`, the platform docs in §4, and public read-only lookups against the owner's own profiles.

When a gallery photo shows a person, the post names that person on every platform. If the person agreed to be tagged and the connection has tagging on, the post also mentions them on that platform. The feed gives one link per profile. The companion turns each link into whatever the target platform needs for a mention.

## 1. Goal

1. Every person the feed lists for an item is named in the post, on every platform, in feed order. A name is never dropped, and it is **cut only as a last resort**, once everything else in the caption is gone (§5.3).
2. If tagging is on for the connection and the person has `autotag="true"`, the post mentions the person's profile on the target platform. The person is then linked and notified. On Instagram the person is also tagged on the photo.
3. A profile that cannot be resolved never fails the upload. The person is named without a mention.
4. The posting account never mentions itself.
5. No name or profile is stored. Everything is read from the feed on each run (handover §4.2).

## 2. Today

| Piece | State | Where |
|---|---|---|
| Feed | Parsed live on every publish with gofeed. `entry.Extensions` is available on the chosen item. Nothing reads it | `src/autouploader/main.go:182-185` |
| Caption | `BuildCaption` joins base text (alt text and caption), EXIF, copyright and hashtags. Over the limit it drops copyright, then EXIF, then hashtags one by one, then hard-cuts the base text | `src/autouploader/caption.go:32-113` |
| Limits | Bluesky 300, Threads 500, everything else 2000, counted in runes | `src/autouploader/main.go:122`, `caption.go:160` |
| Mentions | None. Every platform gets the caption string unchanged | `pixelfed.go:29`, `mastodon.go:36`, `instagram.go:23`, `threads.go:27`, `bluesky.go:36` |
| Bluesky facets | `ExtractFacets` builds hashtag and link facets from the text. No mention facets. `CreatePost` is also used by the microblog | `src/blueskyapi/posts.go:97,155`, `src/microblog/microblog.go:222` |
| Own identity per target | Bluesky: `Session.Did`, but the login happens inside `publishBlueskyEntry`, after the caption is built. Other platforms: not known to the code | `src/blueskyapi/client.go:33`, `src/autouploader/bluesky.go:14` |
| Feed side | Built in Home-Page, uncommitted, not deployed. The live feed has 0 `lna:model` blocks | Home-Page `layouts/_default/rss.xml` |

Verified on 2026-10-12:

- **Reading the extension works.** gofeed v1.3.0 was run against the fixture from handover §2. `entry.Extensions["lna"]["model"]` returns one entry per block, with `Attrs` `slug`, `name`, `url`, `autotag` and `Children["profile"]`. Entities in attributes arrive decoded (`name="Jane &amp; Doe"` becomes `Jane & Doe`).
- **Alt text arrives decoded.** 17 alt texts in the live feed contain `&#39;` in the XML. After `extractAltText`, 0 of 589 contain `&`.
- **Live feed figures (589 items).** Alt text: median 91, p90 172, max 404 characters. Categories: median 11, max 28.

## 3. Findings regardless of models (Stage 0)

### 3.1 Mastodon gets a 2000-character budget, the instances allow 500

- `captionLimitFor` falls through to 2000 for `mastodon` (`src/autouploader/main.go:122-131`).
- `mastodon.online` (the owner's profile) and `mastodon.social` (the local config) both report `max_characters: 500` in `/api/v2/instance`. `pixelfed.de` reports 5000.
- **Trigger:** with `includeAltText` on, a long alt text plus caption, hashtags and EXIF goes past 500. Mastodon rejects the status with 422 and the upload fails. Without alt text the median caption stays well below 500.
- **Why it matters here:** names are cut only as a last resort (Goal 1). That only works if the budget is the real one.
- **Fix:** 500 for `mastodon`. Optionally, a per-target override `maxCaptionLength` next to `maxImageBytes` for instances with a higher limit.

### 3.2 Threads counts emoji as UTF-8 bytes

- Threads: "For the post character limit, emojis are counted as the number of UTF-8 bytes." `BuildCaption` counts runes.
- The EXIF line starts with 📷: 4 bytes, counted as 1. A caption at 498 by `runeLen` can be 501 for Threads.
- **Fix:** a length function per platform. For Threads, an emoji rune counts as its UTF-8 byte length. The names line itself uses no emoji (D1).

## 4. What the platforms allow

Read 2026-10-12.

| Platform | How to mention | Notified | Limits | Source |
|---|---|---|---|---|
| Instagram | `@user` in `caption` | Yes: "Mentioned users receive a notification when the container is published" | 2200 characters, 30 hashtags, **20 @ tags** | IG User Media reference |
| Instagram photo tag | `user_tags=[{username, x, y}]`. `x`/`y` are required for images (0.0–1.0). `username` must be public | Yes | — | same |
| Threads | `@user` in `text`. The publishing reference has no mention parameter and says nothing about @mentions. Meta's Mentions endpoint lists posts where a profile "has been tagged by another Threads profile". Third-party publishers document `@handle` in text as a notifying tag | Not documented by Meta | 500, emoji count as UTF-8 bytes | Threads publishing reference, Threads Mentions |
| Bluesky | Facet `app.bsky.richtext.facet#mention` with the DID over a byte range (UTF-8, start inclusive, end exclusive). "The text is usually a handle, including a '@' prefix" | Yes | 300 graphemes | lexicon `app.bsky.richtext.facet` |
| Mastodon | `@user@domain` in the status. When posting, the server resolves a remote account it does not know yet through WebFinger | Yes | 500 (§3.1) | Mastodon docs |
| Pixelfed | `@user@domain` in the caption (Mastodon-compatible API) | Not checked (§11) | 5000 on pixelfed.de | instance API |

Lookups that work without a token (verified with the owner's own profiles):

- Bluesky: `public.api.bsky.app/xrpc/app.bsky.actor.getProfile?actor=<handle or DID>` returns `did` and the current `handle`. This covers both forms of `bsky.app/profile/…`.
- WebFinger `acct:lna_dev@mastodon.online` and `acct:LNA-DEV@pixelfed.de` both return a `subject`, which is the canonical acct.
- WebFinger with the **profile URL** as `resource` works on Mastodon (200) but **not on Pixelfed** (400). So the acct has to be derived from the URL path first.
- Mastodon's `/api/v2/search` with `resolve=true` needs a user token with `read:search`. Without a token: 401. That is one more reason to use WebFinger, which is public.

## 5. Design

### 5.1 Reading people from the item

New `src/autouploader/people.go`, pure and without network calls:

```go
type Person struct {
    Slug     string
    Name     string
    Autotag  bool              // Attrs["autotag"] == "true", exactly
    Profiles []Profile         // feed order
}
type Profile struct{ Platform, URL string }

func peopleFromEntry(entry *gofeed.Item) []Person
```

- Reads `entry.Extensions["lna"]["model"]` in feed order. No block means nothing to do.
- Trims `name` and folds line breaks into spaces. A block without a name is skipped and logged by slug, because the person cannot be named.
- Unknown `platform` keys are kept but never match a target.
- `media:credit` is ignored (handover §2).

### 5.2 The names line

The names form their own paragraph of the caption. This is **always** written when the item has people, whatever the connection or `autotag` say.

```
With Markus Penninger
With Jane Doe (@janedoe) and Markus Penninger
With Jane Doe (@janedoe), Max Muster and Markus Penninger
```

- Each person is the name, plus ` (<mention>)` when a mention resolved (§5.4).
- The line starts with `With`. The last two people are joined by ` and `, all others by `, `. No serial comma.
- Both words are configurable per connection (§5.8). For German, `Mit … und …`. The defaults are English, like every caption today.
- No emoji, because Threads counts emoji as UTF-8 bytes (§3.2).
- The format is D1. The position in the caption is D2.

New order of the caption:

```
<alt text>          ← includeAltText
<names line>        ← new, protected
<caption>
<EXIF line>
<copyright line>
<hashtags>
```

- The names belong to the description of the photo, so they follow the alt text. The configured caption (usually the "More at …" link) stays the last line of text before EXIF, copyright and hashtags (D2).
- `buildBaseText` joins alt text and caption today (`src/autouploader/caption.go:122`). They become two sections with the names line in between. Without alt text, the names line comes first.

### 5.3 Length budget

The names line is protected: it is never dropped, and it is cut only in step 7, when nothing else is left. Steps, each tried in order until the caption fits:

1. Everything.
2. Drop copyright.
3. Drop EXIF.
4. Drop hashtags one by one from the end.
5. Cut the alt text from the end to whatever is left; if nothing is left, drop it. Then the same for the caption. The alt text goes first because Pixelfed, Mastodon and Bluesky also get it as the image's alt text, and because a caption cut in the middle of its link leaves a broken link.
6. Turn mentions back into plain names, last person first (D7).
7. Last resort: cut the names line (see below). Log it with the slugs of the people affected. The upload goes ahead. At Bluesky's 300 this needs about ten people with long names, so it should not happen in practice.

Steps 1–4 are today's order. Step 5 used to cut alt text and caption as one block from the end, which cut the caption first. Steps 6 and 7 are new.

**How text is cut** (steps 5 and 7). Today the last resort is a hard cut in the middle of a word (`caption.go:107`). Instead, one function `shorten(text, max)` is used for the alt text, the caption and the names line:

1. If a sentence ends (`.`, `!` or `?` followed by a space) within the allowed length, and that keeps at least half of it, cut after the sentence. No `…` is needed.
2. Otherwise cut at the last space that fits, drop trailing punctuation, and append `…`.
3. Only when there is no space at all (one very long word or a URL) cut hard and append `…`.

- `…` is one character and counts against the limit.
- A URL is a single word. So it is either kept whole or dropped, and never left half-cut as a broken link.
- For the names line, the boundary between two people (`, ` or ` and `) is just a space, so the line is cut after a whole name before it is cut inside a name.

The limit check uses the per-platform length function from §3.2.

On Instagram, more than 20 mentions are written as plain names (the 20 @ tags limit). The photo tags (§5.9) follow the same 20 people.

### 5.4 From profile URL to mention

A person gets a mention only if **all** of these hold:

- `tagModels: true` on the connection (D3),
- `autotag` is `true` for the person,
- the person has a usable profile for the target (table below),
- that profile resolves,
- it is not the posting account (§5.5).

Otherwise the person is named without a mention.

| Target | Profile used, in order | From URL to handle | Network | Mention text |
|---|---|---|---|---|
| Instagram | `instagram` | `instagram.com/<user>/`, with or without `www.`. First path segment, `[A-Za-z0-9._]{1,30}`. Reserved segments (`p`, `reel`, `explore`, `stories`) count as invalid | None | `@user` |
| Threads | `threads` (D5) | `threads.net/@<user>` or `threads.com/@<user>` | None | `@user` |
| Bluesky | `bluesky` | `bsky.app/profile/<handle or DID>` | `getProfile` returns DID and current handle | `@handle` plus a mention facet with the DID (§5.6) |
| Mastodon | `mastodon`, then `pixelfed` | Path `/@user` gives `user@host`. `/@user@domain` gives `user@domain`. `/users/user` and `/user` (Pixelfed) give `user@host` | WebFinger `acct:` → `subject` | `@user@domain` from `subject` |
| Pixelfed | `pixelfed`, then `mastodon` | same | same | same |

- **Instagram and Threads:** nothing is checked over the network. Meta has no lookup for arbitrary accounts (`business_discovery` only works for business accounts). A wrong URL gives a dead mention. The author typed the URL, so that is accepted.
- **Fediverse:** WebFinger runs on the host from the URL. Its `subject` is the canonical acct, even if the web host and the account domain differ (handover §5). Mastodon resolves the acct again on its own when the status is posted.
- **Timeouts:** every lookup has a 5-second timeout. A timeout counts as "does not resolve".
- **Caching:** none (D8). A publish makes at most one lookup per person to be tagged, plus one for the own identity. Publishes run a few times a day.

Code: `src/autouploader/mentions.go` chooses the profile, parses the URL and calls the lookups. The lookups go into the API packages, which stay primitive (as `blueskyapi` says in its package doc):

- `blueskyapi.GetProfile(actor)`
- `instagramapi.Me(token)` and `threadsapi.Me(token)` (username, for §5.5)
- `mastodonapi.VerifyCredentials` and `pixelfedapi.VerifyCredentials` (for §5.5)
- a small `src/webfinger` package used by both fediverse paths.

### 5.5 Never mention yourself

Handover §4.3: the owner's own record must not be relied on to keep `autotag: false`. So before the first mention of a publish, the companion fetches the posting account's identity once and drops any mention that resolves to it. The person is still named.

| Target | Own identity | Compared with |
|---|---|---|
| Instagram | `GET /me?fields=username` | parsed username, case-insensitive |
| Threads | `GET /me?fields=username` | parsed username, case-insensitive |
| Bluesky | `getProfile(target.Username)` returns the DID | resolved DID |
| Mastodon / Pixelfed | `verify_credentials` returns `username`, then WebFinger `acct:<username>@<instance host>` | canonical acct, case-insensitive |

- If the own identity cannot be found, **no mentions** are made for that publish. Names are still written. One log line explains why.
- Bluesky: `target.Username` must be a handle, not an email. The local config uses a handle. This avoids a second login just to learn the DID.

### 5.6 Bluesky mention facets

- `CreatePost` gets one more argument, `mentions map[string]string` (mention text such as `@janedoe.bsky.social` → DID). The microblog passes `nil`.
- `ExtractFacets` finds each mention text in the final text and adds a `#mention` facet with byte offsets. Go's `strings.Index` returns byte offsets, which is what the lexicon wants. Hashtag and link facets stay as they are.
- The offsets are computed on the final text that is posted. So the length budget (§5.3) can never move them.
- Visible text: `Jane Doe (@janedoe.bsky.social)`, the same form as on every other platform (D6).

### 5.7 Failures

| Case | Effect |
|---|---|
| No `lna:model` on the item | Caption as today |
| Person without a profile for the target, or without consent | Plain name |
| URL does not parse, lookup fails or times out | Plain name. One log line: `autouploader: <guid> on <platform>: model <slug> not tagged: <reason>` |
| Mention resolves to the posting account | Plain name. Log line as above |
| Own identity unknown | All names plain. One log line |
| Names line alone over the limit | Names line cut with `…` (§5.3 step 7). Log line with the slugs |
| Instagram rejects the container while `user_tags` is set | One more try without `user_tags`; caption mentions stay (§5.9). Log line with the slugs |

- Log lines name the **slug**, never the name (Goal 5).
- `upload_attempts` stays unchanged. A missing mention is not a failure.

### 5.8 Configuration

```yaml
connections:
  - name: instagram
    sourceName: Photo RSS
    targetName: main-instagram
    caption: "More at example.com or the link in my profile"
    tagModels: true   # new, default false
    modelsPrefix: "Mit"   # new, default "With"
    modelsAnd: "und"      # new, default "and"
```

- `tagModels bool` with `yaml:"tagModels,omitempty"` on `Connection`, next to `IncludeAltText`. On Instagram it turns on both the caption mention and the photo tag (§5.9).
- `modelsPrefix` and `modelsAnd` are strings with `omitempty`. Empty means the English default.
- Names have no switch (Goal 1).
- README and `config.yaml.example` document the three fields, and also `includeAltText`, which is missing there today.

### 5.9 Instagram photo tags

On Instagram a person who gets a caption mention also gets a photo tag (D4). Only the photo tag puts the post in the person's "Tagged" tab, and only the photo tag can be removed by the person.

- **Who:** exactly the people who got an Instagram mention in §5.4, including the self-guard (§5.5) and the cap of 20. People named without a mention get no tag.
- **Request:** `CreateMediaContainer` gets one more argument, `userTags []UserTag` (`nil` means none). It is sent as `user_tags`, a JSON array such as `[{"username":"janedoe","x":0.5,"y":0.5}]`. The username has no `@`. The autouploader is the only caller (`src/autouploader/instagram.go:23`).
- **Position:** the feed has no positions, so they are fixed:
  - one person: the centre, `x=0.5, y=0.5`;
  - several people: evenly spread along the middle row, in feed order, `x=(i+1)/(n+1), y=0.5`. Two people get `0.33` and `0.67`.
  - Why the centre: in most portraits the person stands in the middle, so the tag points roughly at them. Bottom centre (`0.5/0.9`) would point at the feet or the ground. Instagram shows tags only on tap, so a tag never covers the photo permanently.
- **Failure:** what Instagram does with a private or missing account in `user_tags` is not checked (§11). So if creating the container fails with a 4xx other than 429 while `user_tags` is set, the companion tries **once more without** `user_tags`. The caption mentions stay. Only if that also fails does the upload fail, as today.
- **Notifications:** the person gets one for the tag and one for the mention. Whether Instagram merges them is not checked (§11). This is accepted.

## 6. Data flow in `Publish`

```
getEntryToPublish
→ peopleFromEntry(entry)                      // pure
→ resolveCredits(people, connection, target)   // network only if tagModels and someone has autotag
→ BuildCaption(..., credits, ...)              // names line + budget
→ publish<Platform>Entry(..., caption, credits) // Bluesky: facets, Instagram: user_tags
```

`BuildCaption` gets the credits (name plus optional mention text) as a new parameter. Today's tests stay valid when the slice is empty.

## 7. Tests

- `peopleFromEntry`: the handover §2 fixture, several people in feed order, `autotag` missing / `"TRUE"` / `"false"` (only `"true"` counts), a block without a name, an item without blocks.
- URL parsing, table-driven per platform: `www.` or not, trailing slash or not, `threads.net` and `threads.com`, `bsky.app/profile/did:plc:…`, `/@user@otherdomain`, Pixelfed `/user`, reserved Instagram paths, junk URLs.
- `BuildCaption`:
  - names present without mentions;
  - Bluesky 300 with a 404-character alt text: the alt text is cut, the names and the caption stay whole;
  - alt text gone and still too long: the caption is cut next;
  - step 6: mentions fall back to plain names;
  - step 7: the names line is cut with `…` only when alt text and caption are gone and every mention is a plain name;
  - `shorten`: cut after a sentence, cut at a space with `…`, hard cut of one long word, a URL kept whole or dropped, the result never longer than `max`;
  - the new section order;
  - the names line: `With A`, `With A and B`, `With A, B and C`, and with `modelsPrefix`/`modelsAnd` set;
  - more than 20 mentions on Instagram.
- Instagram photo tags: positions for 1, 2 and 3 people, the `user_tags` JSON, the retry without `user_tags` after a 4xx (not after a 429), no tags for people without a mention.
- Bluesky facets: byte offsets with non-ASCII before the mention (`Jöhn Döe (@x.bsky.social)`), mention and hashtag facets side by side.
- Resolver with `httptest`: WebFinger hit and miss, getProfile, timeout, the self-guard dropping a mention, own identity unknown.
- Threads length function: emoji counted as bytes, umlauts as one.

## 8. Build order

| Stage | Content | Deployable alone | State |
|---|---|---|---|
| 0 | §3.1 Mastodon limit 500, §3.2 Threads length function | Yes | Done (§12) |
| 1 | §5.1–5.3: read people, names line, length budget. No tagging | Yes. Before Home-Page deploys: no change. After: names on the 16 photos with people (handover §6) | Done (§12) |
| 2 | §5.4, §5.5, §5.7, §5.8: `tagModels`, resolver, self-guard, mentions for Instagram, Threads, Mastodon, Pixelfed | Yes. With today's data nothing gets tagged, because no one has `autotag: true` | Done (§12) |
| 3 | §5.6: Bluesky mention facets | Yes | Done (§12) |
| 4 | §5.9: Instagram photo tags | Yes | Done (§12) |
| 5 | README, `config.yaml.example` | — | Done (§12) |

## 9. Decisions

| # | Question | Decision | Alternative not taken |
|---|---|---|---|
| D1 | Format of the names line | `With A (@a), B and C`. English by default, both words configurable per connection (§5.2, §5.8) | `Model: A` / `Models: …`, or an emoji such as `📸` |
| D2 | Position | between alt text and caption (§5.2) | After the caption, before EXIF. Or at the very top, so Instagram shows it before "… more" |
| D3 | Who turns tagging on | both, `tagModels` on the connection **and** `autotag` from the feed | `autotag` alone. Then every connection tags as soon as Home-Page allows it |
| D4 | Instagram photo tag (`user_tags`) | both, caption mention and photo tag. Fixed position in the centre, several people spread along the middle row (§5.9) | Caption mention only. Or bottom centre (`0.5/0.9`) |
| D5 | Fallback between platforms | fediverse yes (Mastodon ↔ Pixelfed, same platform first). Instagram → Threads no | Threads also uses the Instagram profile, because Threads usernames are Instagram usernames |
| D6 | Bluesky visible text | `Jane Doe (@handle)` with a facet over `@handle` | Facet over `Jane Doe`, no handle shown. Saves characters, but whether it notifies is not checked |
| D7 | Over the limit: mentions or alt text and caption first | keep mentions; cut alt text and caption first (§5.3 step 5 before 6), at a sentence or word boundary with `…`, never in the middle of a word | Turn mentions into plain names before cutting the alt text |
| D8 | Caching lookups | none | Table keyed by profile URL (allowed by handover §4.2) |
| D9 | The owner on his own photos | named like anyone else (`With Lukas Nagel`), as the site does. Never mentioned (§5.5) | Leave out people whose profile resolves to the posting account. That needs lookups even with tagging off |

## 10. Not in this concept

- Instagram `user_tags` with real positions. That would need x/y per person in Home-Page.
- Instagram `collaborators` (the invited account has to accept).
- Admin UI: showing who was tagged on which post.
- Microblog and backfill. Only the gallery autouploader posts photos with people.
- Bridged accounts (e.g. Bluesky ↔ fediverse bridges).

## 11. Not checked yet

- **Threads:** whether `@user` in `text` sent through the API creates a real mention with a notification. Meta does not document it. Only third parties do.
- **Pixelfed:** whether a remote `@user@domain` in a caption sent through the API is resolved, linked and delivered. Also whether a Mastodon profile can be mentioned from Pixelfed (D5).
- **Instagram:** what happens with a mention of an account that does not exist (error or plain text). What happens with a private or missing account in `user_tags` (error code, or whether the tag is just dropped). Whether a tag and a mention of the same person send one notification or two.
- **Live test of the tagging path.** Today no one has `autotag: true`, and the owner is blocked by §5.5. A second test account per platform is needed, plus a local feed fixture as the datasource. The local config already has a Bluesky test account (`bskytestinglna.bsky.social`).
- The production config (which Mastodon instance, whether the Bluesky username is a handle).

## 12. Implementation log

What was built per stage, and where the code differs from the text above. Nothing is committed yet.

### Stage 0 (2026-10-12)

- `BuildCaption` takes a `CaptionLimits` struct instead of `maxLen, maxHashtags`: `MaxLen`, `MaxHashtags` and `Len`, the length function (`src/autouploader/caption.go`). Stages 1–4 extend this struct instead of adding parameters.
- `captionLimitsFor(target)` builds it from `captionLimitFor`, `maxHashtagsFor` and `captionLenFor` (`src/autouploader/main.go`).
- §3.1: `captionLimitFor("mastodon")` is 500. The per-target override exists: `maxCaptionLength` on `Target` (`src/config/models.go`), 0 means the platform default. It applies to every platform.
- §3.2: `threadsLen` counts every rune as one, except emoji runes, which count as their UTF-8 bytes. `isEmojiRune` errs towards counting too much: every Unicode "other symbol" (`So`: 📷, ©, ★, flags), plus skin tone modifiers, ZWJ, keycap, variation selectors and tag characters. So `©` counts as 2 on Threads. Only Threads uses it.
- The last-resort cut (step 5 of today's order) now shortens rune by rune until the length function is satisfied, so an emoji in the base text cannot push a Threads caption over 500. Stage 1 replaces it with `shorten` (§5.3).
- Tests: `TestCaptionLimitsFor`, `TestThreadsLen`, `TestBuildCaption_ThreadsCountsEmojiBytes` (`src/autouploader/caption_test.go`).

### Stage 1 (2026-10-12)

- `src/autouploader/people.go`: `Person`, `Profile` and `peopleFromEntry` as in §5.1. Platform keys are also trimmed and lowercased. `Credit{Slug, Name, Mention}` is the "credit" of §6. `plainCredits` names everyone without a mention; Stage 2 replaces it with the resolver.
- `BuildCaption(conn, entry, feed, imageBytes, credits, limits)` returns `Caption{Text, Unmentioned, NamesCut}` instead of a string. `Unmentioned` holds the slugs whose mention step 6 turned into a plain name, `NamesCut` the slugs step 7 no longer names in full. `Publish` logs both with the platform (`logCaptionCuts`, `src/autouploader/main.go`):
  - `autouploader: <guid> on <platform>: model <slug> not tagged: caption too long`
  - `autouploader: <guid> on <platform>: names line cut to fit, not named in full: <slugs>`
- `buildBaseText` is gone. The sections live in `captionParts` and are joined in the order of §5.2.
- `modelsPrefix` and `modelsAnd` are on `Connection` already (§5.8 lists them under Stage 2), because the names line needs them. `tagModels` follows in Stage 2.
- `shorten` (§5.3), decisions taken while building it:
  - "keeps at least half of it" means half of the allowed length.
  - A sentence ends at `.`, `!` or `?` followed by any whitespace, not only a space.
  - Trailing punctuation removed before `…`: `, ; : . ! ? - – —`.
  - A cut at a word boundary can leave a joining word: `See the whole series and…`. That is what the rule gives, and it is accepted.
  - A word containing `://` counts as a URL. When not even the first word fits and it is a URL, the text is dropped instead of cut hard.
- Step 7 does not cut the names line at the last space. `cutNamesLine` first drops whole people from the end, `With Jane Doe, Max Muster…` (the remaining people joined by `, `, no `and`). Only when not even the first person fits is the line cut with `shorten`. A plain last-space cut could cut inside a two-word name although a whole name would fit, which §5.3 rules out.
- Steps 6 and 7 run with alt text and caption already gone (step 5). Room freed by step 6 is not filled with alt text or caption again. This follows "each tried in order until the caption fits".
- After step 5 has dropped both, the caption is checked once more, so a names line that fits alone is not cut.
- An item without people whose caption cannot be kept even shortened (a single URL longer than the limit) now gets an empty text. Before, the base text was cut hard.
- **The live feed carries `lna:model` now.** Measured later on 2026-10-12: 16 of 589 items, all `markus-penninger` with `autotag="false"` and no profiles. Built against it with alt text: Bluesky 300 and Threads 500 stay within the limit for every item, the configured caption is always whole, 17 alt texts are cut, all of them after a sentence. Example:

  ```
  A bearded man with glasses and a top knot sits on an ornate green cast-iron park bench in Stockholm, looking to the side in the summer sun. He wears a white T-shirt and navy striped track pants.

  With Markus Penninger

  More at https://lna-dev.net/en/gallery/
  ```

  So deploying Stage 1 names Markus Penninger on these 16 photos at once.
- Tests: `TestPeopleFromEntry` (handover fixture, `&amp;` decoding, whitespace folding, `autotag` `"TRUE"`/`"false"`/missing, a block without a name, an item without blocks), `TestPlainCredits`, `TestNamesLine`, `TestBuildCaption_Names` (section order, `Mit … und …`), `TestBuildCaption_BlueskyCutsAltTextFirst`, `TestBuildCaption_CutsCaptionAfterAltText`, `TestBuildCaption_MentionsFallBackToNames`, `TestBuildCaption_NamesLineCutLast`, `TestShorten` (including "never longer than the limit" for every limit with both length functions).

### Stage 2 (2026-10-12)

- `tagModels` on `Connection` (`src/config/models.go`), default false.
- New `src/webfinger`: `Lookup(ctx, base, acct)` asks `base/.well-known/webfinger?resource=acct:<acct>` and returns the `subject` without `acct:`. 404 and 410 give `ErrNotFound`. A subject that is not an acct is an error. **Pixelfed answers an unknown account with 400**, not 404 (measured against `pixelfed.de`). That ends the same way: plain name, the status in the log line.
- Lookups in the API packages: `instagramapi.Me`, `threadsapi.Me` (`GET /me?fields=username`), `mastodonapi.VerifyCredentials`, `pixelfedapi.VerifyCredentials`. Unlike the older functions in these packages they take a `context.Context`, so the caller sets the 5-second timeout (`lookupTimeout`).
- `src/autouploader/mentions.go`:
  - `resolveCredits(guid, people, conn, target)` replaces `plainCredits` in `Publish`. Without `tagModels`, or on a platform without mentions (Bluesky until Stage 3), it names everyone and makes no network call.
  - `mentionTarget` describes a platform: usable profile keys in order, mention cap, `parse` (URL → account, no network), `resolve` (account → mention text and identity) and `self` (own identity).
  - Profile choice: the target's platform order first (Mastodon: `mastodon`, `pixelfed`; Pixelfed the other way round), then feed order. The first profile that **parses** is used. Only that one is looked up; if it does not resolve, the person is named without a mention and no other profile is tried. So there is at most one lookup per person (§5.4).
  - The own identity is fetched only when at least one person has consent and a usable profile. Without it, no mentions for the publish.
  - Instagram's cap of 20 is applied here: people after the 20th mention are named without one and logged.
  - Self-guard and comparisons are case-insensitive. Fediverse: own identity = `verify_credentials` username, then WebFinger `acct:<username>@<instance host>` on the instance; the person's identity is the WebFinger `subject`.
- URL parsing, as §5.4, with these details:
  - Only absolute `http`/`https` URLs. Hosts are compared in lowercase.
  - Instagram and Threads usernames: `[A-Za-z0-9._]{1,30}`. Instagram reserved first segments: `p`, `reel`, `explore`, `stories`. Threads: `threads.net` or `threads.com`, with or without `www.`, path `/@user`.
  - Fediverse usernames: `[A-Za-z0-9_][A-Za-z0-9._-]*`. The Pixelfed form `/user` accepts any single path segment; a non-profile path such as `/explore` then fails at WebFinger and the person is named without a mention.
- Log lines (slug only, never the name):
  - `autouploader: <guid> on <platform>: model <slug> not tagged: <reason>`, reason being the lookup error, `profile is the posting account` or `more than 20 mentions`;
  - `autouploader: <guid> on <platform>: no mentions, own identity unknown: <error>`.
  - People without consent or without a usable profile are not logged (§5.7: plain name).
- Live check, read-only, against the owner's own profiles: `https://mastodon.online/@lna_dev` → `@lna_dev@mastodon.online`, `https://pixelfed.de/LNA-DEV` → `@LNA-DEV@pixelfed.de`.
- Tests: `TestParseInstagramURL`, `TestParseThreadsURL`, `TestParseFediverseURL`, `TestResolveWith` (same platform first, no consent, no profile, lookup failure, self-guard, own identity unknown, own identity not fetched when no one can be tagged, one lookup per person), `TestResolveWith_MaxMentions`, `TestResolveCredits_TagModelsOff`, `TestFediverseLookups` (`src/autouploader/mentions_test.go`); `src/webfinger/webfinger_test.go` (hit, other domain, miss, non-acct subject, timeout); `TestMe` in `instagramapi` and `threadsapi`; `TestVerifyCredentials` in `mastodonapi` and `pixelfedapi`.

### Stage 3 (2026-10-12)

- `blueskyapi.GetProfile(ctx, actor)` asks the public AppView (`PublicURL`, `https://public.api.bsky.app`, overridable in tests) and returns DID and current handle (`src/blueskyapi/profile.go`).
- Bluesky is a `mentionTarget` now (`src/autouploader/mentions.go`):
  - `parseBlueskyURL`: `bsky.app/profile/<handle or DID>`, with or without `www.`, exactly these two path segments. Handle: a domain name. DID: `did:plc:…` or `did:web:…`.
  - `blueskyMention`: `getProfile` on the handle or DID from the URL. The mention shows the **current** handle, so an old handle or a DID in the URL still gives `@<handle>`. A profile whose handle is `handle.invalid` counts as "does not resolve".
  - `self`: `getProfile(target.Username)` → DID. An email as username fails there, and the publish goes out without mentions, with one log line (§5.5).
- `Credit.Account` holds who a mention resolved to: the DID on Bluesky, the username on Instagram and Threads, the canonical acct on the fediverse. Stage 4 uses it for `user_tags`.
- `blueskyapi.CreatePost(…, mentions map[string]string)` and `ExtractFacets(text, mentions)`: the mention facets come first, then hashtags and links as before. Offsets are byte offsets in the final text. Every occurrence of the mention text gets a facet, except where it is only the start of a longer handle (`@x.bsky.social` in `@x.bsky.socialite` or `@x.bsky.social.uk`). A `.` after the mention followed by a space or the end still counts as a boundary. `ExtractFacets` had no callers outside `blueskyapi`, so its signature changed instead of adding a second function.
- `publishBlueskyEntry` gets the credits and passes `blueskyMentions(credits)`: mention text → DID. A mention that step 6 turned into a plain name is not in the text, so it gets no facet. The microblog passes `nil`.
- Live check, read-only: the owner's profile from the live feed, `https://bsky.app/profile/lna-dev.net`, resolves to `@lna-dev.net` / `did:plc:2svaidgd2rfpeu5u7kvau3ci`. The local test account `bskytestinglna.bsky.social` resolves to `did:plc:wvdo7nvvxigenixfiwyafvll`, so the self-lookup works with the local config. The owner has `autotag="false"`, so he is not even looked up during a publish.
- Tests: `TestParseBlueskyURL`, `TestBlueskyLookups` (old handle and DID give the current handle, `handle.invalid`, unknown profile, self), `TestBlueskyMentions` (`src/autouploader/mentions_test.go`); `TestExtractFacets_Mentions` (`Jöhn Döe (@x.bsky.social)`: byte offset 12, mention and hashtag facets side by side), `TestExtractFacets_MentionBoundaries`, `TestGetProfile` (`src/blueskyapi/posts_test.go`).

### Stage 4 (2026-10-12)

- `instagramapi.CreateMediaContainer(…, userTags []UserTag)` sends `user_tags` as a JSON array when the slice is not empty (`src/instagramapi/media.go`). `UserTag{Username, X, Y}`.
- A rejected container create returns `*instagramapi.StatusError{Op, StatusCode, Body}` instead of a plain error. The message is the same as before (`instagram create container status <code> body: <body>`). 429 is still `ErrRateLimited`.
- `src/autouploader/instagram.go`:
  - `instagramUserTags(credits)` tags everyone with a mention, username from `Credit.Account`. Positions as §5.9, rounded to two decimals: one person `0.5`, two `0.33`/`0.67`, three `0.25`/`0.5`/`0.75`; `y` is always `0.5`.
  - The tags are built from the credits after the resolver (§5.4, with self-guard and the cap of 20), not from the caption after the length budget. If step 6 ever turned an Instagram mention into a plain name, the person would still get the photo tag. At Instagram's 2000 this does not happen with at most 20 mentions.
  - `createInstagramContainer`: with tags set, a `StatusError` with 400–499 gets **one** more try without `user_tags`. 429 and 5xx are not retried. The caption mentions stay. Log line: `autouploader: <guid> on instagram: photo tags rejected, retrying without them: <slugs>: <error>`.
- Tests: `TestCreateMediaContainerUserTags` (the `user_tags` JSON, nothing sent for `nil`), `TestCreateMediaContainerStatusError` (`src/instagramapi/media_test.go`); `TestInstagramUserTags` (1, 2 and 3 people, no tag without a mention), `TestCreateInstagramContainer_Retry` (accepted, 4xx retried without tags, 429 and 5xx not retried, no `user_tags` without mentions) (`src/autouploader/instagram_test.go`).

### Stage 5 (2026-10-12)

- README: the feature list names the people in photos (and Threads, which was missing there). *YAML config* lists `maxCaptionLength`, `includeAltText`, `tagModels`, `modelsPrefix` and `modelsAnd`. New section *People in photos*: the names line, the length budget in one sentence, when someone is mentioned, how each platform resolves a profile, the self-guard (Bluesky username must be a handle), and links to this concept and the handover.
- `src/data/config.yaml.example`: `maxCaptionLength` commented out on the Mastodon target; `includeAltText`, `tagModels`, `modelsPrefix` and `modelsAnd` commented out on the Instagram connection, valid on every connection.

### State after all stages

- `go build ./...` and `go test ./...` pass, except `webpush`, whose `go vet` check (run by `go test`) fails on `log.Printf("%s", sub.ID)` with a `uint` in `webpush/notify.go:27,29`. That file is not touched here and fails the same way at `f42a7aa`.
- Deploying changes these things at once, with today's feed and `tagModels` off everywhere:
  - Mastodon captions are capped at 500 (§3.1), Threads counts emoji as bytes (§3.2).
  - The 16 photos with people get the names line (`With Markus Penninger`, `With Lukas Nagel`).
  - Over the limit, the alt text is cut at a sentence or word before the caption, instead of cutting both together in the middle of a word.
- Turning on `tagModels` for a connection makes network calls only when someone in the photo has `autotag="true"` and a profile for the target. Today no one has.
- Next: the live tests from §11, with a test account per platform and a local feed fixture as the datasource.
