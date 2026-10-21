# HomePageCompanion

A self-hosted bridge between your website's feeds and your social-media
accounts — Mastodon, Pixelfed, Bluesky, Instagram and Threads — with an admin
dashboard, a federated microblog, engagement tracking and the website's maps.

The companion reads the website's RSS feeds, posts new items to the
configured networks on a schedule, and collects what comes back (likes,
replies, webmentions). It is built from modules: a small core, building
blocks compiled into one program, and services in containers of their own
in any language. Design: [`docs/concepts/modular-architecture.md`](docs/concepts/modular-architecture.md).

## Features

- **Autoposting**: one connection per feed and network, with a cron
  schedule, rotation or announcing, captions, alt text, EXIF and copyright,
  a preview of the exact post, and a page per item with its state on every
  network. People in a photo are named and, with their consent, tagged
  (*People in photos* below).
- **Networks**: Mastodon, Pixelfed, Bluesky, Instagram, Threads compiled in;
  Lemmy as a service (`services/lemmy`). Mastodon, Pixelfed, Instagram and
  Threads accounts connect with OAuth on their settings page, or take a
  token from `.env`; Instagram and Threads tokens are refreshed every week.
- **Microblog**: posts with images and content warnings to several networks,
  their replies and likes fetched back.
- **Engagement**: website likes (deduplicated by a salted IP hash),
  webmentions, remote interactions; web-push notifications.
- **Maps** without third parties: a self-hosted OpenStreetMap basemap, trip
  routes computed once per leg, a caching proxy for GBIF's tiles.
- **Admin UI**: a shell whose pages, widgets and settings come from the
  building blocks; problems, jobs, logs, backups, metrics.
- **Operations**: one container, SQLite or another database module, blobs on
  disk or in S3, encrypted secrets, backups and restore, a migration from the
  old installation, Prometheus metrics at `/metrics`.

## How it is built

| Part | What it is |
|---|---|
| `core/` | config, building blocks and their connections, gateway, jobs, events, secrets, backups, the API for services, the egress proxy |
| `companion/` | the SDK of the building blocks: `companion.Info`, slots, `Context` |
| `capability/` | the capabilities as Go interfaces (`social.publisher/v1`, `feed.source/v1`, `media.image/v1`, …) and their generated adapters |
| `modules/<name>/` | building blocks compiled into the companion, each a Go module of its own |
| `services/<name>/` | building blocks in their own container (`lemmy`, Python) |
| `service/`, `sdk/python/` | the SDKs that run a building block as a service |
| `cmd/hpc/` | the tool: `build`, `apply`, `dev`, `test`, `gen` |
| `web/` | the admin shell (SvelteKit) and `@companion/ui` for module UIs |

`build.yaml` says what is compiled in, `companion.yaml` what runs and how it
is connected: a slot of one block names an instance of another
(`target: mastodon:main`), whether that is compiled in or a service.

## Quick start (Docker)

```bash
git clone https://github.com/LNA-DEV/HomePageCompanion
cd HomePageCompanion
mkdir -p data
cp .env.example data/.env                      # the admin key, the accounts' tokens
cp companion.yaml.example data/companion.yaml  # feeds, networks, connections
$EDITOR data/.env data/companion.yaml
docker compose up -d
```

Then open <http://localhost:8080> and log in with `ADMIN_API_KEY`. The
published image is `ghcr.io/lna-dev/homepagecompanion` (see
`docker-compose.yml`). `docker compose exec companion /app/companion config check`
checks every connection of `companion.yaml`; changes to it are picked up
without a restart.

**Moving over an old installation** (its `data/config.yaml`, `companion.db`,
`.env`): `docker compose run --rm companion migrate --check` shows what
would happen, `docker compose run --rm companion migrate` writes
`companion.yaml` and moves every table into its module's area. The website
sees the same answers as before.

## Configuration

`companion.yaml` ([`companion.yaml.example`](companion.yaml.example)) has
three parts:

- `core`: `domain` (CORS), `adminKey`, `publicUrl`, `database`, `blobs`,
  `backups`, `listen`, `servicesListen`.
