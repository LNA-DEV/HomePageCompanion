# Concept: Autoposting — findings and a new admin UI

**Status:** **decided, not started.** Every decision in §9 was taken on 2026-10-20. Everything, the fixes of §3 included, is built in the stages of `docs/concepts/modular-architecture.md` §7; today's code is not changed ahead of the rebuild (D7).
**Scope:** this repository only.
**Measured:** 2026-10-20, against the code at `8c01664`, in a local run (§2.1). The live gallery feed was not reachable from the sandbox (§11).

The autoposting pages show database tables instead of posts. Whoever wants to know where a photo went out and how many likes it got has to switch between four pages, and several of them show something wrong. A local run with 36 photos on 5 targets reproduced 11 bugs; 9 more follow from the code. Three are critical: a one-minute feed outage stops the whole companion, and a fresh installation does not start. Announcing blog posts, the second job of autoposting, does not work today at all (§5). This concept puts the single post at the centre, whether photo, blog post or microblog post: one table with a column per target and status and likes in every cell, a detail view per post, a schedule and a list of open problems.

## 1. Goal

1. One view answers, per post: where is it published, where not and why, how many likes.
2. Photos keep rotating from the archive; new blog posts are announced once, soon after they appear.
3. What the UI shows is true: no stale failures, no target marked down for one bad image, search over everything.
4. Neither a feed nor a single item can stop the companion.

## 2. Today

### 2.1 How it was measured

- Backend built from `8c01664`, admin UI with `vite dev`, both local. Nothing in the repository changed.
- A generated feed of 36 test images shaped like the gallery feed (`media:content`, alt text, categories), served locally.
- Seed data: 117 publications on 5 targets, 9 failed attempts (rate limit, expired token, network, image too large, 503), likes per publication, 120 website likes, 4 webmentions, 2 publications of photos no longer in the feed, one double post.
- Playwright with Chromium through Dashboard, Feeds, feed detail, Publications, Uploads, Connections and Interactions, desktop and mobile, with console and network recorded. The console showed no JavaScript errors.
- Under `127.0.0.1` instead of `localhost`, every POST fails with `403`, because CORS admits only the domain and `localhost`. Harmless in production as long as the UI runs under the configured domain.

### 2.2 The pages

| Page | Shows | What is missing |
|---|---|---|
| Feeds → feed detail | photos with platform badges, failure badges, likes as one sum | comes closest to the question but hides under "Feeds"; missing platforms are invisible; likes per platform only in a tooltip |
| Publications | one row per photo and platform | 117 rows for 36 photos; "Delete" on every row; the upload trigger lives here of all places |
| Uploads | one row per attempt | shows long-resolved failures exactly like open ones |
| Connections | the config, read-only | no next run, no next photo, no action |
| Interactions | one row per photo and platform with likes | rows again instead of photos, and a different like sum than the feed page |
| Dashboard | target status and counts | target status mixes account problems with problems of single photos |

