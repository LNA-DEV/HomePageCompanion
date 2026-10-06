# Concept: Webmention — receive and send, in the companion

**Status:** **decision pending, not started.** The decisions are in §12. Implementation starts with Stage 0 (§11), which is worth doing whether or not the rest ever lands.
**Constraint:** self-hosted in HomePageCompanion **only**. That rules out webmention.io, Bridgy, and a separate receiver service (webmentiond, Go-Jamming).
**Scope:** two repositories. This one gets the receiver, the sender and the admin UI. `Home-Page` gets the endpoint link, the display and the h-entry markup.
**Measured:** 2026-09-24, against the code of both repositories on that date, Home-Page's `.test-site/` built 2026-09-24 19:40, the read-only requests against the live site and companion listed in §2, and an endpoint-discovery dry run over every outbound link in the site's posts (§4).

Webmention is how one site tells another "I linked to you". The sender POSTs `source` (its page) and `target` (your page) to an endpoint your page advertises. The receiver fetches `source`, confirms that it really links to `target`, and may then show it as a reply, like, repost or mention. The companion already has a stub for the receiving half and nothing for sending, and the site does not advertise the stub. The stub is also the entry point for a stored-XSS chain that ends in the admin API key (§3.1), which is why Stage 0 should not wait for the rest.

## 1. Goal

1. Another site can send a webmention to any page of `lna-dev.net`. It is verified, classified (reply / like / repost / bookmark / mention), moderated in the admin UI and shown under the post.
2. When a post is published or edited, every page it links to that has a webmention endpoint is notified, including pages a link was removed from.
3. Nothing reachable from the public endpoint can reach the companion's internal network, the admin UI, or the site's HTML without being escaped.
4. The Home-Page test suite keeps its no-network rule.

## 2. Today

| Piece | State | Where |
|---|---|---|
| Receive endpoint | `POST /api/webmention`: parses both parameters with `url.ParseRequestURI`, requires a scheme and a host, inserts a row and answers `202`. It never fetches the source and never checks the target. No deduplication, no limits, no authentication | `src/webmention/main.go:14-37`, route `src/main.go:96` |
| Data model | `ID, Source, Target, CreatedAt` | `src/models/webmention.go` |
| Admin | `GET /api/admin/webmentions` returns every row, newest first. The `/webmentions` page lists them; the feed-item view counts mentions per item | `src/admin/handlers.go:67, 593`; the count at `:299`; `web/src/routes/webmentions/+page.svelte` |
| Sending | **Nothing.** `bruno/SendWebMention.bru` POSTs to `/webmention` (the route is `/api/webmention`) and exercises the *receiver*. `README.md:139` calls it "send an outgoing webmention" | — |
| Site | No `rel="webmention"` anywhere. No h-entry: `Home-Page/docs/concepts/indieweb-basics.md` is not started | — |
| Traffic | Local `src/data/companion.db`: table exists, **0 rows**. Local access logs (`src/data/logs/`, May–June 2026): **0** POSTs to `/api/webmention`. Production row count not checked (§14) | — |

Verified live, read-only, on 2026-09-24:

| Request | Result |
|---|---|
| `GET https://companion.lna-dev.net/health` | `200` |
| `POST https://companion.lna-dev.net/api/webmention` with no body | `400 {"error":"Invalid or missing 'source' and/or 'target'"}`. **The endpoint is public, reachable and unauthenticated.** An empty body fails validation before the insert, so nothing was written |
| CORS preflight with `Origin: https://lna-dev.net` | `204` + `Access-Control-Allow-Origin: https://lna-dev.net` |
| CORS preflight with `Origin: http://lnadevwj2…czpyd.onion` | **`403`** (§3.3) |
| `GET https://lna-dev.net/en/` | `200`. `webmention` appears 0 times; no `Link` header, no CSP header |
| Response headers of `companion.lna-dev.net` | `Server: openresty`: a host proxy sits in front of the `web` container's nginx |

## 3. Findings that need action regardless of Webmention (Stage 0)

