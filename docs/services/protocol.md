# Service protocol

A service is a building block in its own container, written in any language
that speaks HTTP and JSON (docs/concepts/modular-architecture.md §5.3). This
page is what such a service has to answer and what it may call. Go modules
get all of it from the SDK: `func main() { service.Serve(info) }`
(package `service`).

## Two directions

| | From | To | Authentication |
|---|---|---|---|
| core → service | the core | the service on its port (8080 unless the manifest says otherwise) | every request but `GET /health` and `GET /manifest` carries `Companion-Signature` |
| service → core | the service | `COMPANION_URL` (`http://companion:3128` next to the companion) | `Authorization: Bearer <token>` |

The token is the service's own. The core creates it and writes it to
`data/services/<block>/run/token`; `hpc apply` mounts that directory
read-only at `/run/companion/`, so the service reads `/run/companion/token`
(or the file `COMPANION_TOKEN_FILE` names). The same token keys the
signatures, so a service can tell the core's requests from anyone else's.

### The signature

```
Companion-Signature: t=<unix seconds>,v1=<hex HMAC-SHA256(token, message)>
message = t "\n" METHOD "\n" path-with-query "\n" Companion-Instance header "\n" hex(SHA-256(body))
```

Refuse a request whose signature does not match or whose `t` is more than
five minutes away from your clock.

## What a service answers

| Request | Answer |
|---|---|
| `GET /manifest` | the manifest (below); also printed to stdout when started with `COMPANION_MANIFEST=print`, which is how `hpc apply` reads it |
| `GET /health` | `200 {"status": "ok", "configVersion": "<version of the configuration you hold, \"\" before the first>"}`. The core asks every 10 s and configures the service again whenever the version differs, also after a restart |
| `POST /lifecycle/configure` | the instances and their config, see below; answer `200 {"errors": {"<instance>": "…"}, "jobs": {"<instance>": [{"name", "schedule", "timeout"}]}}` |
| `POST /capabilities/<capability>/v<major>/<operation>` | one operation of an offered capability for the instance in `Companion-Instance`; bodies and answers as in `docs/services/openapi/<capability>.v<major>.json` |
| `POST /jobs/<name>` | run a job for the instance in `Companion-Instance` (none for a module job); `2xx` when it worked. The core never runs the same job twice at once |
| `POST /events` | an event, `{"id", "type", "source", "time", "data"}`, delivered at least once: deduplicate by `id`. Only events the service subscribes to and the operator granted |
| `POST /lifecycle/pause`, `/lifecycle/resume` | before and after a backup copies the volume; flush and stop writing in between. Optional |
| `ANY /api/…` | the service's admin API, reachable for the admin as `/api/b/<block>/…` |
| `ANY /public/…` | its public API, `/api/b/<block>/public/…`, only with the `public` grant |

Errors of capability operations are `{"error": {"code", "message", "cause", "status"}}`:
`code` is one of the capability's codes (`rate_limited`, `not_a_profile`, …,
listed in its OpenAPI file) or anything else, `cause` your own
classification (`account`, `content` or `transient`) and `status` the HTTP
status the platform behind you answered, if any.

### The manifest

```json
{
  "id": "lemmy",
  "name": "Lemmy",
  "version": "1.0.0",
  "port": 8080,
  "provides": [{"capability": "social.publisher/v1", "operations": ["describe", "preview", "publish", "resolveMention"]}],
  "slots": [{"name": "media", "needs": "media.image/v1", "optional": true}],
  "jobs": [{"name": "sync", "schedule": "0 */10 * * * *"}],
  "subscribes": ["social.post.published/v1"],
  "requests": {"egress": ["lemmy.world"], "public": false, "volume": true},
  "config": [{"name": "password", "type": "string", "secret": true}],
  "ui": {"nav": [], "pages": []}
}
```

`operations` lists the required operations and whichever optional ones you
implement (`listPosts`, `delete` for `social.publisher/v1`). A job either
has a fixed `schedule` (cron with seconds), names the instance config field
holding one (`scheduleFrom`), or comes with the answer to `configure`.
`"module": true` makes a job run once for the service instead of per
instance. `requests` are the rights you ask for; what you get is what the
operator grants (§5.8).

### Configure

```json
{
  "version": "3f9c…",
  "service": "lemmy",
  "publicUrl": "https://companion.example.com",
  "instances": {
    "main": {
      "config": {"server": "https://lemmy.world", "community": "photography"},
      "slots": {"media": ["media:default"]},
      "targets": {"media:default": ["resize", "readExif"]}
    }
  }
}
```

