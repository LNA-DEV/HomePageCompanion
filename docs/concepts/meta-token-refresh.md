# Concept: Token refresh for Instagram and Threads

**Status:** **implemented 2026-10-12, not committed, not deployed.** Every decision in §9 is taken; all stages of §8 are built. §12 lists where the code differs from this design, what the test run showed, and what turned up along the way.
**Constraint:** the companion keeps working with the token that sits in `.env` today. Refresh is added on top of that; it does not add an OAuth login flow (§10).
**Scope:** this repository only. Home-Page is not touched.
**Measured:** 2026-10-12, against the code at `c573e79`, the local `src/data/` (config, `companion.db`, logs from May–June 2026), and Meta's developer docs as fetched that day (§4).

Instagram and Threads both run on long-lived OAuth tokens that expire **60 days** after they were issued or last refreshed. Once one has expired it can no longer be refreshed: the only way back is a new token through Meta's developer dashboard. The companion reads both tokens once from `.env` and never renews them, so roughly every two months uploads, microblog posts, likes and replies for both platforms stop until someone pastes a new token by hand.

## 1. Goal

1. The Instagram and Threads tokens are renewed automatically, long before they expire, with no manual step as long as the companion runs at least once every few weeks.
2. A token pasted into `.env` by hand still takes effect. Automation never silently overrides a newer manual token.
3. The admin UI shows for each token when it was last refreshed, when it expires, and why the last refresh failed.
4. No token, old or new, ends up in a log line, an `upload_attempts` row or an admin API response, and none is stored in the database in plain text.

## 2. Today

| Piece | State | Where |
|---|---|---|
| Source of the token | `INSTAGRAM_ACCESS_TOKEN` / `THREADS_ACCESS_TOKEN` in `.env`, substituted into `config.yaml` as `${…}` | `.env.example:27,36`, `src/data/config.yaml.example:35,45` |
| Loading | `config.LoadConfig` reads `.env`, expands, unmarshals and **replaces `config.Data` wholesale**. It runs once at startup, **before** the database is opened, and then every 5 minutes from cron | `src/config/main.go:11-32`, `src/main.go:48,51,83` |
| Env precedence | `loadDotenv` never overrides a variable already in the environment. In production, compose sets them from `env_file: .env` at container creation, so editing `.env` needs a container recreate, not just the 5-minute reload | `src/config/dotenv.go:51`, `docker-compose.yml:7-9` |
| Users of the token | `Target.AccessToken`, read straight from `config.Data.Targets` at **18** call sites: the autouploader (`autouploader/instagram.go`, `threads.go`), the microblog (`microblog/microblog.go:250-349`), interactions (`interactions/instagram.go:40`, `threads.go:46`, `microblog.go:66,189`) and the backfill (`backfill/main.go:513`) | `src/config/models.go:132` |
| Refresh | **None.** No call to `refresh_access_token`, no expiry stored anywhere | — |
| Expiry detection | `classifyError` labels a message containing `expired` or `invalid token` as `auth`, which shows up as a failed upload. Nothing reacts to it | `src/autouploader/attempt.go:79-87` |
| Admin | `GET /api/admin/targets/health` derives `healthy / degraded / down` from upload attempts only. Shown on the dashboard | `src/admin/handlers.go:71,741-820`, `web/src/routes/dashboard/+page.svelte` |
| Notifications | `webpush.BroadcastNotification` sends to **every** subscriber of the site. It is a visitor channel, not an admin one | `src/webpush/notify.go:13` |
| Precedent for secrets in the DB | The VAPID key pair lives in `vapid_keys`, plaintext, in `companion.db` | `src/models/vapid.go` |
| Local data | Targets `main-instagram` and `main-threads` are configured. `upload_attempts` has **0** Instagram or Threads rows (only 6 Pixelfed and Bluesky rows from 2026-05-15). This is a dev database; production was not checked (§11) | `src/data/` |

## 3. Finding that needs action regardless of refresh (Stage 0)

### 3.1 Tokens leak into logs, the database and the admin UI on network errors

