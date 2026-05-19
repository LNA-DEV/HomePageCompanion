# HomePageCompanion

A self-hosted bridge between your RSS feeds and your social-media accounts —
Pixelfed, Bluesky, Instagram, and Mastodon — with a built-in admin dashboard,
federated microblog, and engagement tracking.

HomePageCompanion ingests RSS feeds, routes new items to configured social
platforms on a schedule, and tracks the resulting engagement (likes,
webmentions, remote comments). It also hosts a small federated microblog and
collects browser-side telemetry, all driven from a single SvelteKit admin UI.

## Features

- Multi-source RSS ingestion (image and text item types).
- Per-connection scheduled autoupload to Pixelfed / Bluesky / Instagram /
  Mastodon, with cron schedules, custom captions, EXIF appending, and
  copyright attribution.
- Federated microblog: write posts, attach images, set content warnings, fetch
  comments and likes back from remote servers.
- Engagement tracking: native likes (deduplicated by hashed IP), incoming
  webmentions, and remote interactions from federated servers.
- VAPID web-push notifications, including admin broadcast.
- Admin dashboard (SvelteKit) with stats, logs, and connection health.
- Client-log ingestion endpoint for browser-side telemetry.

## Architecture

- **Backend** — Go 1.25 with [Gin](https://github.com/gin-gonic/gin), SQLite
  via GORM, and `robfig/cron` for scheduled tasks. Entry point:
  [`src/main.go`](src/main.go). Listens on `:8080`.
- **Frontend** — SvelteKit 2 + Svelte 5 + Tailwind, built with the static
  adapter and served by Nginx in production. Lives in [`web/`](web/).
- **Orchestration** — `docker compose` runs two services:
  - `companion` — the Go binary, internal only.
  - `web` — Nginx serving the built SvelteKit app on host port `8080`, with
    API requests reverse-proxied to `companion`.

The host only exposes `web` on `:8080`; the Go backend is never published
directly.

## Quick start (Docker)

```bash
git clone <repo-url> HomePageCompanion
cd HomePageCompanion

# 1. Fill in credentials
cp .env.example .env
$EDITOR .env

# 2. Configure feeds, targets, and connections
cp src/data/config.yaml.example src/data/config.yaml
$EDITOR src/data/config.yaml

# 3. Bring it up
docker compose up -d
```

Then open <http://localhost:8080>.

To check backend health:

```bash
docker compose exec companion curl -f http://localhost:8080/health
```

## Configuration

### Environment variables

Defined in [`.env.example`](.env.example). Values present in the OS
environment win over the file. They can be referenced from `config.yaml`
using `${VAR}` or `${VAR:-fallback}` syntax.

| Variable | Purpose |
| --- | --- |
| `ADMIN_API_KEY` | API key for all `/api/admin/*` endpoints and the SvelteKit admin UI. **Required.** |
| `IP_HASH_SALT` | Salt used when hashing IPs to deduplicate native likes. |
| `PIXELFED_PAT` | Pixelfed personal access token. |
| `PIXELFED_INSTANCE` | Pixelfed instance URL (e.g. `https://pixelfed.de`). |
| `BLUESKY_USERNAME` | Bluesky handle. |
| `BLUESKY_PAT` | Bluesky app password. |
| `INSTAGRAM_ACCESS_TOKEN` | Instagram Graph API access token. |
| `INSTAGRAM_ACCOUNT_ID` | Instagram Graph API account ID. |
| `MASTODON_INSTANCE` | Mastodon instance URL. |
| `MASTODON_PAT` | Mastodon access token (used by autouploader and microblog). |
| `WEBPUSH_SUBSCRIBER_MAIL` | Contact string (usually `mailto:you@example.com`) sent to web-push providers. |

### YAML config

Edit `src/data/config.yaml` (template at
[`src/data/config.yaml.example`](src/data/config.yaml.example)).
Top-level sections:

- `security` — `apiKey`, `ipHashSalt`, public `domain`.
- `datasources.rss[]` — RSS sources, each with `name`, `url`, and
  `itemType` (`image` or `text`).
- `targets[]` — social accounts, each with `platform` and credentials sourced
  from env vars, plus optional per-platform image sizing overrides.
- `connections[]` — datasource → target routes, including caption template,
  cron schedule, routing tags, and EXIF / copyright flags.
- `microblog.publishTo[]` — list of target platforms that microblog posts
  publish to.
- `webpush.subscriberMail` — push-provider contact string.

## Local development

Backend:

```bash
cd src
go run main.go        # reads src/data/config.yaml
```

Frontend:

```bash
cd web
npm install
npm run dev           # SvelteKit dev server, proxies API calls to :8080
```

Adjust the proxy target in [`web/vite.config.ts`](web/vite.config.ts) if your
backend runs somewhere other than `localhost:8080`.

## API testing

REST requests are documented as a [Bruno](https://www.usebruno.com/)
collection under [`bruno/`](bruno/). Open the directory in Bruno, pick an
environment from `bruno/environments`, then run requests such as:

- `UploadNext.bru` — trigger the next scheduled autoupload.
- `Backfill.bru` — backfill items from configured datasources.
- `Broadcast.bru` — send a web-push broadcast.
- `Fetch Interactions.bru` / `Get Interactions.bru` — pull and inspect remote
  interactions.
- `Native Like.bru` / `Native Unlike.bru` / `Native Like Status.bru` — the
  native-like flow.
- `SendWebMention.bru` — send an outgoing webmention.
- `GetVapidPublicKey.bru` — fetch the VAPID public key for the frontend.

Admin endpoints require `ADMIN_API_KEY`. `/api/admin/client-logs` accepts the
key via either the `Authorization` header or a `?token=` query parameter.

## Repository layout

```
src/                Go backend — main.go, autouploader/, admin/, interactions/, microblog/, webpush/, ...
web/                SvelteKit frontend — routes, components, Nginx Dockerfile
bruno/              Bruno REST API collection
src/data/           Runtime volume — SQLite DB, logs, microblog media, config.yaml
.github/workflows/  CI — build.yaml, build-web.yml
Dockerfile          Multi-stage Go build (golang:1.25 → debian:bookworm-slim)
docker-compose.yml  Two-service compose (companion + web)
```