`targets` lists the operations each connected instance offers. Keep the
instances whose config did not change running.

## What a service may call

| Request | Does |
|---|---|
| `POST /core/v1/slots/<slot>/<operation>` | an operation on an instance connected to one of your slots, with `Companion-Instance` (yours) and, when several are connected, `Companion-Target` (theirs); bodies as in the capability's OpenAPI file |
| `GET /core/v1/slots` | your instance's connections |
| `GET`, `PUT {"value"}`, `DELETE /core/v1/secrets/<name>` | your part of the core's encrypted secret store (refreshed tokens) |
| `GET`, `PUT` (JSON), `DELETE /core/v1/data/<collection>/<id>`; `GET /core/v1/data/<collection>?prefix=&after=&limit=` | your JSON documents (at most 1 MiB each), in the core's database and so in every backup |
| `GET` (with `Range`), `HEAD`, `PUT`, `DELETE /core/v1/blobs/<key>`; `GET /core/v1/blobs?prefix=` | your files, in the blob store under `services/<block>/` |
| `POST /core/v1/uploads` `{"key", "contentType"}`, `PUT /core/v1/uploads/<id>/<n>?key=`, `POST …/<id>/complete` `{"key", "parts"}`, `DELETE …/<id>?key=` | large files in parts |
| `POST /core/v1/blob-urls` `{"key", "ttl"}` | a signed public URL of one of your files, to hand to a platform (at most a week) |
| `POST /core/v1/temp?ttl=` (the file as the body) | a short-lived file and its URL, at most a day |
| `GET /core/v1/fetch?url=` | the file behind one of the core's own URLs (an image of a post you are to publish): you cannot reach the companion's public address, and need no egress for it |
| `POST /core/v1/events` | `{"type", "data"}`: emit an event, delivered like a module's |
| `POST /core/v1/logs` | `{"lines": [{"level", "message", "instance"}]}`: lines for the admin UI's log, besides your stdout |
| `POST /core/v1/metrics` | `{"values": [{"name", "value", "kind": "add" or "set", "labels"}]}`: your own values in `/metrics` |

Nothing else is reachable: not the other services, not the core's admin API,
and the internet only through the egress proxy (`HTTP_PROXY`), for the hosts
the operator granted.

## Running next to the companion

`hpc apply` starts the service with these variables; an SDK reads them:

| Variable | Value |
|---|---|
| `COMPANION_URL` | `http://companion:3128`, the core's API for services |
| `COMPANION_TOKEN_FILE` | `/run/companion/token` |
| `COMPANION_DATA` | `/data`, the volume (with the `volume` grant) |
| `HTTP_PROXY`, `HTTPS_PROXY` (and lower case) | `http://companion:3128`, the egress proxy |
| `NO_PROXY` | `companion` |
| `PORT` | the manifest's port, when it is not 8080 |

The container runs as a user other than root, on a read-only file system with
`/tmp` in memory, without capabilities. The proxy refuses hosts outside the
grant with `403`; outside Docker (a service run by hand, `url:` in
companion.yaml) it needs `Proxy-Authorization: Basic <service>:<token>`.

## The admin UI

A service with `ui` in its manifest (nav entries, pages, widgets as for a
module; `element` may be empty) answers `GET /ui/<file>`. The shell shows
`/ui/index.html?page=<page path>&path=<rest>` (or `?widget=<element>`) in a
sandboxed iframe, an opaque origin: no cookies, no storage, no access to the
shell. Talk to the shell with the SDK, one ES module the core serves:

```html
<script type="module">
  import { connect } from '/ui/sdk/companion-ui.js';
  const c = connect('lemmy');
  const posts = await c.api.get('posts');   // your /api/posts, through the shell
  c.toast('Saved', 'success');
  c.navigate('settings');                    // /b/lemmy/settings in the shell
</script>
```

Without the SDK the bridge is `postMessage` to `window.parent` with `'*'`:
`{type: "ready"}` first; then `{type: "api", id, method, path, body}` (answered
with `{type: "api", id, status, body}`), `{type: "navigate", to}`,
`{type: "toast", message, kind}`, `{type: "resize", height}`. The shell sends
`{type: "init", page, path, widget, dark, token}` and `{type: "theme", dark}`.