Every page loads its list and then fetches titles and thumbnails through `feed-items/lookup`. Filters and search run in the browser over whatever is loaded (#6, #7).

## 3. Findings (Stage 0)

"Test" means reproduced in the local run; "code" means derived from the code, not triggered.

| # | Severity | Finding | Where | Evidence |
|---|---|---|---|---|
| 1 | critical | If the feed cannot be fetched, the whole companion exits, at startup as well. The reason reaches no log | `src/inventory/imageRssToDb.go:17` (`log.Fatalf`) | test: feed server stopped, the process was gone at the next fetch; a restart without the feed fails too |
| 2 | critical | The feed is fetched every minute instead of every hour. Together with #1, a one-minute outage is enough | `src/main.go:93`: `"0 * */1 * * *"`, robfig/cron with a seconds field | test: fetches at 16:21:00, 16:22:00, 16:23:00 … in the access log |
| 3 | critical | A fresh installation does not start: migration 001 runs before the tables exist | `src/database/migration_001_itemname_to_guid.go`, `src/main.go:54` | test: empty database, `no such table: auto_upload_items`, process exits |
| 4 | wrong | "Failed" stays on a photo that has since been published on that platform | `enrichFeedItems` in `src/admin/handlers.go` | test |
| 5 | wrong | One content error (image too large) marks the whole target "Down"; a "Healthy" target keeps showing its old error in red | `GetTargetHealth` in `src/admin/handlers.go` | test |
| 6 | wrong | Search covers the loaded page only: "Libelle" finds 1 of 3 photos, the footer still says "Showing 1 – 20 of 36". The same on the Uploads page | `web/src/routes/feeds/[id]/+page.svelte`, `web/src/routes/uploads/+page.svelte` | test |
| 7 | wrong | Uploads: after choosing a platform, the platform menu offers only that one | `web/src/routes/uploads/+page.svelte`, options built from the loaded rows | test: afterwards only `All` and `threads` |
| 8 | wrong | "Trigger Upload" reports a green "Upload triggered" even when the run fails, and never says which photo was chosen | `web/src/routes/publications/+page.svelte`, `POST /api/upload/:connection` | test: Mastodon run failed, message green |
| 9 | wrong | Likes are counted differently: the feed page includes website likes, Dashboard and "Top Items" do not | `enrichFeedItems` versus `GetStats` and `GetInteractionsSummary` | test: one photo shows 305 likes on the feed page and 304 in Interactions |
| 10 | risky | "Delete" on Publications removes only the record, so the autouploader posts the photo again on its next run; the dialog does not say so | `getAlreadyUploadedItems` in `src/autouploader/main.go` | code |
| 11 | risky | Platforms skipped by routing tags are not recorded: the photo stays "open", is chosen again and skipped again. Failed photos are never retried on purpose | `src/autouploader/main.go:58-66`, `getEntryToPublish` | code |
| 12 | risky | Publications are stored per platform, not per target. With two Mastodon accounts, a photo counts as done on both after the first | `src/models/autouploadItem.go`, no `TargetName` | code |
| 13 | minor | The HTTP status of a failed attempt is never stored | `RecordAttempt(…, 0)` in `src/autouploader/main.go` | code |
| 14 | minor | Publications loads every record without paging (photos × platforms) and sends every ID to `feed-items/lookup`. The live feed had 589 items on 2026-10-12 (`model-tagging.md`) | `web/src/routes/publications/+page.svelte` | test: 117 rows on one page |
| 15 | minor | The "Logs" menu entry is cut off at a window height of 900 px | `web/src/lib/components/Sidebar.svelte` | test |

The findings for blog posts (B1–B5) are in §5.1.

## 4. Design: the post at the centre

Five menu entries (Feeds, Publications, Uploads, Connections, Interactions) become three pages of the `autoupload` module: **Posts**, **Schedule** and **Problems**. A post is a photo, a blog post (§5) or a microblog post. Managing feeds stays with the `rss` module.

### 4.1 Posts (start page)

- One row per post, one column per target instance (`mastodon:main`, `lemmy:main` …), likes on the right. Photos show a thumbnail, blog and microblog posts their title or first line.
- Every cell is in one of five states: **published** (with likes; a click opens the post), **failed** (with a short reason), **scheduled next** (with the time), **skipped** (by routing tag, or because the target needs an image) or **open**.
- Filters: All · Photos · Blog · Microblog · Not everywhere yet · With problems · No longer in feed; plus target, tag and a search over all posts. 50 posts per page; paging, filters and search run on the server.
- Sorted by date, likes or last posted. On a phone, rows become tiles: image or title large, a status dot per target below.
- **Microblog posts** keep their editor and their own `publishTo` in the `microblog` module. They reach this table through the events `social.post.published/v1` and `social.post.failed/v1`, which carry the kind `note`, the title and the link. "Retry" and "Post now" on such a row call the microblog module's own action (D5).
- **Posts no longer in the feed** (renamed or removed photos) stay, marked "no longer in feed", and keep counting likes (D6).

Mock-up with sample data:

```
[All 48] [Photos 36] [Blog 12] [Not everywhere yet 22] [With problems 4]   [Search      ]

Post                         Pixelfed  Bluesky  Instagram  Mastodon  Threads    Likes
Alpenglühen am Watzmann      ◔ 18:00   –        –          ✕ 503     –              –
  Photo · 2026-10-18
Eisvogel am Teich            ● 31      ● 12     ✕ limit    ● 18      ✕ token       61
  Photo · 2026-09-30                                                         +7 website
Unterwegs auf den Lofoten    ○ image   ● 24     ○ image    ● 33      ● 17          74
  Blog · 2026-06-02                                                          +5 website
Feuersalamander nach Regen   ● 22      ○ tag    ● 140      ● 25      ● 38         225
  Photo · 2026-05-17                                                         +3 website

● published, number = likes   ✕ failed with reason   ◔ scheduled next   ○ skipped   – open
```

New targets, services included, appear as further columns. With many targets the table scrolls sideways and the post column stays.

### 4.2 Post detail (side panel)

- The photo, or for a blog post title, excerpt and cover image. Below, one card per target: status, date, link, likes with a small history.
- A preview per target through `preview` (`modular-architecture.md` §5.4): the caption with cuts and mentions for photos, text and link card for blog posts, as the platform will show them.
- Actions per target: "Post now", "Skip for this target", "Mark as posted" (with URL, for posts made by hand) and "Allow reposting". The last replaces "Delete" and says plainly that the post will go out again.
- The history of every attempt for this post.

### 4.3 Schedule

- Per connection: source → target, strategy (rotation or announcement, §5.2), next run in local time, which post would come next (with preview), last run with its result.
- For announcements it shows what is waiting, e.g. "1 new blog post, goes out at 08:00".
- "Run now" shows the real result afterwards: which post, link or error. It no longer just says "triggered" (#8).

### 4.4 Problems

- Only open cases: failures with no later success for the same post and target.
- Grouped by cause: **account** (token expired, no permission), **content** (image too large, format) and **transient** (network, 5xx, rate limit), with "retry all" per group.
- A "Log" tab holds every attempt, like today's Uploads page, with server-side filters.

### 4.5 Dashboard and terms

- The module provides three widgets: "Targets" (status from account and connection problems only, #5), "Top posts" and "Open problems: n".
- "Likes" means platform likes everywhere. "Website likes" are always shown separately next to them (#9).

## 5. Not only photos: announcing blog posts

Autoposting has two jobs: showing photos from the archive again and again, and announcing new blog posts once. They differ in what is chosen, when and with what content. Today's code knows only the first, so the strategy becomes a setting of each connection.

### 5.1 What is missing today

| # | Finding | Where | Evidence |
|---|---|---|---|
| B1 | Text feeds (`itemType: text`) never reach the inventory; a blog appears on no admin page | `src/inventory/main.go`, only `case "image"` | code |
| B2 | Bluesky, Pixelfed and Instagram read `entry.Image.URL` without a nil check. An item without an image panics; robfig/cron recovers it, but "Trigger Upload" runs `Publish` in a goroutine without `recover` and stops the companion | `src/autouploader/bluesky.go:21`, `pixelfed.go:13`, `instagram.go:72`; `go autouploader.Publish` at `src/main.go:207` | code |
| B3 | Threads refuses items without an image, although the Threads API can post text | `src/autouploader/threads.go:23` | code |
| B4 | "The date closest to today, ignoring the year" would announce a years-old article and a new one only a year later | `getEntryToPublish` in `src/autouploader/main.go` | code |
| B5 | The caption is a fixed text; there are no placeholders for title, excerpt or link. The EXIF and names lines are made for photos | `conn.Caption` in `src/autouploader/caption.go` | code |

### 5.2 Two strategies per connection

| | Rotation (photos, as today) | Announcement (blog posts) |
|---|---|---|
| Which post | one whose date is closest to today, ignoring the year | every new post exactly once, oldest first |
| When | on a schedule, e.g. daily at 18:00 | soon after it appears, triggered by `feed.item.discovered/v1`; optional delay and time window |
| On first setup | every photo is a candidate | older posts count as known and are never announced (`since`) |
| Content | image, alt text, caption, EXIF, people | title, excerpt, link, hashtags from the categories; cover image if there is one |
| Targets that need an image | – | Instagram and Pixelfed post with the cover image; without one they are skipped with the reason "needs an image" |
| Repeat | follows from the rotation | optional per connection, "share again after n days", off by default (D4) |

```yaml
modules:
  autoupload:
    instances:
      blog-mastodon:
        source: rss:blog
        target: mastodon:main
        strategy: announce        # rotate | announce
        delay: 10m                # after the post appears
        window: "08:00-21:00"     # post only in this window
        since: 2026-10-20         # never announce older posts
        caption: "{{.Title}}\n\n{{.Summary}}\n\n{{.Link}}"
        reshareAfter: 7d          # optional
```

### 5.3 What changes in the capabilities

- **`feed.source/v1`:** every item has a kind, `photo` or `article`, plus title, excerpt, link, categories, date and an optional image. `rss` detects the kind and lets the config fix it per feed.
- **`social.publisher/v1`:** images become optional and a link card is added (URL, title, description, preview image). `describe` reports what the target can do: `requiresImage` for Instagram and Pixelfed; `linkCard: auto` for Mastodon and Threads, which build the card from the link; `linkCard: manual` for Bluesky, where the sender has to supply title, description and preview image.
- **`autoupload`:** skips targets with `requiresImage` when an article has no cover image and records that as `skipped` with the reason. Caption templates know `{{.Title}}`, `{{.Summary}}`, `{{.Link}}` and `{{.Hashtags}}` for both kinds. Today's cutting order stays, but the link is never cut.

## 6. Backend

Each page needs exactly one state per post and target. Today it has to be pieced together from three tables; the module stores it directly.

| Today | New in the `autoupload` module |
|---|---|
| `auto_upload_items` per platform (`Platform`, `ItemID`) | `publications` per post and target instance, with status `published`, `skipped` or `manual`, URL and time (#11, #12) |
| `upload_attempts` without a link to the publication, HTTP status always 0 | attempts with target instance, HTTP status and cause (`account`, `content`, `transient`). A failure is open until a later attempt for the same post and target succeeds (#4, #13) |
| `interactions` per platform and target, current value only | likes per publication through `social.interactions/v1`, plus a daily value for the history |
| routing skip only in the log | stored as a publication with status `skipped` |
| 3 to 4 requests per page, list plus `feed-items/lookup` | one request `GET /api/b/autoupload/items?filter=&q=&target=&sort=&page=` returning posts with all cells; search and filters server-side (#6, #7, #14) |
| `POST /api/upload/:connection` without a result | `POST /api/b/autoupload/connections/<name>/run` starts a job, `GET …/runs/<id>` returns post, target and result (#8) |
| `DELETE /api/admin/publications/:id` | `POST …/items/<id>/targets/<target>/allow-repost` with confirmation, plus `skip`, `mark-published` and `publish-now` (#10) |

Publications made by other modules are recorded too: the module listens to `social.post.published/v1` and `social.post.failed/v1`, so microblog posts get the same cells as photos (D5).

Choosing the next post changes as well; rotation stays the anniversary rule of today (D1), announcements follow §5.2:
- Posts with status `skipped` are never chosen again.
- Open transient failures (network, 5xx, rate limit) come back first, with growing gaps of 1, 3 and 7 days. Account and content failures are retried only by hand, from the Problems page (D2).
- On account errors the connection pauses instead of burning every run, and reports it through its `notify` slot.

## 7. Tests

The general strategy is `modular-architecture.md` §6. For autoposting in particular:
- Stage 0 starts with one failing test per critical finding: feed unreachable → process keeps running (#1); every cron expression parsed and its frequency checked (#2); startup on an empty database (#3); an item without an image does not panic (B2).
- Unit tests for the choice of the next post, both strategies, `since`, the retry gaps and skips; for the cell state of a post and target; for the open/resolved rule of failures.
- UI tests with Playwright against the integration setup, using the generators for test feeds (photos and blog posts) and seed data: the posts page with filters and search across pages, the detail panel, "run now" with its result, the problems page, mobile tiles.

## 8. Build order

There are no fixes in today's code ahead of the rebuild (D7). The fixes come first within Stage 0 of `modular-architecture.md` §7, each with a test that reproduced the bug first.

**Stage 0: module boundaries in the monolith, fixes first**
1. #1: replace `log.Fatalf` in `inventory/imageRssToDb.go` by logging and returning; the next fetch tries again.
2. #2: inventory cron to `0 0 * * * *` (hourly).
3. #3: skip migration 001 when its tables do not exist yet.
4. B2: check for an image before `entry.Image.URL` (Bluesky, Pixelfed, Instagram) and guard the manual `Publish` with `recover`.
5. #4, #5: failure badge only without a later success; content errors do not mark a target down; no old error on a healthy target.
6. #6, #7: search and platform filter server-side, platform list from the config.
7. #10: the delete dialog says plainly that the photo will be posted again.
8. #11, #13: record routing skips as attempts; store the HTTP status.
9. Publications per target instead of per platform, causes for failures, the rotation and announcement strategies, choosing the next post with retries.

**Stage 1:** the `autoupload` API of §6, text feeds in the `rss` module, microblog publications through events; `hpc migrate` carries the old tables over, publications of photos no longer in the feed included.

**Stage 2:** the Posts, post detail, Schedule and Problems pages as the module's UI, dashboard widgets, preview through `preview`.

**Stage 3:** nothing of its own; targets that are services appear as columns automatically.

**No commit, no deploy** unless asked.

## 9. Decisions

All taken on 2026-10-20.

| # | Question | Decision |
|---|---|---|
| D1 | How rotation picks the next photo | the anniversary rule of today: the photo whose date is closest to today, ignoring the year. Rejected: oldest first (no seasonal link), random (cannot be shown in the schedule), a choice per connection (more config and tests) |
| D2 | Retrying failed posts | transient failures automatically after 1, 3 and 7 days; account and content failures only by hand; account failures pause the connection |
| D3 | An article without a cover image, on Instagram and Pixelfed | skipped with the reason "needs an image". Rejected: a generated card (`renderCard`), a fixed default image per connection |
| D4 | Sharing blog posts again | optional per connection (`reshareAfter`), off by default |
| D5 | Microblog posts | in the Posts table as a third kind, through events; editor and `publishTo` stay in the `microblog` module |
| D6 | Publications of photos no longer in the feed | kept and marked "no longer in feed", with their own filter; their likes keep counting |
| D7 | When the fixes of §3 land | first thing in Stage 0 of the rebuild; no separate change to today's code before it |
| D8 | Paging and likes | 50 posts per page, paged on the server; website likes always shown separately from platform likes |

## 10. Not in this concept

Generated images for Instagram and Pixelfed (D3). A choice of rotation rules (D1). Deleting publications of photos no longer in the feed (D6). Changes to today's code before the rebuild (D7).

## 11. Not checked yet

| What | Why not | How |
|---|---|---|
| The pages against real data (the live gallery feed, a blog feed, a production `companion.db`) | `lna-dev.net` is not reachable from the sandbox; production needs the admin key | repeat the Playwright run against a copy of the production `data/` and the live feeds |
| How the real blog feed marks cover images and excerpts | no access to the feed | read `https://lna-dev.net/…/index.xml` of the blog before B1 |
| Threads text posts, Bluesky link cards (external embed with thumbnail) | API behaviour from memory | check the current platform docs before Stage 2 |
| Whether the panic in B2 really stops the process through "Trigger Upload" | derived from the code, not triggered | the Stage 0 test with an item without an image, started through `POST /api/upload/:connection` |