### 3.1 Stored XSS through the admin UI → admin API key (live today)

- `isValidURL` (`src/webmention/main.go:14`) accepts **any scheme** that comes with a host. Run against a verbatim copy of the function, all of these pass: `javascript://x/%0aalert(document.domain)`, `data://x/text/html,hi`, `file://localhost/etc/passwd`, `http://companion:8080/api/admin/stats`.
- The Webmentions page renders `Source` as `<a href={wm.Source}>` in its "Recent Webmentions" card (`+page.svelte:143`). It renders `Target` the same way when it matches no feed item (`:162`). Svelte does not sanitize URL attributes.
- The admin UI is served from the same origin as the API (`web/nginx.conf`) and keeps the key in `localStorage.apiKey` (`web/src/lib/api.ts:9`).
- **Chain:** an anonymous `POST source=javascript://x/%0a<payload>&target=<anything>`, then one click on that link by the admin. The payload runs on `companion.lna-dev.net`, reads the key, and has the whole admin API. This was **not** exploited and **not** tested against production. It follows from the code alone.
- **Fix** (small, independent of everything else):
  - `isValidURL` accepts `http` and `https` only.
  - The Svelte page renders an `href` only for http(s) URLs, through one `safeHref` helper.
  - Stage 1 replaces the handler anyway, but this fix should not wait for it.

### 3.2 gin trusts every proxy

- `gin.Default()` runs without `SetTrustedProxies`. The logs carry gin's own warning: *"You trusted all proxies, this is NOT safe."*
- When every hop is trusted, `c.ClientIP()` returns the **leftmost** `X-Forwarded-For` entry. The client writes that entry, because `web/nginx.conf` only appends to the header (`$proxy_add_x_forwarded_for`).
- As a result, the native-like deduplication (`interactions/native.go:33, 98, 139`, hashed IP) can already be bypassed by sending a header, and any per-IP rate limit for webmentions would be worthless.
- **Fix:** `router.SetTrustedProxies(...)` with the compose network and the host gateway (`172.29.0.1` appears in the access logs). The right setting depends on whether openresty *sets* or *appends* `X-Forwarded-For` / `X-Real-IP`. That is host config, not in this repository (§14).

### 3.3 The onion origin is refused by CORS

- The allow-list at `src/main.go:76` is `*.<security.domain>` plus `localhost`. A preflight from the onion origin gets `403`.
- So on the Tor build, **the native likes, the like counts, the trips and the push subscription already fail today**. A webmention display would fail the same way.
- **Fix:** allow the onion origin through a config value, e.g. `security.extraOrigins`. Whether the onion site *should* call a clearnet service is a decision in its own right (D9): Home-Page's dex map makes zero third-party requests there on purpose.

### 3.4 The per-item webmention count can never match

- `enrichFeedItems` joins `Webmention.Target == FeedItem.Link`. The only datasource is the photo RSS, whose item links are `https://lna-dev.net/en/gallery/#<uuid>` (560 local rows, plus one legacy `…/gallery/underwater//#1876514885`).
- Nobody links a fragment URL as a webmention target. The spec also says a receiver SHOULD ignore the fragment when checking the target.
- Since this join was written, photos have got pages of their own: `/<lang>/gallery/photo/<slug>/`, with a permanent `/gallery/photo/<uuid>/` alias.
- **Fix, in Stage 1:** join on the photo's **UUID**, resolved from the target path.
  - The `<uuid>` alias form carries it directly.
  - The slug form resolves through `https://lna-dev.net/<lang>/gallery-metadata.json`, which the site already publishes with `id` and `slug` for every photo.

### 3.5 Bruno and README

- Fix the path in `bruno/SendWebMention.bru` and rename it to what it does (receive).
- Correct `README.md:139`.
- Add a real "send" request once Stage 4 exists.

## 4. What sending would reach: the dry run