- `modules`: one block per compiled-in module, with `config:` (the instance
  `default`) or named `instances:`.
- `services`: building blocks in containers, with `from:` (`oci://…`,
  `git+https://…@ref`, `path:<dir>`), their `grants` and `instances`.

`${NAME}` and `${NAME:-default}` are read from the environment and
`data/.env` ([`.env.example`](.env.example)); the environment wins.

| Variable | Purpose |
| --- | --- |
| `ADMIN_API_KEY` | the admin login, and `Authorization: ApiKey <key>` for scripts |
| `COMPANION_DOMAIN` | the website's domain, for CORS |
| `COMPANION_SECRET_KEY` | 32 bytes, base64 (`openssl rand -base64 32`): encrypts the secret store (refreshed and connected tokens, the VAPID key). Without it the core generates `data/secret.key`; keep it with the backups |
| `TRUSTED_PROXIES` | the reverse proxies whose `X-Forwarded-For` counts (IPs, CIDRs, `none`); unset: the private networks |
| `IP_HASH_SALT` | the salt of website likes' IP hashes |
| `WEBPUSH_SUBSCRIBER_MAIL` | the contact sent to web-push providers |
| `MASTODON_*`, `PIXELFED_*`, `BLUESKY_*`, `INSTAGRAM_*`, `THREADS_*` | the accounts, unless connected on their settings page; Bluesky takes an app password |
| `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` | the S3 bucket of `blob.s3` |

## Operating

```bash
docker compose exec companion /app/companion backup -o /app/data/backup.tar.gz
docker compose stop companion && docker compose run --rm companion restore /app/data/backup.tar.gz
docker compose exec companion /app/companion secrets rotate --new "$(openssl rand -base64 32)"
```

Backups also run on `core.backups.schedule` into the blob store. The admin
UI lists them, the runs of every job, the logs and the open problems;
`/metrics` (admin only) has the calls, errors and latency per capability and
instance.

## Services

A service is a building block in its own container, in any language:
[`docs/services/protocol.md`](docs/services/protocol.md). `hpc apply` starts
the services of `companion.yaml` next to the companion with Docker Compose,
after showing what each one offers, asks for and gets:

```yaml
services:
  lemmy:
    from: oci://ghcr.io/lna-dev/companion-lemmy:latest
    grants: { egress: [lemmy.world] }
    instances:
      main: { server: https://lemmy.world, username: me, password: "${LEMMY_PASSWORD}", community: photography }
```

```bash
go run ./cmd/hpc apply -c data/companion.yaml   # writes compose.generated.yml and data/companion.lock
```

Every service runs unprivileged in a network of its own; it reaches the
internet only through the companion's egress proxy, and only the hosts of
its grant. Its pages appear in the admin UI in a sandboxed frame.

## Maps

The website's maps make no request to anyone but this companion (design:
the Home-Page repository, `docs/concepts/self-hosted-maps.md`).

