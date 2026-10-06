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
- Maps for the website without third parties: a self-hosted OpenStreetMap
  vector basemap (copied monthly into S3), trip routes computed once per leg,
  and a caching proxy for GBIF's occurrence tiles. See *Maps* below.

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
| `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` | Credentials for the object storage bucket (`storage.bucketUrl`). |

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
- `security.extraOrigins[]` — further CORS origins, compared exactly (the
  site's onion address).
- `storage` — the object store the modules share: `bucketUrl` and the
  companion's folder in it, `prefix` (`home-page-companion`). Every module
  keeps its objects under `<prefix>/<module>/` — the basemap under
  `home-page-companion/maps/basemap/` — and nothing outside the prefix is
  ever touched.
- `basemap`, `routing`, `gbif` — the maps; see *Maps* below and the template.

## Maps

The website's maps (the trip map, the dex's detailed map, route maps in posts)
make no request to anyone but this companion. Design and reasoning live in the
Home-Page repository, `docs/concepts/self-hosted-maps.md`.

**Basemap** (`basemap/`). A [Protomaps](https://docs.protomaps.com/) planet
build — one PMTiles file of OpenStreetMap vector tiles, ~139 GB, zoom 0–15 — is
copied from the daily builds into the storage bucket
(`<storage.prefix>/maps/basemap/<YYYYMMDD>.pmtiles`) and served through
[go-pmtiles](https://github.com/protomaps/go-pmtiles):

| Route | |
| --- | --- |
| `GET /api/tiles/basemap.json` | TileJSON of the active build (`max-age=3600`) |
| `GET /api/tiles/basemap/:version/:z/:x/:y.mvt` | a tile of the active or a retained build (`immutable`) |
| `GET /api/admin/basemap` | status, progress of a running copy, the stored builds |
| `POST /api/admin/basemap/update` | start an update now |
| `POST /api/admin/basemap/cleanup` | delete expired builds and stale uploads |

The update job (`basemap.schedule`, monthly) finds the newest build of the
last week, refuses it before copying if it is not schema `basemap.schemaMajor`
at zoom 15, then copies it part by part — a ranged GET into an S3 multipart
`UploadPart` — and records every finished part, so a crash or a restart
resumes at the next missing part (`ResumeInterrupted` at startup). It then
compares size and header with the source, reads the TileJSON and sample tiles
through the serving path, and switches over. The previous build stays
servable for `retainDays`, because browsers cache the TileJSON for an hour and
the tiles under a build's URL forever. The *Basemap* page of the admin UI shows
all of it. Only keys under the module's prefix are ever written or deleted.

**Trip routes** (`routing/`). When a trip is saved, every car and train leg
without a stored track is queued; a worker asks OSRM (car) or Transitous (rail,
falling back to the road) at most once per second, simplifies the line and
stores it keyed by mode and points. The public trip payload carries it as
`transportIn.geometry`. Flights stay straight. `POST /api/admin/routes/backfill`
queues missing and failed legs (also done at startup); the trip editor shows
each leg's status and can recompute one.

**GBIF** (`gbif/`). `GET /api/tiles/gbif/:taxonKey/:z/:x/:y.png` proxies
GBIF's density tiles with the one parameter set the site uses, caching them
under `data/cache/gbif` (fresh for GBIF's `max-age`, then revalidated with the
ETag; served stale while GBIF is down; capped at `gbif.cacheMB`).

The S3 store has an integration test that is skipped unless a bucket is named:

```bash
cd src
BASEMAP_S3_TEST_URL='s3://<bucket>?endpoint=https://nbg1.your-objectstorage.com&region=nbg1' \
AWS_ACCESS_KEY_ID=… AWS_SECRET_ACCESS_KEY=… go test ./basemap -run S3Store
```

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
