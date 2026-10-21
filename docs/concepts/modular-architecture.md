# Concept: The companion as a shell for modules and services

**Status:** **decided, not started.** Every decision in §8 was taken on 2026-10-20. Implementation starts with Stage 0 (§7), which is worth doing whether or not the rest ever lands.
**Constraint:** one container in the normal case. An installation without services runs exactly one container; today's `companion` and `web` merge into it (§5.7).
**Scope:** this repository only. Home-Page keeps every public URL it calls today (§5.9).
**Measured:** 2026-10-20, against the code at `8c01664`: a local run of backend and admin UI with a generated test feed (details in `docs/concepts/autoposting-ui.md` §2), `go test ./...` and `svelte-check` (both green).

The companion does a dozen unrelated things: autoposting to five networks, a microblog, likes and webmentions, web push, trips, a self-hosted basemap, a GBIF tile proxy. All of it is wired by hand in `src/main.go`, shares one config struct and one SQLite file, and ships as one build. Whoever only wants the maps still configures social tokens, and nobody else can add a network without forking. This concept turns the companion into a shell in the style of Caddy: a small core plus **modules** compiled in at build time (trusted, Go, from a path or a Git repository, including other people's), and optional **services** in their own containers (untrusted, any language). Both offer the same capabilities, the operator connects them freely, and each brings its own admin UI.

## 1. Goal

1. Install only what you need. Maps without social tokens, autoposting without maps; a core without modules starts and runs.
2. Add a module with one line: a local path or a Git repository, including modules written by other people.
3. Run untrusted extensions as services: own container, any language, only the rights the operator grants.
4. The operator connects modules and services. A consumer never knows whether a module or a service sits behind a capability.
5. Every module and service ships its own admin UI; the shell composes them.
6. The website keeps working unchanged through every stage.

## 2. Today

| Piece | State | Where |
|---|---|---|
| Wiring | `main.go` knows every package, every cron job and every route | `src/main.go:31-165` |
| Config | One `Config` struct with a field per feature (`Basemap`, `Gbif`, `Routing`, `Microblog` …), loaded once and replaced every 5 minutes | `src/config/models.go`, `src/config/main.go` |
| Database | One SQLite file and a global `database.Db`; 21 models migrated in one call | `src/database/main.go`, `src/main.go:56` |
| Platforms | The same `switch target.Platform` in three places | `src/autouploader/main.go:74`, `src/interactions/scheduler.go:285`, `src/microblog/microblog.go:127` |
| Cross-package calls | `microblog` calls `autouploader.RecordAttemptWithSource` and `DownloadImageBytes`; `gbif` calls `basemap.ParseZXY`; `metatoken` reaches into `instagramapi` and `threadsapi` | `src/microblog/microblog.go:161, 178`, `src/gbif/gbif.go:104` |
| Admin API | `admin/handlers.go` (940 lines) reads the tables of almost every feature | `src/admin/handlers.go` |
| Admin UI | 13 fixed navigation entries | `web/src/routes/+layout.svelte:34-46` |
| Deployment | Two containers: the Go backend and nginx, which serves the SvelteKit build and proxies `/api` | `docker-compose.yml`, `web/nginx.conf` |
| Object storage | `storage.Prefix` gives each module `<prefix>/<module>/` in the bucket. This is the precedent the concept generalises | `src/storage/storage.go` |
| Files outside the bucket | Microblog and trip images on local disk; the basemap has its own `Store` with an S3 and a directory variant | `src/admin/microblog_handlers.go:24`, `src/admin/trips_handlers.go:24`, `src/basemap/store.go:26` |
| Secrets at runtime | Refreshed Meta tokens encrypted by `metatoken` itself; the VAPID private key in plain text | `src/metatoken/crypto.go`, `src/models/vapid.go:8` |
| Tests and CI | 9 of 27 Go packages have no tests (`backfill`, `database`, `interactions`, `inventory`, `logger`, `microblog`, `models`, `webmention`, `webpush`). The CI builds and pushes images and runs no test | `.github/workflows/build.yaml`, `build-web.yml` |
| Module path | `src/go.mod` declares `github.com/LNA-DEV/HomePageCompanion`, but the repository root has no `go.mod` | `src/go.mod` |

## 3. Findings that need action regardless of this concept (Stage 0)

The full list, with how each was reproduced, is in `docs/concepts/autoposting-ui.md` §3. The ones that block or shape this concept:

### 3.1 A one-minute feed outage stops the companion

- `inventory/imageRssToDb.go:17` calls `log.Fatalf` when the feed cannot be fetched or parsed. The reason never reaches the app log.
- The inventory runs every minute, not every hour: robfig/cron v1 has a seconds field, so `"0 * */1 * * *"` (`src/main.go:93`) fires at second 0 of every minute.
- Reproduced locally: feed server stopped, the process was gone at the next full minute. A restart without the feed fails at startup (`src/main.go:67`).

### 3.2 A fresh installation does not start

`database.RunMigrations()` (`src/main.go:54`) runs before `AutoMigrate`, and migration 001 (`src/database/migration_001_itemname_to_guid.go`) updates `auto_upload_items`, which does not exist yet on an empty database. Reproduced with an empty `data/` directory.

### 3.3 A feed item without an image can crash the process

Bluesky, Pixelfed and Instagram read `entry.Image.URL` without a nil check. robfig/cron recovers the panic; the manual trigger runs `Publish` in a bare goroutine (`src/main.go:207`) and takes the process down. Matters as soon as a blog feed is connected (`autoposting-ui.md` §5).

### 3.4 gin trusts every proxy

Already described in `docs/concepts/webmention.md` §3.2 and still open. The gateway of this concept sets `SetTrustedProxies`; it should not wait for it.

### 3.5 The CI never runs a test

All of the above lived in untested code, and nothing would have stopped a red build from being pushed. §6 is the remedy; its first step belongs to Stage 0.

## 4. Options considered

| | Option | Principle | Model |
|---|---|---|---|
| A | Monolith with switches | everything in one program, features turned on and off | the companion today |
| B | Caddy style | modules are Go packages compiled in; one program, one container | Caddy (`xcaddy`), CoreDNS (`plugin.cfg`) |
| C | Process plugins | one container, one process per module, gRPC over local sockets | Terraform providers, Grafana backend plugins (`go-plugin`) |
| D | WebAssembly | modules as `.wasm` in the core process, sandboxed | Extism, Envoy filters, Traefik v3 |
| E | Container per module | every module its own container, HTTP to the core | Home Assistant add-ons |
| F | **Hybrid of B and E** | B as the normal case; untrusted or non-Go extensions as services in their own container | Home Assistant: integrations in-process, add-ons as containers |

Scored against what a website companion needs (`++` = 2 … `−−` = −2, weighted high 3, medium 2, low 1):

| Criterion | Weight | A | B | C | D | E | F |
|---|---|---|---|---|---|---|---|
| Simple operation | high | ++ | ++ | + | ++ | −− | + |
| Only what you need | high | − | ++ | ++ | ++ | ++ | ++ |
| Modules from path and Git | high | −− | ++ | + | o | ++ | ++ |
| Protect secrets from foreign code | high | + | −− | − | ++ | + | + |
| Long background jobs (139 GB basemap copy) | high | ++ | ++ | + | −− | + | ++ |
| Robust public endpoints | medium | − | − | + | + | ++ | o |
| Open to foreign modules | medium | −− | − | + | + | ++ | + |
| Little memory | medium | ++ | ++ | + | + | − | + |
| Feasible rebuild | medium | ++ | + | − | −− | − | + |
| Fast calls between modules | low | ++ | ++ | + | + | o | + |
| **Weighted sum** | | 10 | 22 | 17 | 15 | 16 | **31** |

F wins because it keeps B's single container and adds E's isolation only where it is needed. The cost of B alone is that every module can read every secret in the process; F moves untrusted code out of the process instead. A first draft of this concept chose E; it was dropped because a typical installation would have run about 16 containers. Interpreted Go plugins (Traefik's Yaegi) were left out: they work only with the standard library, which rules out the AWS SDK, SQLite and `go-pmtiles`.

## 5. Design

### 5.1 Terms

| Term | Meaning | Example |
|---|---|---|
| Core | The Go program: gateway, connections, events, jobs, service manager, admin shell | `companion` |
| Module | Go package compiled into the core at build time. Trusted, runs in-process. Yours or someone else's | `mastodon`, `trips`, `github.com/someone/hpc-pixelfed-stories` |
| Service | Own container in any language, talks HTTP to the core. Untrusted | `lemmy` in Python |
| Building block | Module or service | |
| Capability | Versioned interface a building block offers or needs | `social.publisher/v1` |
| Instance | One configured occurrence of a building block, addressed as `<block>:<name>` | `mastodon:main`, `rss:photos` |
| Slot | Place where a building block needs a capability | `autoupload` needs `source` (`feed.source/v1`) and `target` (`social.publisher/v1`) |
| Connection | The operator's assignment of an instance to a slot | `target: lemmy:main` |

### 5.2 Architecture

```
Browser (website, admin UI)
        │ HTTPS
┌───────▼──────────────────────────────────────────────────────────────┐
│ Companion container: one Go program, trusted                         │
│  Admin shell: module UIs inline, service UIs in a sandboxed iframe   │
│  Gateway · Connections · Events and jobs · Service manager + proxy   │
│  Modules: rss mastodon bluesky pixelfed instagram threads autoupload │
│           interactions microblog webmention webpush trips routing    │
│           basemap gbif media db.sqlite blob.s3 (+ third-party)        │
└──────┬───────────────────────────────────────────────┬───────────────┘
       │ HTTP, signed                                  │ modules directly,
┌──────▼───────────────────────────────┐               │ services only through
│ Services: own containers, untrusted  │               │ the egress proxy
│  e.g. lemmy (Python)                 │          Internet
└──────────────────────────────────────┘
```

Services reach the core and nothing else: not each other, and the internet only through the core's egress proxy. Without services there is one container.

### 5.3 Module or service

| | Module | Service |
|---|---|---|
| Trust | trusted, full rights in the core process | untrusted, only what the operator grants |
| Language | Go | any, speaks HTTP/JSON |
| Adding one | a line in `build.yaml`, rebuild the image | a block in `companion.yaml`, start the container, **no** rebuild |
| Source | Go module from Git or a local path | image, or Git/path with a Dockerfile |
| Secrets | gets only its own config block, but could read more | gets only its own config block, cannot reach more |
| Internet | directly | only through the core's egress proxy, only granted hosts |
| Data | own area in the DB and blob modules (§5.10) | own volume; from Stage 3 also the core's data and blob API |
| UI | web component inside the shell | sandboxed iframe |
| Calls | Go function call | HTTP through the core |
| Fits | core features, heavy jobs, authors you trust | foreign authors, other languages, experiments |

The SDK lets one code base ship as both (§5.12).

### 5.4 Capabilities

A capability is defined once, as a Go interface. `hpc gen` derives everything a service needs from it, so the core can connect any combination: module to module by function call, anything involving a service over HTTP.

```go
// package social – capability social.publisher/v1
type Publisher interface {
    Describe(ctx context.Context) (Limits, error)          // incl. RequiresImage, LinkCard
    Preview(ctx context.Context, p Post) (Rendered, error) // what the post would look like, without sending
    Publish(ctx context.Context, p Post) (Result, error)
    ResolveMention(ctx context.Context, profileURL string) (Mention, error)
}

type Post struct {
    ItemID string    `json:"itemId"`
    Text   string    `json:"text"`
    Images []Image   `json:"images"`         // may be empty (blog post); Image.URL points to a core blob
    Link   *LinkCard `json:"link,omitempty"` // URL, title, description, preview image
}
```

Generated from it: a Go client adapter (a `social.Publisher` that POSTs to a service), a Go server adapter (lets a service call a module's capability), OpenAPI and JSON Schema for service authors, TypeScript types for UIs.

Rules:
- The major version is part of the name. Within `v1`, only optional fields are added. A building block may offer `v1` and `v2` side by side.
- Binary data never travels in JSON; images are short-lived blob URLs of the core.
- `Companion-Instance: main` tells a service which of its instances is meant.
- Third parties define their own capabilities in their own package as `x.<author>.<name>/v1`.

As built (`cmd/hpc/gen*.go`, `companion/wire`): `hpc gen` reads the capability packages with `go/types` and writes `wire_gen.go` into each: a client (an implementation of the capability that posts to a provider), a server (answers the operations for an implementation) and a measuring wrapper, which the core puts around every connection for the calls, errors and latency per capability and instance in `/metrics`. Optional operations are interfaces named in `NewCapability(…, companion.Optional[social.Lister]())`; client and wrapper keep exactly the ones a provider offers visible to type assertions. A capability's sentinel errors travel as codes (`ErrRateLimited` as `rate_limited`) and come back as themselves. OpenAPI 3.1 goes to `docs/services/openapi/`, TypeScript types to `@companion/ui/capabilities/<package>`; a test fails when they are not up to date. `storage.*` stays Go only (`//hpc:local`). The protocol around them is `docs/services/protocol.md`.

Starting set, derived from the code:

| Capability | Operations | Provided by | Used by |
|---|---|---|---|
| `feed.source/v1` | `listItems`, `getItem`; every item is a `photo` or an `article` | `rss` | `autoupload`, `interactions` |
| `social.publisher/v1` | `describe`, `preview`, `publish`, `resolveMention`, optional `listPosts` | `mastodon`, `bluesky`, `pixelfed`, `instagram`, `threads` | `autoupload`, `microblog` |
| `social.interactions/v1` | `getStats`, `listReplies` | the same five | `interactions`, `microblog` |
| `geo.routing/v1` | `route` | `routing` | `trips` |
| `notify/v1` | `send`, `broadcast` | `webpush` | any building block with an optional `notify` slot |
| `media.image/v1` | `resize`, `readExif` | `media` (from `imageresize`, `imagemeta`) | `autoupload`, `microblog`, services in any language |
| `storage.sql/v1` | handle to the own area, migrations, backup | `db.sqlite` (later `db.postgres`) | core and every module, Go only |
| `storage.blob/v1` | read (also ranges), write, list, delete, signed URL, resumable multipart upload | `blob.fs` or `blob.s3` | core and every module; services through the core |

### 5.5 Connections

Every building block declares its slots: name, capability, single or multiple, required or optional. The operator assigns an instance to each slot. The core checks at load time that the instance exists and offers the capability in a compatible version, and refuses otherwise.

```yaml
# companion.yaml – runtime config (the build list is build.yaml, §5.6)
modules:
  rss:
    instances:
      photos: { url: https://example.com/gallery/index.xml }
      blog:   { url: https://example.com/posts/index.xml }
  mastodon:
    instances:
      main: { instance: ${MASTODON_INSTANCE}, token: ${MASTODON_PAT} }
  autoupload:
    instances:
      photos-mastodon:
        source: rss:photos          # slot source → module
        target: mastodon:main       # slot target → module
        strategy: rotate
        cron: "0 0 18 * * *"
      photos-lemmy:
        source: rss:photos
        target: lemmy:main          # slot target → service
        strategy: rotate
        cron: "0 30 18 * * *"
  microblog:
    config:
      publishTo: [mastodon:main, lemmy:main]

services:
  lemmy:
    from: oci://ghcr.io/someone/hpc-lemmy:0.3.1
    grants:
      egress: [lemmy.world]
      events: [social.post.published/v1]
    instances:
      main: { instance: https://lemmy.world, token: ${LEMMY_TOKEN} }
```

- A config change rebuilds the affected instances and swaps them, like Caddy. Only adding a *module* needs a rebuild.
- Events are the second kind of connection. Modules subscribe through their manifest; a service receives an event only if `grants.events` lists it.
- **Notifications instead of rules.** There is no rule engine. A building block that knows something worth telling offers an optional `notify` slot (`autoupload`: failed uploads; `webmention`: new mentions; `instagram`: token about to expire), and the operator connects it, e.g. to a `webpush` instance that only targets their own devices.
- A connection page in the UI is not part of this concept (§9); connections live in the config.

### 5.6 Loading and building

`build.yaml` says what is compiled in, `companion.yaml` what runs and how it is connected. A config change never triggers a rebuild.

```yaml
# build.yaml
core: v1.4.0
modules:
  - github.com/LNA-DEV/HomePageCompanion/modules/db-sqlite@v1.0.0   # required: exactly one DB module
  - github.com/LNA-DEV/HomePageCompanion/modules/mastodon@v1.3.0
  - github.com/LNA-DEV/HomePageCompanion/modules/autoupload@v1.1.0
  - github.com/someone/hpc-pixelfed-stories@v0.2.0                   # someone else's module
  - example.com/trips => ./modules/trips                            # local path
```

1. `hpc build` generates `main.go` (one import per module) and `go.mod` (`require` for Git, `replace` for paths).
2. It builds the shell and every module UI and embeds them with `embed.FS`.
3. `go build` produces one program. `go.sum` pins every version; for public repositories Go also checks `sum.golang.org`, so a moved tag is noticed.

```dockerfile
FROM ghcr.io/lna-dev/companion-builder:1.4 AS build
COPY build.yaml ./
COPY modules/ ./modules/
RUN hpc build -o /out/companion

FROM gcr.io/distroless/base-debian12
COPY --from=build /out/companion /companion
ENTRYPOINT ["/companion"]
```

Prebuilt images (`companion:full` with today's features, `companion:maps`) cover the common cases. If two modules need incompatible versions of one library, the build fails and `hpc build` names both; that is the price of the Caddy style.

**Repository layout.** Own modules live in this repository under `modules/<name>/`, each with its own `go.mod`, versioned by tags `modules/<name>/vX.Y.Z`. Core internals live under `core/internal/`, which modules cannot import. Third-party modules live in their authors' repositories. The core module path has to match the layout for `go get` to work; today it does not (§2, §10).

**Services** come from `oci://` (image), `git+https://…@tag` (Dockerfile in the repository) or `path:` (local directory). `hpc apply` writes `compose.generated.yml` with the companion and every service, one network per service, and starts them; image digests and commits go to `companion.lock`. The core is not rebuilt for a service.

### 5.7 UI

The admin shell provides the frame: login, navigation, dashboard grid, logs. Everything feature-specific comes from the building block. The boundary is a web component; what it uses inside (Svelte, Lit, nothing) is the author's choice.

| Extension point | What the building block provides | Example |
|---|---|---|
| `nav` | sidebar entry pointing to a page | "Trips" |
| `page` | page under `/b/<id>/…` | trip editor |
| `widget` | dashboard card, size S, M or L | "Next upload", token expiry |
| `settings` | settings per instance; without its own UI, the shell generates a form from the config schema | Mastodon account |
| `action` | button on an instance | "Refresh token now" |
| `public` | files for the website | like button script |

- **Module UI** is embedded with `//go:embed ui/dist`. `entry.js` registers custom elements (`trips-page`, `trips-next`); the shell loads it from `/ui/b/<id>/entry.js` and places them. Svelte compiles components to custom elements, so today's pages carry over.
- **Service UI** is served by the service under `/ui/` and loaded into `<iframe sandbox="allow-scripts allow-forms">` **without** `allow-same-origin`: an opaque origin with no access to the shell's cookies, storage or DOM. Size, navigation and API calls go through `postMessage`.
- **One SDK** (`@companion/ui`) hides the difference: `connect(this)` gives an authenticated `api`, `navigate` and the theme tokens, directly for modules and over the bridge for services.
- The shell is embedded in the core binary as well. Its nginx container goes away: static files, the SPA fallback and the `/_app/` cache header move into the core, the `/api` proxy becomes unnecessary.

Today's pages move to their building blocks: Dashboard, Logs, Login → shell; Feeds → `rss`; Publications, Uploads, Connections, Interactions → the new `autoupload` pages (`autoposting-ui.md` §4); Microblog → `microblog`; Subscribers, Broadcast → `webpush`; Webmentions → `webmention`; Trips → `trips`; Basemap → `basemap`.

### 5.8 Trust and rights

**Modules** are trusted by being listed in `build.yaml`. In one process there is no real boundary, so the core keeps order instead: each instance gets only its own config block, secrets are removed from the environment after loading (`os.Unsetenv`), fields marked secret are masked in logs and UI, and `hpc build` shows the source and new dependencies of every added or updated module.

**Services** are never trusted:

| Right | Effect | Default |
|---|---|---|
| offered capabilities | appears as a provider for slots | on |
| slots | may call capabilities, only on the instances the operator connected | connected only |
| `events` | receives the listed events | off |
| `egress` | internet through the core's egress proxy, listed hosts only | off |
| `public` | own public routes under `/api/b/<id>/public/*` | off |
| `volume` | own data directory | on |

Never possible: S3 credentials, Docker access, other services, other building blocks' secrets. Enforcement: each service sits in its own internal Docker network (`internal: true`) with only the companion attached; internet only through `HTTP_PROXY=http://companion:3128`; every call carries the service's own token and is checked against grants and connections; the core signs its requests to the service; containers run non-root with a read-only root file system, `cap_drop: [ALL]`, `no-new-privileges` and memory and PID limits. `hpc apply` shows the requested rights and applies them only after confirmation, also on every update that asks for more.

**Admin login and gateway.** The admin key moves from `localStorage` (`web/src/lib/api.ts:9`) to a session cookie (`HttpOnly`, `SameSite=Strict`). Module UIs use it implicitly; service iframes never see it and get a short-lived token valid only for `/api/b/<service>/…`. The gateway sets `SetTrustedProxies` (§3.4).

**Connecting accounts (OAuth).** The core runs the flow (redirect, callback at `/api/b/<id>/oauth/callback`, `state` and PKCE, code exchange); the module only names the authorisation URL and scopes, and registers an app first where the platform requires it (Mastodon, Pixelfed). Tokens go to the secret store (§5.10) and the module refreshes them itself. Instagram and Threads need a Meta app once (client ID and secret in `.env`). Where a platform has no OAuth, the token field in the settings form stays.

As built (`companion/oauth.go`, `core/oauth.go`): an instance implements `companion.OAuthConnector`; the settings page asks `POST /api/b/<id>/oauth/start` for the authorisation URL. The session cookie is `SameSite=Strict`, so the platform's redirect back arrives without it: the callback is authenticated by the `state` alone, which only a logged-in operator can create, works once and lives 15 minutes. The callback answers a page of its own that moves on to the settings page, because a redirect would still count as cross-site and lose the cookie. The redirect URI is `core.publicUrl` + `/api/b/<id>/oauth/callback`; the settings page shows it for registering with Meta. A connected account takes the place of the configured token; disconnecting brings the configured one back. Mastodon and Pixelfed share `lib/fediauth`, Instagram and Threads hand the long-lived token to `lib/metatoken`, where a new token in `.env` still replaces the connection. Bluesky keeps its app password (§10.1).

### 5.9 Runtime

| Aspect | Module | Service |
|---|---|---|
| Lifecycle | `Provision` → `Validate` → `Start` → `Stop`; on a config change a new instance is built and swapped in | container start, `GET /health` until ready, `POST /lifecycle/configure`; `SIGTERM` and 30 s on stop |
| Own API | gin group `/api/b/<id>/` | the same path, forwarded by the core |
| Jobs | Go function with context, `recover` and timeout; never twice at once; "run now" in the UI | `POST /jobs/<name>`, otherwise the same |
| Events | in-process bus with an outbox in the core's database: at least once, retried | `POST /events`, deduplicated by event ID |
| Failures | `recover` and timeout around every job, handler and event; a memory leak still hits the whole process | a crash stays in the container; CPU and memory limits per service |
| Logs | shared logger with `module=<id>` | stdout, read by the core through Docker |

As built (`core/services.go`, package `service`): the core plans a service with the manifest `companion.lock` recorded, so a service that is down changes nothing but its own instances: they count as not running, calls to them fail as transient errors, and the core configures the service again as soon as it answers, also after a restart it notices through the configuration version `/health` reports. Instance jobs come from the manifest or from the answer to the configuration, because a module's schedule often is one of its config fields. Services reach the core on a listener of their own (`core.servicesListen`, `:3128`, never published), which the egress proxy shares. Logs: the SDKs send their lines to `/core/v1/logs` besides stdout, instead of the core reading them through Docker, which would need the Docker socket in the companion. `service.Serve(info)` runs any module as a service: its database is a SQLite file in the volume, its slots, secrets and events go through the core; the integration tests run the `mastodon` module that way, connected to `autoupload`.

Today's public URLs stay as aliases: `/api/tiles/basemap/*` → `basemap`, `/api/tiles/gbif/*` → `gbif`, `/api/interactions/*` → `interactions`, `/api/webmention` → `webmention`, `/api/webpush/*` → `webpush`, `/api/microblog/*` → `microblog`, `/api/trips/*` → `trips`. The core refuses to start if two aliases collide.

### 5.10 Storage and operations

**Database: exactly one DB module.** All modules store through one DB module, and exactly one may be active, like Caddy's `storage` module. The core has the required slot `database` (`storage.sql/v1`).
- Each module gets a handle to its own area and registers its migrations there; the core keeps its event outbox and job history there too.
- `db.sqlite` keeps one file per module (`data/db/<id>.db`) and backs them up with `VACUUM INTO`. Removing a module removes its file.
- Modules write portable SQL through GORM, so a `db.postgres` (one schema and one role per module) can follow without rework. No transactions across modules. The DB module starts first; without it the core does not start.
- Services get no handle. Their default is their own volume; from Stage 3 the core offers a data API (`/core/v1/data/<collection>/<id>`, JSON documents, own area only) that stores through the DB module and therefore lands in the same backup.

**Files: exactly one blob module** (`blobs` slot, `storage.blob/v1`), `blob.fs` (default, `data/blobs/`) or `blob.s3` (any S3-compatible store).
- Only the blob module knows the S3 credentials. Each module gets a handle to `<prefix>/<id>/`, as `storage.Prefix` already does. The interface is essentially today's `basemap.Store` with its S3 and directory variants, plus simple writes, listing and signed URLs.
- Microblog and trip images move from the local folders into the blob store (§5.11) and are still served through their module's public routes.
- Short-lived core files, such as images for `publish`, live under `core/tmp/` and are deleted after a day.
- Pure caches such as the GBIF tiles stay out of the blob store: the SDK gives every module `CacheDir()` on local disk with a size limit the core enforces.
- Services get no keys; from Stage 3 the core streams `/core/v1/blobs/<key>` or hands out signed URLs for large files, own area only.

**Secret store.** The core stores secrets created at runtime, encrypted and separated per module, with one key `COMPANION_SECRET_KEY` from `.env`, through the DB module: refreshed tokens (today `metatoken` with `TOKEN_ENCRYPTION_KEY`), OAuth tokens, the VAPID private key. SDK: `ctx.Secrets().Get/Put/Delete`; services from Stage 3 through `/core/v1/secrets/<name>`. What the operator types in, a token via `${VAR}`, stays in `.env`. `hpc secrets rotate` re-encrypts everything.

**Backup and restore.** `hpc backup` writes the whole state to one file, `hpc restore` brings it back; no module has to do anything for it.

| Part | How |
|---|---|
| DB module | `VACUUM INTO` per module file (`pg_dump` with `db.postgres`) |
| Secret store | as part of the database, encrypted |
| Blob module | the files with `blob.fs`; optional with `blob.s3`, whose buckets usually version on their own |
| Service volumes | as an archive, the service paused briefly |
| Config | `companion.yaml`, `build.yaml`, `companion.lock`; `.env` only with `--include-env` |

A core job can do this on a schedule and put the result under `core/backups/` in the blob store. `hpc restore --module <id>` restores one module. `hpc migrate` and every module migration take a backup first.

**Metrics.** The core measures without help from the modules: duration and result of every job, calls, errors and latency per capability and instance, event delivery (open, retried, given up), HTTP requests per building block, state and restarts of services. Modules add their own values through the SDK (uploads per platform, days left on a token). Output: `/metrics` in Prometheus format (admin only) and a dashboard widget with the last day.

### 5.11 Moving an existing installation: `hpc migrate`

`hpc migrate` moves an installation in one go and takes a backup first; `hpc migrate --check` shows every step without changing anything.

1. **Config:** `src/data/config.yaml` becomes `companion.yaml` and `build.yaml`. `targets[]` become instances of the platform modules, `connections[]` instances of `autoupload` with `source`, `target` and `strategy: rotate`, `microblog.publishTo` the multiple slot. `build.yaml` lists only the modules in use.
2. **Database:** the 21 tables of `companion.db` are split into the DB module's files.
3. **Files:** images from `data/microblog/` and `data/trips/` move into the blob store. Their public URLs (`/api/microblog/media/<sha>`, `/api/trips/media/<sha>`) stay, because the modules keep serving them under those paths.
4. **Secrets:** refreshed Meta tokens from `platform_tokens` and the VAPID key move into the secret store.

Later updates bring their own migrations: database migrations through the DB module, and config migrations that lift an old config block to the new schema version at startup. The core records the version per module and runs each migration once, after an automatic backup.

### 5.12 Developer side

```go
package autoupload

func init() {
    companion.Register(companion.Info{
        ID:  "autoupload",
        New: func() companion.Instance { return new(Upload) },
        Slots: []companion.Slot{
            {Name: "source", Needs: feed.SourceV1},
            {Name: "target", Needs: social.PublisherV1},
        },
        Jobs: []companion.Job{{Name: "publish", ScheduleFrom: "cron"}},
    })
}

type Upload struct {
    Source feed.Source      `companion:"slot=source"` // connected by the core
    Target social.Publisher `companion:"slot=target"` // module or service
    Cron   string           `json:"cron"`
}
```

The same module ships as a service without changes: `func main() { service.Serve(autoupload.Info) }`.

| Command | Purpose |
|---|---|
| `hpc gen` | adapters, OpenAPI and TypeScript types from the capabilities |
| `hpc dev` | module: rebuild and restart on change; service: container with the source mounted; UI with hot reload |
| `hpc build` / `hpc apply` | build the companion (§5.6), start the services |
| `hpc test` | the tests of §6 for one building block, including the conformance suite of its capabilities |
| `companiontest` | Go package with a test core: fakes in slots, events injected, jobs triggered |

## 6. Tests

Every stage is built with tests, and the CI only lets through what passed them. Today the CI builds and pushes images without running a single test, and two of the three critical findings of §3 sit in packages without any (`inventory`, `database`). The existing tests are green.

| Level | What | Tool | When |
|---|---|---|---|
| Unit | logic in core and modules: caption cutting, choosing the next post, strategies, health rating, config validation, cron expressions | `go test`, table tests as in `autouploader` today | every push |
| Contract | one conformance suite per capability; every provider, module or service, has to pass it. Platform APIs run against recorded responses | `companiontest.Conformance`, `httptest`, golden files | every push |
| Module | one module against the test core: fakes in slots, events injected, jobs triggered, DB module with in-memory SQLite | `companiontest` | every push |
| Migration | `hpc migrate`, DB and config migrations against an empty database and against frozen states of earlier versions | `go test` with fixture databases | every push |
| Integration | core with all standard modules as one program, local test feed, fake platforms; failures: feed gone, 429, 503, expired token, restart in the middle of a job | `go test -tags integration` | every pull request |
| UI | shell and module UIs in a browser against the integration setup: login, posts page, filters, search, "run now", mobile; screenshots as artefacts | Playwright, as in the run behind `autoposting-ui.md` | every pull request |
| Service | an example service in a container against the core, including rights: calls without a grant and egress to hosts not granted must fail | Docker Compose in CI | from Stage 3, every pull request |
| Smoke | `/health` and one public endpoint per alias | `curl` in the deploy step | after every deploy |

Rules:
- **Test first, then fix.** Every bug fix starts with a test that reproduces the bug. For Stage 0: feed unreachable → process keeps running; empty database → startup works; every cron expression is parsed and its frequency checked.
- **No real platforms in automated tests.** External APIs run against `httptest` servers or recorded responses; no secrets in tests. Real test accounts only by hand, `hpc test --live`.
- **Fakes per capability.** `companiontest` ships a fake for every capability, e.g. a publisher that stores posts and returns a chosen error on request.
- **Reproducible test data.** A generator for test feeds (photos and blog posts) and seed data (publications, failed attempts, likes) lives under `testdata/`.
- **The same for third parties.** Module and service templates come with unit, contract and module tests. A building block counts as compatible once it passes the conformance suite of its capabilities, which every author can run with `hpc test`.

CI gates:
1. Every push and pull request: `go vet`, `go test -race ./...`, `svelte-check`, contract and migration tests. Red means no merge.
2. Every pull request in addition: integration and UI tests, screenshots as artefacts.
3. Images are built and pushed only from `main` and only after green tests.
4. Instead of a coverage percentage: every package with logic has tests. A package without a test file passes only if it is on a short exception list, e.g. `models`.

As built: the integration setup is `e2e/world`, every standard module as one program, a generated test feed (60 photos with real JPEGs, 8 blog posts) and fake Mastodon, Pixelfed, Bluesky, Instagram, Threads and push servers that fail on request (status, expired token, a call that hangs). Its seed uses real runs: posts on every network, one open problem per cause, likes, microblog posts, webmentions, subscribers. `go test -tags integration ./...` in `e2e/` runs the scenarios, among them a restart and a `kill -9` in the middle of a post, against a separately built companion (`e2e/cmd/companion`). `e2e/cmd/uiserver -world` serves the same setup to Playwright (`web/tests/ui`, `npm run test:ui`). Both run on every push and pull request; the image waits for them.

## 7. Build order

**Stage 0: module boundaries in the monolith (no new deployment)**
1. CI runs `go vet`, `go test`, `svelte-check` and blocks on red (§6).
2. The fixes of §3, each with a test that reproduced it first.
3. Go interfaces for the capabilities; the three `switch target.Platform` become one `Publisher` interface with five implementations.
4. `main.go` becomes a registry; config blocks and tables separated per module; file access through a store interface (today's `basemap.Store`) instead of `data/` directly.

Gate: `main.go` imports no feature package directly, all tests green.

**Stage 1: core, builder and storage**
1. Core package, `build.yaml`, `hpc build`, builder image; shell embedded, nginx container gone.
2. Every current feature as its own module, connected through `companion.yaml`.
3. DB module `db.sqlite`, blob module (`blob.fs`, `blob.s3`), secret store, `hpc backup` and `restore`.
4. `hpc migrate`; `SetTrustedProxies`.

Gate: one container; an existing installation migrated without data loss; the website unchanged.

**Stage 2: UI per building block**
1. Shell with the extension points `nav`, `page`, `widget`, `settings`, `action`, `public`.
2. Today's pages as custom elements in their modules; `@companion/ui`.
3. Session cookie instead of the key in `localStorage`; OAuth account connection.
4. Preview before posting; metrics under `/metrics` and as a widget.

Gate: the shell contains no feature page.

**Stage 3: services**
1. `hpc gen` (adapters, OpenAPI), `hpc apply` with Compose, egress proxy, grants.
2. Service UI in the sandboxed iframe with the `postMessage` bridge.
3. Data, blob and secret API for services; `media.image/v1` as a capability.
4. A first service as proof, e.g. `lemmy` in Python.

Gate: a service in another language offers `social.publisher/v1` and can be connected to `autoupload`.

**No commit, no deploy** unless asked.

## 8. Decisions

All taken on 2026-10-20.

| # | Question | Decision |
|---|---|---|
| D1 | Architecture | Hybrid (§4 F): modules compiled in, services as containers |
| D2 | How capabilities are defined | Go interfaces; `hpc gen` derives adapters, OpenAPI and TypeScript types. Rejected: Protobuf with Connect (clumsier types in Go), OpenAPI first (module authors would work against generated code) |
| D3 | Who starts services | `hpc apply` with Compose. Installation by click through the Docker API is out of scope |
| D4 | How modules are added | rebuild with `hpc build`; prebuilt images for common sets |
| D5 | UI technology | web components, free inside; the template uses Svelte |
| D6 | UI extension points | only the shell's at first (§5.7); own points can be added later without breaking anything |
| D7 | Rules (event → action) | no rule engine; optional `notify` slots (§5.5). A `rules` module remains possible later |
| D8 | Module data | exactly one DB module, first `db.sqlite` with one file per module |
| D9 | Service data | own volume; from Stage 3 also the core's data and blob API |
| D10 | Repositories for own modules | in this repository under `modules/`, one `go.mod` each |
| D11 | Files | exactly one blob module, `blob.fs` or `blob.s3` |
| D12 | Existing installations | `hpc migrate` with backup and `--check` |
| D13 | Secrets created at runtime | secret store in the core |
| D14 | Backups | `hpc backup` and `hpc restore` |
| D15 | Account connection | OAuth flow in the core |
| D16 | Preview | `preview` in `social.publisher/v1` |
| D17 | Image processing | capability `media.image/v1`, so services can use it |
| D18 | Tests | the levels and gates of §6; no bug fix without a test that reproduced it |

## 9. Not in this concept

A registry with a "discover" page; templates `hpc new module` and `hpc new service`; a connection page with a graph view; a consent dialog in the UI (the CLI shows rights instead); a guide for third-party authors. Third-party modules and services still work: through a Git URL, a path or an image. Also out: WebAssembly modules, multiple operators per installation, a rule engine (D7), extension points defined by building blocks (D6), `db.postgres` (possible later, D8).

## 10. Not checked yet

| What | Why not | How |
|---|---|---|
| Whether `go get` can resolve the core as it is laid out | `src/go.mod` declares the repository root as module path, without a `go.mod` there; not tried | `go get github.com/LNA-DEV/HomePageCompanion@8c01664` from an empty module; fix the module path or the layout in Stage 0 |
| Memory per service container (10–30 MB assumed) and build time of `hpc build` | estimates, nothing built yet | measure the first service and the first `hpc build` in Stage 1 and 3 |
| ~~Threads text posts and link previews, Bluesky link cards, OAuth for Bluesky and Meta~~ | checked 2026-10-21, §10.1 | |
| Real data: the live gallery feed and a production `companion.db` | `lna-dev.net` is not reachable from the sandbox; production needs the admin key | run `hpc migrate --check` against a copy of the production `data/` before Stage 1 |
| Whether openresty sets or appends `X-Forwarded-For` | host config, outside this repository | as in `webmention.md` §14; decides the `SetTrustedProxies` value |

### 10.1 Platform behaviour, checked 2026-10-21

Read from the official documentation sources: Mastodon's and atproto's documentation repositories, Pixelfed's and Laravel Passport's source, Meta's developer pages and its `fbsamples/threads_api`. The documentation sites themselves did not resolve from the sandbox.

| Platform | Account connection | Tokens | Notes |
|---|---|---|---|
| Mastodon | `POST /api/v1/apps` (`client_name`, `redirect_uris`, `scopes`), then `/oauth/authorize` and `/oauth/token`; PKCE `S256` and `/.well-known/oauth-authorization-server` from 4.3 | do not expire, no refresh token; `/oauth/revoke` | the app is registered with every scope it later asks for: `read:accounts read:statuses write:media write:statuses` (`profile` would do for the account, but only from 4.3) |
| Pixelfed | `POST /api/v1/apps` too; `redirect_uris` is a newline-separated string, `scopes` is ignored; Laravel Passport behind `/oauth/authorize` and `/oauth/token` | expire after 365 days (`OAUTH_TOKEN_DAYS`), with a refresh token (400 days); no revoke endpoint | only `read` and `write`, no granular scopes; PKCE optional |
| Bluesky | atproto OAuth: `client_id` is the URL of a client metadata document, PAR, DPoP with server nonces on the token endpoint and on every PDS request, PKCE `S256`; a confidential client authenticates with `private_key_jwt` against a published JWKS | access tokens under 30 minutes, single-use refresh tokens; public clients limited to two weeks | app passwords are still supported. Link cards: the client fetches the page and uploads the thumbnail itself (`app.bsky.embed.external`, thumb up to 1 MB); posts 300 graphemes |
| Instagram | Instagram API with Instagram Login: `https://www.instagram.com/oauth/authorize`, scopes comma-separated (`instagram_business_basic`, `instagram_business_content_publish`, …), code at `https://api.instagram.com/oauth/access_token`; the Instagram app's ID and secret, not the Meta app's | the code gives a 1-hour token, `ig_exchange_token` a 60-day one, `ig_refresh_token` renews it after 24 hours | the redirect URI must be registered exactly in the dashboard; PKCE not documented; the documented answer wraps the token in `data: [...]` |
| Threads | `https://www.threads.com/oauth/authorize`, code at `https://graph.threads.net/oauth/access_token`; the Threads app's ID and secret | `th_exchange_token` gives 60 days, `th_refresh_token` renews it | https redirects only, never localhost; deleting needs `threads_delete`. Text posts: `media_type=TEXT`, 500 characters (emoji counted by UTF-8 bytes); without `link_attachment` the first URL becomes the preview; at most 5 links per post |

**Bluesky stays on app passwords for now.** Its OAuth does not fit the model above, where a module names URLs and scopes: it needs PAR, DPoP proofs (with nonce retries) on every request of the Bluesky client, a client metadata document and a JWKS served from `core.publicUrl`, and `private_key_jwt` instead of a client secret. That is a client of its own, not a few hooks; it waits for a decision.