**Method.**
- Parsed every post page in Home-Page's `.test-site/` (`<article class="post-single">`) and took each `<a href>` inside `.post-content`.
- Resolved relative links, dropped the fragment and dropped the site's own hosts.
- For each unique URL, ran a `GET` with redirects followed, checking the `Link` header first, then the first `<link>`/`<a rel="webmention">`. That is the spec's discovery order. 8 in parallel, 20 s timeout, nothing POSTed.
- The script was not kept; the sender's dry-run mode (§7) replaces it.

**Input:** 20 post pages (13 en, 4 de, 3 sv). 17 of them have outbound links: 117 (post, target) pairs, 86 unique URLs on 61 hosts. One is the site's own onion address, which leaves **85 external targets**.

**Result: 3 targets have an endpoint, which makes 4 webmentions for the whole back catalogue.**

| Source post | Target | Endpoint (found via) |
|---|---|---|
| `/en/posts/media/fediverse/` and `/de/posts/media/fediverse/` | `https://axbom.com/` | `https://axbom.com/webmentions/receive/` (HTML `<link>`) |
| `/en/posts/projects/bluesky-automation/` | `https://indieweb.org/` | `https://webmention.io/indiewebcamp/webmention` (`Link` header) |
| `/en/posts/projects/bluesky-automation/` | `https://www.citationneeded.news/posse/` | `https://www.citationneeded.news/webmentions/receive/` (HTML `<link>`) |

- 77 targets have no endpoint: GitHub, YouTube, the Guardian, Wikipedia, gohugo.io, bsky.app, joinmastodon.org, and others.
- 6 were unreachable: two `410`, one `403` (a bot block at pcmag.com), one connection refused, one DNS failure, and the own onion without Tor.

**What that means for the decision (D1):**
- Sending is cheap to build but has little to do today. Its value grows only as posts link personal, IndieWeb-aware sites. A link to a silo (bsky.app, github.com) never triggers anything.
- Interactions come from the **receiving** half, and only once other sites find the endpoint.
- The posts are not POSSE'd by the companion either: the only datasource is the photo RSS. There is no silo copy of a post that anything could backfeed from.
- For photos, `interactions/` already pulls Mastodon, Bluesky and Pixelfed reactions natively, which is the job Bridgy would do.

## 5. The spec, the parts that bind

W3C Webmention, Recommendation of 12 January 2017. Quoted where short.

**Sender**
- **Discovery:** the first HTTP `Link` header takes precedence, followed by the first `<link>` or `<a>` element in document order.
  - A relative endpoint MUST be resolved.
  - Query-string parameters MUST be preserved and MUST NOT move into the POST body.
  - Redirects are followed. A `HEAD` MAY come first.
- **Sending:** the sender MUST post `x-www-form-urlencoded` `source` and `target`. "Any `2xx` response code MUST be considered a success."
- **Updates:** the sender SHOULD re-send every previously sent webmention, "including re-sending a Webmention to a URL that may have been removed from the document", and MUST re-discover each endpoint when it does.
- **Deletes:** the sender SHOULD return `410 Gone` for a deleted source and re-send.
- **Security:** a sender that discovers a localhost endpoint SHOULD NOT send to it. A sender behind a firewall SHOULD be kept from reaching internal resources.

**Receiver**
- **Checks before answering:**
  - it MUST check that both are valid URLs of supported schemes;
  - it MUST reject `source == target`;
  - it SHOULD check that the target is a resource it accepts webmentions for, ignoring the fragment;
  - on a failed check it MUST answer `400`.
- **Processing:** it SHOULD queue the request and process it asynchronously, to prevent DoS. It answers `202`, or `201` + `Location` if it offers a status page.
- **Verification:** receivers MUST verify.
  - They fetch `source` with a GET, following redirects, and SHOULD limit how many.
  - The source "MUST have an exact match of the `target` URL", checked per content type (HTML links, JSON properties, plain text).
  - They SHOULD limit time and data spent on unverified sources; the spec's examples are 5 s and the first 1 MB.