- `instagramapi` and `threadsapi` put `access_token` in the **query string** of every request. That is 10 `http.DefaultClient.Do` call sites (`instagramapi/media.go:31,76,120`, `likes.go:32`; `threadsapi/media.go:62,110,158,204,250`, `insights.go:38`, `replies.go:71`). The backfill does the same with `http.Get` (`backfill/main.go:512-516`).
- `http.Client.Do` wraps every transport error in a `*url.Error`, and its message contains the **full URL, query included**. Go only redacts a password in the userinfo part. Verified on 2026-10-12 with Go 1.26.8:
  `Get "http://does-not-exist.invalid/v1.0/me?access_token=SECRET123&fields=id": dial tcp: lookup does-not-exist.invalid: no such host`
- These errors are passed up unchanged. They are logged (`autouploader/instagram.go:33`, `threads.go:37`, `microblog/microblog.go:155`), and `RecordAttempt` stores them in `upload_attempts.error_message` (`autouploader/attempt.go:43-54`). From there the admin UI shows them on `/uploads` and as `lastError` on the dashboard.
- **Trigger:** any timeout, DNS failure or connection reset towards `graph.instagram.com` or `graph.threads.net`. Meta's own error bodies (4xx/5xx) do not contain the token, so those are fine.
- **Local evidence:** 0 occurrences of `access_token=` in the local logs. Production not checked (§11).
- **Why it matters more with refresh:** today a leaked token dies within 60 days at the latest. With refresh it is the same token that **anyone** holding it could keep alive by refreshing it themselves. The refresh call also adds one more request carrying the token.
- **Fix** (small, independent of everything else):
  - One shared helper, `redact.URLError(err)` (`src/redact/`). If `err` is a `*url.Error`, it replaces `URL` with the URL without its query, then returns the error. Every `Do` call site goes through it.
  - The backfill's `http.Get(nextURL)` gets the same treatment. Meta's `paging.next` URLs carry the token too.
  - Sending the token as an `Authorization: Bearer` header instead would avoid the query altogether. Whether both hosts accept that is not verified (§11), so redaction comes first.

## 4. What Meta allows

From the Instagram Platform reference (`refresh_access_token`) and the Threads "Long-lived tokens" guide, both read on 2026-10-12.