- **Basemap** (`modules/basemap`): a [Protomaps](https://docs.protomaps.com/)
  planet build, ~139 GB of OpenStreetMap vector tiles, copied monthly into
  the blob store part by part (a restart resumes at the next missing part),
  checked, and served at `GET /api/tiles/basemap.json` and
  `GET /api/tiles/basemap/:version/:z/:x/:y.mvt`. The previous build stays
  servable for `retainDays`. Status, update and cleanup: its page in the
  admin UI, or `/api/b/basemap`.
- **Trip routes** (`modules/routing`, `modules/trips`): every car and train
  leg of a trip is routed once with OSRM or Transitous, at most one request a
  second, and stored; `POST /api/b/trips/routes/backfill` queues missing ones.
- **GBIF** (`modules/gbif`): `GET /api/tiles/gbif/:taxonKey/:z/:x/:y.png`
  proxies GBIF's density tiles with a cache under `data/cache/gbif`.

## Token refresh (Instagram, Threads)

Meta's long-lived tokens expire 60 days after they were issued or last
refreshed, and an expired one cannot be refreshed. The `instagram` and
`threads` modules keep the current token in the secret store, use it in
place of the one in `.env` and refresh it once a week (`lib/metatoken`,
[`docs/concepts/meta-token-refresh.md`](docs/concepts/meta-token-refresh.md)).
Their settings page shows the expiry and the last error and refreshes on
request. If a token expired anyway, put a new long-lived one into `.env`
(a different one there always wins) or connect the account on the settings
page.

## People in photos

When a gallery item carries `lna:model` blocks (the people in the photo,
with their profiles and consent), every post names them in a line of its
own between the alt text and the caption:

```
With Jane Doe (@janedoe), Max Muster and Markus Penninger
```

The words are `modelsPrefix` and `modelsAnd` of the connection. Names are
never dropped: when a caption is too long, copyright, EXIF and hashtags go
first, then alt text and caption are shortened, then mentions become plain
names, and only then is the names line cut. A person is mentioned only with
`tagModels: true` on the connection and `autotag="true"` in the feed, with
the profile for that network (Bluesky with the DID, Mastodon and Pixelfed
through WebFinger); on Instagram they also get a photo tag. A profile that
does not resolve never fails the post. Design:
[`docs/concepts/model-tagging.md`](docs/concepts/model-tagging.md), feed
format: [`docs/concepts/autouploader-model-tags.md`](docs/concepts/autouploader-model-tags.md).

## Building your own companion

Add or remove modules in [`build.yaml`](build.yaml) (`path@version`, or
`path => ./dir` for a local one) and build the program or the image:

```bash
go run ./cmd/hpc build -f build.yaml -o companion   # the shell and module UIs need npm
docker build -t companion .
```

## Development

```bash
go run ./cmd/hpc dev -c data/companion.yaml   # rebuilds and restarts on change; the shell with hot reload at :5173
go run ./cmd/hpc dev --service lemmy="python3 services/lemmy/lemmy.py"   # a service from its source (its block has a url:)
go run ./cmd/hpc test modules/mastodon        # go vet and go test -race of one module
go run ./cmd/hpc test --run "python3 services/lemmy/lemmy.py" --config lemmy.yaml   # a service against its capabilities' suites
go run ./cmd/hpc gen                          # adapters, OpenAPI, TypeScript after changing a capability
```

The tests, as CI runs them:

```bash
scripts/go-each.sh vet ./... && scripts/go-each.sh test -race ./...   # every module
scripts/check-tests-each.sh                                          # every package has tests
(cd e2e && go test -race -tags integration ./...)                   # every module together, fake platforms
(cd web && npm ci && npm run check && npm run build && npm run build:modules && npm run test:ui)
docker build -t companion:test . && (cd e2e && COMPANION_IMAGE=companion:test go test -tags compose -run Compose ./...)
python3 -m unittest discover -s sdk/python/tests
```

No automated test talks to a real platform. By hand: a service against a
test account with `hpc test --run`, the S3 store against a bucket with
`BLOB_S3_TEST_URL` set (`modules/blob-s3`), and a module's `-tags live`
tests with `hpc test --live`.

## API testing

The website's and scripts' requests are a [Bruno](https://www.usebruno.com/)
collection in [`bruno/`](bruno/): the next upload, backfill, web-push
broadcast, interactions, native likes, webmentions, the VAPID key. Admin
requests send `Authorization: ApiKey <ADMIN_API_KEY>`.

## Repository layout

```
core/ companion/ capability/   the core, the building blocks' SDK, the capabilities
modules/                       compiled-in building blocks, one Go module each
services/ service/ sdk/        services, and the SDKs to write them (Go, Python)
cmd/hpc/                       hpc: build, apply, dev, test, gen
web/                           the admin shell and @companion/ui
lib/                           shared code of several modules (fediauth, metatoken, …)
e2e/                           integration, Compose and website tests, the test world
docs/                          concepts, the service protocol, OpenAPI of the capabilities
build.yaml                     what the image compiles in
companion.yaml.example         what runs and how it is connected
Dockerfile, docker-compose.yml one container
```