- **Updates and deletes:** processing SHOULD be idempotent. On a deleted source the receiver SHOULD delete the mention or mark it deleted.
- **Republishing:** a receiver that republishes the data MUST encode or filter it against XSS and CSRF. Receivers MAY moderate first and MAY periodically re-verify.

## 6. Receiving: design

### 6.1 The request (synchronous) → `400` or `202`

- `source` and `target` parse, and both are `http` or `https`. Nothing else is accepted (the §3.1 fix, carried over).
- `source != target`.
- The target's host is `lna-dev.net`, or the onion host, which is **mapped to clearnet**. The fragment is ignored for the check, and the path starts with `/en/`, `/de/`, `/sv/` or is `/`.
- **Upsert** on `(source, normalised target)`. A repeat send resets the row to `pending`; that is how an update arrives.
- **Rate limit** per source host and globally, with a cap on the queue length, answering `429` past it. Per-IP limits only become meaningful after §3.2.
- The answer is `202`, with no status page (D10).

### 6.2 The queue

A cron tick in the shape of `interactions.RunTick` (every minute, N items per tick) works through `pending` rows. Nothing is fetched inside the request.

### 6.3 The fetch guard (shared with the sender)

One `*http.Client` for every outbound fetch the feature makes:

- **`net.Dialer.Control` rejects any non-public IP:** loopback, RFC 1918, CGNAT `100.64/10`, link-local `169.254/16` (which covers cloud metadata), `0/8`, multicast, `::1`, `fc00::/7`, `fe80::/10`.
  - `Control` sees the IP actually being dialled, after DNS and for every hop of a redirect, so it also covers DNS rebinding and redirects to an internal address.
  - In this deployment the internal targets it has to stop include `web:80`, `companion:8080`, the host gateway `172.29.0.1`, and whatever else the host runs.