| | Instagram (Instagram API with Instagram Login) | Threads |
|---|---|---|
| Endpoint | `GET https://graph.instagram.com/refresh_access_token` | `GET https://graph.threads.net/refresh_access_token` |
| Parameters | `grant_type=ig_refresh_token`, `access_token=<token>` | `grant_type=th_refresh_token`, `access_token=<token>` |
| Precondition | The token must be "at least 24 hours old but has not expired" | The token must be "at least 24 hours old but have not expired" |
| Permission | `instagram_business_basic` | `threads_basic` |
| Response | `access_token`, `token_type: bearer`, `expires_in` (seconds; the doc's example is `5183944` ≈ 60 days) | the same three fields |
| Lifetime | "valid for 60 days from the date at which they are refreshed" | the same, plus: "Tokens that have not been refreshed in 60 days will expire and can no longer be refreshed" |
| Learning the expiry of a given token | Only through `expires_in` of a refresh. There is no documented debug endpoint for Instagram Login tokens | `GET /debug_token` returns `expires_at`, `issued_at`, `is_valid`, `scopes`. However, its `access_token` must belong to a **Threads tester** of the same app (D3) |

Notes:

- Neither endpoint is versioned. `GraphURL` in both packages carries a version (`v22.0/`, `v1.0/`), so the refresh URL needs its own variable.
- The companion already talks to `graph.instagram.com`. That host only accepts tokens from Instagram Login, so the configured token is one that this endpoint can refresh. A token from Facebook Login (`EAA…`) would need a different endpoint, but it would not work for the existing uploads either.
- **Neither doc says whether the old token stays valid after a refresh.** The design does not depend on it, but it decides how bad a crash between "refreshed" and "stored" would be (§5.4, §11).

## 5. Design

### 5.1 Where the current token lives

A new table `platform_tokens`, one row per target:

| Field | Meaning |
|---|---|
| `TargetName` (unique) | `Target.Name` from the config |
| `Platform` | `instagram` or `threads` |
| `AccessToken` | the token in use right now, the seed or the latest refreshed one, **encrypted** (below) |
| `SeedHash` | SHA-256 (hex) of the **config** token this row descends from (§5.2) |
| `RefreshedAt` | last successful refresh; `nil` while the row still holds the unrefreshed seed |
| `ExpiresAt` | `RefreshedAt + expires_in`; `nil` while unknown |
| `LastAttemptAt`, `LastError` | the last refresh attempt and its error, redacted, trimmed to 500 characters |

- A new package `metatoken` owns the model, the encryption, the overlay and the job. The two API packages each get a `RefreshToken(token string) (Token, error)`.

**Encrypted at rest (D5).**

- `AccessToken` holds `base64(nonce ‖ AES-256-GCM ciphertext)`.
- The key is `TOKEN_ENCRYPTION_KEY`: 32 bytes, base64 (`openssl rand -base64 32`). It goes in `.env` next to the tokens, and into `.env.example` and the README.
- The nonce is 12 random bytes, new on every write. The target name is the additional authenticated data, so a ciphertext copied into another row fails to decrypt.
- `SeedHash` stays a plain SHA-256. The token is random, so its hash does not reveal it.
- Standard library only: `crypto/aes`, `crypto/cipher`, `crypto/rand`.
- **What it protects against:** `companion.db` travelling without `.env`, as in a backup of `data/` or a copy pulled for debugging. With refresh, a leaked token can be kept alive indefinitely (§3.1), which is why this case is worth covering. It does not help against someone with access to the host, because the key sits next to the database. That is accepted.

**Without a key:** refresh is **off** (fail closed, D5b).

- Nothing is written to `platform_tokens`, the overlay (§5.2) is skipped, and the job (§5.3) does nothing.
- The tokens come from `.env` exactly as today, and they expire as they do today.
- One log line at startup, and a visible warning on the dashboard (§5.5).
- Existing rows are left alone. When the key comes back, the overlay picks them up again, as long as `.env` has not changed in between.

**Key changed or lost:** a row that does not decrypt is treated like a missing row. The overlay logs it and re-seeds the row from the config token (§5.2). If that config token has already expired, §6 is the way back.

### 5.2 Overlay: config token versus stored token

`config.LoadConfig` replaces `config.Data` every 5 minutes, so writing the new token into `config.Data` alone would be undone 5 minutes later. Instead, every load runs an overlay over the Instagram and Threads targets:

| Situation | Effect |
|---|---|
| Config token empty | Nothing. The existing "empty access token" errors stay as they are |
| No row for the target, or a row that does not decrypt | Insert (or replace) a row with the encrypted config token as `AccessToken` and its hash as `SeedHash`. Use the config token |
| `sha256(config token) == SeedHash` | The config has not changed since the row was seeded. **Use the decrypted `row.AccessToken`**, which may be a refreshed token |
| `sha256(config token) != SeedHash` | Someone put a new token in `.env`. **Replace the row** with it: new `SeedHash`, `RefreshedAt` and `ExpiresAt` cleared. Log one line that names the target, never the token |

This is what Goal 2 needs. The stored token wins only as long as `.env` still holds the token it descends from. A new manual token always wins. Storing a hash instead of a second copy is enough to notice the change.

**Wiring:**

- `config` gets a hook, `var AfterLoad func(*Config)`, called at the end of `LoadConfig` when set. That keeps `config` free of database imports.
- The first `LoadConfig` runs before the database exists (`main.go:48` vs `:51`). So `main` sets `config.AfterLoad = metatoken.Apply` after the migrations and then calls `config.LoadConfig()` once more.
- `metatoken.Apply` returns right away when `TOKEN_ENCRYPTION_KEY` is not set (§5.1).
- None of the 18 call sites change. They keep reading `target.AccessToken`, which is now the overlaid value.

### 5.3 The refresh job

- **When:** one cron entry, daily (`0 20 3 * * *`), plus one run about a minute after startup. Only with a key (§5.1).
- **What is due:** every row whose target is still configured, where `RefreshedAt` is `nil` (an unrefreshed seed, age unknown) or older than **7 days** (D2).
- **Per row:** `RefreshToken` → store `AccessToken`, `RefreshedAt = now`, `ExpiresAt = now + expires_in`, clear `LastError` → afterwards, if any row changed, call `config.LoadConfig()`. That is the same call the 5-minute cron makes, and it applies the overlay.
- **A fresh seed:** if the token is younger than 24 hours the refresh fails. That is expected: the next daily run tries again. The first success fills in `ExpiresAt`.
- **Budget:** with 60 days of lifetime and a 7-day interval, one token gets about 8 scheduled refreshes before it could expire. With a daily retry after each failure, it gets about 50 attempts in all.
- **Serialisation:** one mutex around the job, shared with the admin's "refresh now" (§5.5).

### 5.4 Failures

- **Any failure** (network, 4xx, 5xx, rate limit): keep the old token, record `LastAttemptAt` and `LastError`, retry on the next daily run. A dead token costs one request a day, so there is no state machine and no "give up" state.
- **Expired:** the token is `expired` when `ExpiresAt` lies in the past, or when the last error says so (OAuthException code `190` or the word `expired`, the same match `classifyError` uses). That is for display only. The way out is a new token in `.env` (§6), which re-seeds the row through §5.2.
- **Order of writes:** the new token is written to the database **before** it is used. If that write fails, the job logs it and keeps using the old token. Whether the old token still works after a refresh is unknown (§4); if it does not, a crash between the HTTP response and the DB write loses the token, and §6 is the recovery.
- **Callers holding the old token:** most callers copy `config.Target` by value (`autouploader/main.go:35`). An upload that is in flight during a refresh finishes with the old token. Same question as above, same consequence: at worst one failed attempt, which the existing retry covers.

### 5.5 Admin

- `GET /api/admin/tokens`: per Instagram/Threads target, `name, platform, source (config | refreshed), refreshedAt, expiresAt, daysLeft, lastAttemptAt, lastError, status (ok | expiring | expired | unknown | disabled)`.
  - `expiring` means fewer than 14 days left. With a 7-day interval that only happens after about 46 days of failed refreshes in a row, so it always means something is broken.
  - `disabled` means `TOKEN_ENCRYPTION_KEY` is not set.
  - **The response never contains the token or any part of it.**
- `POST /api/admin/tokens/:target/refresh`: runs the refresh for one target right away, ignoring the due rule, and returns the updated row. Without a key it answers `409` with the reason.
- **Dashboard:**
  - The target health card shows "token expires in N days" for these two platforms. `expired` turns the target `down`, `expiring` turns it `degraded`, whatever the upload attempts say.
  - Without a key, both cards show a visible warning: "Token refresh disabled: `TOKEN_ENCRYPTION_KEY` is not set". The upload status is left as it is, because uploads still work with the `.env` token until it expires.
- **No push alert.** `webpush` only reaches the site's visitors (§2). The log line and the dashboard are the signal (D4).

### 5.6 API client additions

- `instagramapi.RefreshURL = "https://graph.instagram.com/refresh_access_token"` and `threadsapi.RefreshURL = "https://graph.threads.net/refresh_access_token"`. Both are exported so tests can point them at `httptest`, like `GraphURL`.
- `RefreshToken(token string) (Token, error)` with `Token{AccessToken string; ExpiresIn int}`.
  - It rejects an empty `access_token` and an `expires_in` of 0 in the response.
  - It goes through the §3.1 `redact`.
  - It returns `ErrRateLimited` on `429`, like every other call in the package.

## 6. By hand: when a token has expired anyway

1. Create a new long-lived token in Meta's app dashboard: Instagram API setup with Instagram business login for Instagram, the Threads use case for Threads.
2. Put it in `.env` on the host.
3. `docker compose up -d companion` recreates the container with the new `env_file`. A `restart` does not re-read it (§2).
4. On the next `LoadConfig` the overlay notices the new hash and re-seeds the row (§5.2). From the next daily run on, refresh takes over again.

This belongs in the README's configuration section, next to the two variables.

## 7. Tests

`go test`, `httptest`, no network:

- **`redact`:** a `*url.Error` with `access_token=` in the query comes out without it. Other errors pass through unchanged.
- **`RefreshToken`, both packages:** success; `400` with an OAuthException body; `429` → `ErrRateLimited`; missing `access_token` or `expires_in` → error. The fake server also asserts the `grant_type`.
- **Encryption:** round trip; the wrong key fails; a ciphertext moved to another target name fails (AAD); two writes of the same token give different ciphertexts; a key that is not 32 bytes after base64 is rejected at startup.
- **Overlay:** no row → seeded; same hash → stored token used; new hash → row replaced and dates cleared; a row that does not decrypt → re-seeded; empty config token → untouched; non-Meta targets → untouched; no key → no database write and the config untouched.
- **Job:** due rule (nil, 6 days, 7 days); success writes the row and swaps the config; failure keeps the old token and records the error; a target that is no longer configured is skipped; no key → no request is made.
- **Database:** after a seed and a refresh, the raw `platform_tokens` row does not contain the token in plain text.
- **Admin:** the JSON contains no token. Asserted by seeding a known token and searching the response body for it.

The repository already has Go tests and CI (`.github/workflows/build.yaml`).

## 8. Build order

**Stage 0: redaction, useful now**
1. `redact.URLError`, applied at all 10 call sites in `instagramapi` and `threadsapi`, and in the backfill (§3.1).
2. Tests for it.

**Stage 1: store and overlay**
1. `platform_tokens` model and migration.
2. Encryption helpers and `TOKEN_ENCRYPTION_KEY` in `.env.example` and the README (§5.1).
3. The `config.AfterLoad` hook and `metatoken.Apply` (§5.2).
4. Tests.

After Stage 1 nothing changes in behaviour: the stored token is the seed, and that is the config token.

**Stage 2: refresh**
1. `RefreshToken` in both packages (§5.6).
2. The job and its cron entries (§5.3, §5.4).
3. Tests.

**Stage 3: admin**
1. The two endpoints (§5.5).
2. The token line and the missing-key warning on the dashboard card.
3. The README section (§6).

## 9. Decisions

All taken on 2026-10-12.

| # | Question | Decision |
|---|---|---|
| D1 | Build automatic refresh, or only an expiry warning and keep pasting tokens by hand? | **Refresh.** It is two GET requests, and without it both platforms go dark every 60 days at a moment nobody chose. A warning alone would not even work for Instagram, whose expiry is only known through a refresh |
| D2 | Refresh interval? | **7 days, fixed.** Far from the 24-hour minimum and far from the 60-day limit: about 8 scheduled refreshes and about 53 days of buffer per lifetime. A shorter interval gains nothing; a longer one leaves fewer retries if Meta or the companion is down for a while |
| D3 | Use Threads' `debug_token` to learn the expiry of a freshly seeded token? | **No.** It needs a token of a "Threads tester" of the same app, one more condition that may or may not hold, and it would give the two platforms different paths. The first refresh, at most a day after seeding, fills in `ExpiresAt` anyway |
| D4 | How should the admin learn that refresh keeps failing? | **Dashboard and log.** With refresh working, tokens do not expire. The signal is a safety net for access revoked at Meta, a companion that was down for weeks, or a bug that silently stops the job. There is no admin-only channel; `webpush` would notify the site's visitors. An admin channel would be a feature of its own (§10) |
| D5 | Encrypt the stored token? | **Yes, AES-256-GCM** with `TOKEN_ENCRYPTION_KEY` (§5.1). It covers the database travelling without `.env`, which matters more once a leaked token can be kept alive |
| D5b | What happens without `TOKEN_ENCRYPTION_KEY`? | **Refresh off, with a warning on the dashboard** (§5.1, §5.5). No plaintext fallback, and the companion still starts |
| D6 | Allow pasting a new token through the admin UI instead of `.env`? | **No, `.env` only.** With working refresh the manual path is rare, `.env` stays the one place a human puts secrets, and there is no write endpoint for secrets behind an admin key that can leak through XSS (`webmention.md` §3.1). If it comes later, it writes the row with the current `SeedHash`, so §5.2 keeps it |

## 10. Not in this concept

- A full OAuth flow inside the companion: redirect URI, app secret, exchanging a short-lived token for a long-lived one.
- Facebook Login tokens and the `fb_exchange_token` path.
- Mastodon, Pixelfed and Bluesky. Their credentials do not expire this way.
- An admin notification channel (D4).
- The unsynchronised swap of `config.Data` (`config/main.go:31`) while other goroutines read it. It exists today; this concept only adds one more caller of `LoadConfig`.

## 11. Not checked yet

| What | Why not | How |
|---|---|---|
| Whether production logs or `upload_attempts` already contain a token (§3.1) | Needs access to the production host | `grep -c 'access_token=' data/logs/*.log` and `sqlite3 data/companion.db "select count(*) from upload_attempts where error_message like '%access_token=%'"`. If either is non-zero, rotate both tokens after Stage 0 |
| Current expiry of the production tokens | Needs the tokens. Instagram only reveals it through a refresh, which changes state | Threads: `debug_token` (§4). Instagram: the first run of the Stage 2 job |
| Whether the old token stays valid after a refresh | Not documented (§4). Testing it means refreshing a production token | Refresh once by hand with `curl`, then call `GET /me?fields=id` with the old token |
| Whether a Threads refresh also extends `data_access_expires_at` | Threads' `debug_token` reports it apart from `expires_at` (§4). Neither doc says whether a refresh moves it. If it does not, Threads might need a new login now and then despite refresh | `debug_token` before and after a refresh, if a tester token is at hand. Otherwise it shows up as a failing refresh on the dashboard (D4) |
| Whether Meta returns something specific for a token younger than 24 hours | Not documented. What *was* measured (2026-10-12, a made-up token against both live endpoints): `400` with `{"error":{"message":"Failed to decode","type":"OAuthException","code":190,…}}`, so `code 190` covers malformed tokens too | The first refresh of a fresh seed. §5.4 treats it as an ordinary failure either way |
| Whether `graph.instagram.com` and `graph.threads.net` accept `Authorization: Bearer` | Not documented for these hosts | One `GET /me` per host with the header and no query token. If both work, the query parameter can go entirely (§3.1) |

## 12. Implementation notes

Built on 2026-10-12 on top of `c573e79`. Nothing committed.

**Where the code differs from the design above:**

- **One redaction helper, not one per package.** `src/redact/` holds `URLError`; `instagramapi`, `threadsapi` and the backfill call it. Three copies of the same ten lines would only drift.
- **The config reload after a refresh is panic-safe.** `config.LoadConfig` panics on a broken `config.yaml`. Cron recovers from that, but the startup run (`time.AfterFunc`) and the admin handler do not, so `metatoken.reloadConfig` recovers and logs. The new token is stored either way and takes effect on the next reload that works.
- **Writes are guarded by the seed hash.** The job updates a row only `WHERE id = ? AND seed_hash = ?`. A refresh that finishes after the row was re-seeded from `.env` drops its token instead of overwriting the new seed. That replaces the "one mutex around everything" idea of §5.3: the mutex only serialises job runs, and `Apply` has its own, so a slow request to Meta never blocks the 5-minute reload.
- **"Refresh now" answers `200` even when Meta refuses.** The refusal is in `lastError` of the returned status; `404` and `409` are kept for "no such target" and "no key".
- **Dashboard:** the token line sits inside each Instagram/Threads entry of the *Target Health* card: expiry, last refresh, the last refresh error, and a *Refresh token* button. Without a key it is replaced by the amber warning.

**Where it lives:**

| Piece | Where |
|---|---|
| Redaction | `src/redact/redact.go` |
| Refresh calls | `src/instagramapi/refresh.go`, `src/threadsapi/refresh.go` |
| Model | `src/models/platformToken.go` (table `platform_tokens`) |
| Hook | `config.AfterLoad` in `src/config/main.go` |
| Encryption, overlay, job, status, admin routes | `src/metatoken/` |
| Wiring | `src/main.go`: `Init`, `AfterLoad`, second `LoadConfig`, cron `0 20 3 * * *`, the startup run, the routes |
| Health integration | `src/admin/handlers.go` (`TargetHealth.Token`) |
| UI | `web/src/lib/api.ts`, `web/src/routes/dashboard/+page.svelte` |
| Docs | `README.md` (*Token refresh*, environment table), `.env.example` |

**Verified:**

- `go test ./...` passes for every package this touches, also with `-race`. The tests cover everything §7 lists.
- `npm run check` in `web/`: 0 errors, 0 warnings.
- The real binary, in a scratch directory with made-up tokens:
  - Without a key, both targets show `disabled` with the reason, refresh answers `409`, and nothing is written to `platform_tokens`.
  - With a key, both rows are seeded encrypted.
  - *Refresh now* reached both live Meta endpoints and got the `code 190` answer above. Both tokens then showed `expired` and both targets `down`.
  - The made-up tokens appear nowhere in `companion.db`, the app log or the access log.

**Found along the way, not fixed here:**

- On an empty database the companion does not start. Migration `001_itemname_to_guid` runs before `AutoMigrate` and fails on the missing `auto_upload_items` table, and `RunMigrations` then exits with `log.Fatalf` (`src/database/migrations.go`, `src/main.go:52-56`). A database that already has its tables is unaffected: there the migration ran long ago and is recorded.
- `go vet` fails in `webpush/notify.go:27,29` (`%s` for a `uint` ID). Because `go test` runs vet, `go test ./webpush` fails to build.