- Ports 80 and 443 only.
- 5 s timeout, body read through `io.LimitReader` at 1 MB, at most 10 redirects (the spec's example is 20).
- A `User-Agent` that names the site.

### 6.4 Verification

- `GET source`.
  - `410` → delete the stored mention.
  - `404`, another error, or no link to the target → `rejected`. A previously verified mention becomes `gone` and disappears from the page.
- **The target check (async half of the SHOULD):** the target must answer `200` on the clearnet site.
- **Exact match** of the target string as sent:
  - **HTML:** `href` on `a`, `link` and `area`; `src` on `img`, `video`, `audio` and `source`. Uses `golang.org/x/net/html`, already in the module graph as an indirect dependency (v0.41.0).
  - **JSON and plain text:** substring.

### 6.5 Classification

- Parse the fetched body with `willnorris.com/go/microformats`, and take the first h-entry that contains the link.
- **Type:** `in-reply-to`, `like-of`, `repost-of` or `bookmark-of` equal to the target gives reply / like / repost / bookmark. Anything else is a mention.
- **Author:** the entry's `author` h-card (name, url, photo), falling back to the source host.
- **Content:** `content.value` as text, trimmed to 500 characters, plus `published` and the entry's `url`.
- **Only text is stored, never HTML** (the spec's MUST on republishing).

### 6.6 Model

`Webmention` gains:

- `Status` (`pending` | `verified` | `rejected` | `gone`) and `Moderation` (`pending` | `approved` | `hidden`);
- `Type`, `AuthorName`, `AuthorURL`, `AuthorPhoto`, `Content`, `Published`, `URL`;
- `VerifiedAt`, `LastCheckedAt`, `Error`;
- a unique index on `(Source, Target)`.

`AutoMigrate` adds the columns. Whatever rows production already has start as `pending` and are verified like new ones.

### 6.7 Re-verification

Weekly, over every verified mention (the spec's MAY). This catches a source that was deleted without a resend. It is also the removal path for someone who wants their mention off the page: they delete their post, and the mention goes.

### 6.8 Public read API

- `GET /api/webmentions?target=<url>&target=<url>` returns **approved and verified** mentions only.
- Fields: type, author name and URL (photo per D3), content, published, url.
- Only targets on this site are served, results are capped, and responses carry `Cache-Control: max-age=300`.
- CORS as today, plus the onion origin if D9 says so.

### 6.9 Admin

- Columns for status, type, author and content.
- Buttons: approve, hide, delete, re-verify; optionally, block a source host.
- Every `href` goes through `safeHref` (§3.1).
- The feed-item join switches to photo UUIDs (§3.4).

## 7. Sending: design

### 7.1 What to send for

- The site's posts, read from the per-language sitemaps (`https://lna-dev.net/<lang>/sitemap.xml`). Every post there has a `lastmod` (checked: 13 of 13 English posts), and edits are what trigger resends.
- A sitemap entry counts as a post only if its HTML is one: an h-entry once Stage 3 lands, `article.post-single` until then. That filters out the sub-section list pages under `/posts/`.
- The alternative is the post RSS: it has no limit set (13 / 4 / 3 items = every post), but it carries no `lastmod`. It would also need the `itemType: text` datasource that the README documents but `inventory/main.go:7` never implemented (it handles `image` only).

### 7.2 When

- An hourly cron (D7). `lastmod` is the cheap trigger; a hash of the post body is the truth.
- **Only against the live site**: the receiver fetches the source to verify, so a send before the deploy's rsync is rejected. Polling the live sitemap guarantees the order by construction.

### 7.3 Links

- `DiscoverLinksFromReader(body, url, selector)`, with `.e-content` as the selector once Stage 3 lands and `.post-content` until then.
- Drop the site's own hosts, clearnet and onion, and every non-http(s) URL.

### 7.4 Per link

- `DiscoverEndpoint(target)` through the guarded client. It runs on every send, because the spec requires re-discovery.
- No endpoint → recorded as `no-endpoint`. Otherwise `SendWebmention(endpoint, source, target)`, where any `2xx` is success.

### 7.5 Table and retries

- `WebmentionSent`: `Source, Target, Endpoint, Status (sent | no-endpoint | failed), HTTPStatus, Attempts, NextRetryAt, ContentHash, SentAt`.
- **Retries:** network errors and `5xx` are retried with backoff, up to 5 attempts spread over days. `4xx` is not retried.
- `interactions/retry.go` has a backoff helper, but it only retries rate-limit errors, so it would need a different predicate. A small loop of its own is simpler.

### 7.6 Updates

When a post's hash changes, send to every target in **old links ∪ new links**. That is the spec's resend "including … a URL that may have been removed".

### 7.7 Deletes

The SHOULD is a `410` for a deleted post. Hugo cannot emit one, so it would need an nginx rule on the production host. Recommendation: skip it (D11). The admin gets a "resend all for this source" button for the rare case.

### 7.8 Source URL

Always the canonical **clearnet** permalink, never the onion. That is the rule the metadata-embedding pass follows in Home-Page.

### 7.9 Dry-run mode

An admin action that runs discovery and records the sends it would make, without POSTing. §4 is what it would show today.

## 8. Home-Page: what changes there

1. **Advertise the endpoint** in `layouts/partials/head.html`, next to the canonical link (line 68): `<link rel="webmention" href="https://companion.lna-dev.net/api/webmention">`.
   - Build it from `site.Params.CompanionUrl` (`hugo.yaml:216`) directly, not through the dev/prod switch at `head.html:322`. It is metadata for other servers and the page never calls it.
   - It is the clearnet URL in the Tor build too.
   - Put it on every page. Verification makes that safe, and D6 decides where mentions are shown.
2. **Display** in the generic post branch of `layouts/_default/page.html` and `single.html` (the same pair the theme dispatch lives in).
   - A small JS module fetches the read API for every `.AllTranslations` permalink (D5).
   - Everything goes in with `textContent`; only http(s) URLs become links.
   - Likes and reposts render as a count or a row of names; replies and mentions as entries.
   - i18n under a `# Webmentions` block in `i18n/{en,de,sv}.yaml`. Styles in `assets/css/extended/webmentions.css`, using theme variables only.
   - With JS off nothing is shown, which is the same as the likes.
3. **Tests.**
   - Static: every page has exactly one `rel="webmention"`, with the clearnet URL.
   - e2e: mock `GET ${COMPANION}/api/webmentions` in `tests/support/network.ts` the way `tests/e2e/likes.spec.ts` mocks the likes. Assert the rendering, and assert that a `javascript:` URL in the mock comes out as text. No network.
4. **h-entry** (`docs/concepts/indieweb-basics.md` §3–4) is a **soft prerequisite for sending**, not for receiving. Without it:
   - a receiver can only show your mention as a bare link, because it reads your page's h-entry to render it;
   - the sender has no `.e-content` to take links from.
5. **The About page's Privacy paragraph.** It is prose, so it is the site owner's to write. What it has to cover:
   - that received mentions are stored (URL, author name, author URL, text) and shown after approval;
   - that deleting the mentioning post removes the mention at the next re-verification;
   - how else to ask for removal.

## 9. Libraries: build vs reuse

| Need | Choice | Why | Alternative, and why not |
|---|---|---|---|
| Endpoint discovery and sending | `willnorris.com/go/webmention`: BSD-3, **no tagged release** (pseudo-version `v0.0.0-20250531043116-33a44c5fb605`), last commit 2025-05-31, not archived, **sender-only** | Read in its source: `HEAD`, then `GET`; `Link` header before HTML; the first `<link>`/`<a>`; relative endpoints resolved against the final URL after redirects; `DiscoverLinks` takes a CSS selector. It accepts our `*http.Client`, so the §6.3 guard plugs straight in | Hand-rolling: webmention.rocks has **23** discovery tests for the edge cases. The library has no SSRF guard and no body cap, and both come from our client |
| Parsing a source's mf2 | `willnorris.com/go/microformats` v1.2.0: MIT, released 2023-01-31, last commit 2026-02-18 | The standard Go mf2 parser | Class-name regexes. That is the same argument `indieweb-basics.md` §5 makes for its tests |
| Verifying the link | `golang.org/x/net/html`, already indirect at v0.41.0 | About 30 lines over the tokenizer | — |
| The receiver as a whole | Written here | The library is sender-only. The receiving side is small, it must join onto feed items, photos and the admin UI, and the SSRF guard is ours either way | webmention.io, webmentiond, Go-Jamming: excluded by the constraint |

## 10. Tests

**Go (`go test`, `httptest`, no network):**
- the fetch guard: each blocked range, a public-to-`127.0.0.1` redirect, a name that resolves to a private address (through an injected resolver);
- request validation: `javascript:`, `data:`, `file:`, `source == target`, a foreign host, onion → clearnet mapping, the fragment;
- verification: link present, link absent, `410`, `404`, JSON, text;
- classification, over fixtures for reply, like, repost, bookmark and mention;
- the sender: discovery through a fake target serving a `Link` header or HTML; resend to removed links on update; retry on `5xx` but not on `4xx`.

The repository already has Go tests (`imageresize/imageresize_test.go`) and CI (`.github/workflows/build.yaml`).

**Home-Page:** as in §8.3.

**By hand, needs the network, outside both suites:** webmention.rocks.
- Discovery 1–23 against the sender.
- Receiver 1–2.
- Update 1–2 and delete 1.
- Its own page notes that the formal receiver suite is still in progress, so the receiver's real coverage is the Go tests.

## 11. Build order

**Stage 0: fixes, independent of the rest, useful now (companion)**
1. `isValidURL` accepts http(s) only; `safeHref` on the Webmentions page (§3.1).
2. `SetTrustedProxies`, after checking what openresty forwards (§3.2, §14).
3. The onion origin in CORS, if D9 says so (§3.3).
4. Bruno path and README line (§3.5).

**Stage 1: receive (companion)**
1. The fetch guard (§6.3).
2. Model and migration (§6.6).
3. Request validation and rate limit (§6.1).
4. Queue tick (§6.2), then verification (§6.4), then classification (§6.5).
5. The re-verify job (§6.7).
6. The read API (§6.8).
7. Admin moderation and the UUID join (§6.9, §3.4).
8. Go tests.

**Stage 2: advertise and display (Home-Page)**
1. The head link.
2. Display partial, JS, i18n and CSS.
3. Tests.
4. The privacy text, written by the site owner.

**Stage 2 goes live only after Stage 1 is deployed.** Advertising an endpoint that stores unverified input would reopen Stage 0.

**Stage 3: h-entry (Home-Page)**, `indieweb-basics.md` §3–4.

**Stage 4: send (companion)**
1. Sitemap poll and post detection.
2. Content hash and link extraction.
3. Discovery and send.
4. `WebmentionSent` with retries.
5. Resend on edit.
6. The admin "Sent" tab and dry run.
7. Go tests.

The first real run is the 4 sends of §4.

**Stage 5: conformance** by hand on webmention.rocks, then a report. **No commit, no deploy** unless asked.

## 12. Decisions to take

| # | Question | Recommendation |
|---|---|---|
| D1 | Build sending at all, given §4 (4 sends for the whole back catalogue)? | **Yes, but last** (Stage 4). With the library it is small; the three receivers are exactly the IndieWeb sites the posts talk about; and it pays off more with every post that links a personal site. Receiving comes first because it is the half with the real risk, and the half other sites need |
| D2 | Show mentions live (client-side, like the likes) or bake them in at build time (`resources.GetRemote`)? | **Live.** Baked in, a new mention waits for the next manual ~7½-minute deploy, and the build would depend on the companion being up |
| D3 | Author avatars? | **None at first.** Hotlinking sends every visitor's IP to the mentioning site. Caching them in the companion (`imageresize.PrepareForTarget` exists) means storing other people's faces. A name is enough to start |
| D4 | Moderation? | **Manual approval for everything to start.** There will be few mentions, and each one is someone else's name and words on your page. Revisit auto-approving likes once there is traffic |
| D5 | Mentions of the `/en/` and `/de/` versions of one post? | **Merged:** the page asks for all its `.AllTranslations` permalinks |
| D6 | Which pages accept mentions, and which show them? | **Every page accepts; only posts show them** at first. Photo pages and dex pages later, each as its own decision |
| D7 | Send trigger? | **An hourly cron over the sitemaps.** It leaves `deploy.sh` untouched. The alternative is an authenticated `POST /api/webmention/send` at the end of `deploy.sh`: immediate, but the gate script gains a network step and a secret |
| D8 | Existing posts? | **Send the 4** of §4, after one dry run of the real sender |
| D9 | Allow the onion origin in CORS? This also fixes likes, trips and push on the Tor build | **Yes.** The Tor build already calls the companion on every gallery page, and today those calls just fail. The other consistent choice is to stop issuing them from the onion build at all |
| D10 | Status page (`201` + `Location`)? | **No, `202`.** Senders rarely use one, and it is one less public surface |
| D11 | `410` for deleted posts? | **Skip.** Hugo cannot emit it and it needs a rule in the host nginx. The admin's "resend all for this source" button covers the rare case |

## 13. Not in this concept

IndieAuth and Micropub. Bridgy, excluded by the constraint and not needed (§4). Salmention and Vouch. The companion's own microblog posts (`GET /api/microblog/posts/:slug`) as webmention sources or targets. Gallery photo pages and dex pages as h-entries, and showing mentions on them (D6).

## 14. Not checked yet

| What | Why not | How |
|---|---|---|
| Rows already in the production `webmentions` table (spam through the open endpoint?) | Needs the admin key against production | `curl -s -H "Authorization: ApiKey $ADMIN_API_KEY" https://companion.lna-dev.net/api/admin/webmentions \| jq length` |
| Whether openresty sets or appends `X-Forwarded-For` / `X-Real-IP` | Host config, not in this repository | Read the host's openresty site config for `companion.lna-dev.net`. It decides the `SetTrustedProxies` value in §3.2 |
| The webmention.rocks runs | Needs a deployed receiver and sender | Stage 5 |
