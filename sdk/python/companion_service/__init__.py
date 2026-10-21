"""The companion's service protocol for Python (docs/services/protocol.md).

A service is a building block in its own container. This module answers
what the core asks (manifest, health, configure, capabilities, jobs, events,
its API and UI) and gives each instance a client for the core's API for
services (slots, documents, files, secrets, logs). Only the standard
library is used.

    from companion_service import Service, Error

    service = Service("lemmy", name="Lemmy", version="1.0.0")

    @service.instance
    class Lemmy:
        def __init__(self, ctx):
            self.server = ctx.config["server"]

        @service.operation("social.publisher/v1", "describe")
        def describe(self):
            return {"platform": "lemmy", "maxLength": 10000}

    service.run()
"""

from __future__ import annotations

import hashlib
import hmac
import inspect
import json
import logging
import mimetypes
import os
import posixpath
import queue
import re
import sys
import threading
import time
import traceback
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any, Callable

__all__ = ["Service", "Error", "Context", "Core", "Request", "Response"]

SIGNATURE_HEADER = "Companion-Signature"
INSTANCE_HEADER = "Companion-Instance"
TARGET_HEADER = "Companion-Target"
MAX_SKEW = 300
MAX_BODY = 64 << 20


class Error(Exception):
    """A failed operation as it travels: code (a capability's error code,
    such as "rate_limited" or "not_a_profile", or another one), message,
    cause (account, content or transient) and the platform's HTTP status."""

    def __init__(self, code: str, message: str = "", cause: str | None = None, status: int | None = None, http_status: int | None = None):
        super().__init__(message or code)
        self.code = code
        self.message = message or code
        self.cause = cause
        self.status = status
        self.http_status = http_status

    def to_json(self) -> dict:
        e: dict[str, Any] = {"code": self.code, "message": self.message}
        if self.cause:
            e["cause"] = self.cause
        if self.status:
            e["status"] = self.status
        return {"error": e}

    def response_status(self) -> int:
        if self.http_status:
            return self.http_status
        return {"bad_request": 400, "unsupported": 501, "internal": 500, "not_found": 404, "rate_limited": 429}.get(self.code, 422)


# ---- the core's side: signatures ----

def sign(key: bytes, method: str, uri: str, instance: str, body: bytes, now: int | None = None) -> str:
    """The Companion-Signature header the core sends (for tests)."""
    t = int(now if now is not None else time.time())
    msg = f"{t}\n{method}\n{uri}\n{instance}\n{hashlib.sha256(body).hexdigest()}".encode()
    return f"t={t},v1={hmac.new(key, msg, hashlib.sha256).hexdigest()}"


def verify(key: bytes, header: str, method: str, uri: str, instance: str, body: bytes, now: float | None = None) -> bool:
    parts = dict(p.strip().split("=", 1) for p in (header or "").split(",") if "=" in p)
    try:
        t = int(parts.get("t", ""))
    except ValueError:
        return False
    sig = parts.get("v1", "")
    if not sig or abs((now if now is not None else time.time()) - t) > MAX_SKEW:
        return False
    msg = f"{t}\n{method}\n{uri}\n{instance}\n{hashlib.sha256(body).hexdigest()}".encode()
    return hmac.compare_digest(sig, hmac.new(key, msg, hashlib.sha256).hexdigest())


# ---- the core's API for services ----

class Core:
    """The core's API for services (COMPANION_URL), with the service's token.
    Calls through slots name the calling instance."""

    def __init__(self, url: str, token: Callable[[], str], instance: str = ""):
        self.url = url.rstrip("/")
        self._token = token
        self.instance = instance
        # Straight to the core: HTTP_PROXY is for the internet.
        self._opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

    def for_instance(self, name: str) -> "Core":
        return Core(self.url, self._token, name)

    def request(self, method: str, path: str, body: bytes | None = None, headers: dict | None = None, raw: bool = False):
        req = urllib.request.Request(self.url + path, data=body, method=method)
        req.add_header("Authorization", "Bearer " + self._token())
        if self.instance:
            req.add_header(INSTANCE_HEADER, self.instance)
        for k, v in (headers or {}).items():
            req.add_header(k, v)
        try:
            with self._opener.open(req, timeout=120) as resp:
                data = resp.read()
                if raw:
                    return data, resp.headers
                return json.loads(data) if data.strip() else None
        except urllib.error.HTTPError as e:
            raise error_from(e.code, e.read()) from None

    def json(self, method: str, path: str, value: Any = None):
        body = None if value is None else json.dumps(value).encode()
        return self.request(method, path, body, {"Content-Type": "application/json"} if body is not None else None)

    # Slots: an operation on an instance connected to one of your slots.
    def call(self, slot: str, operation: str, args: Any = None, target: str | None = None):
        h = {"Content-Type": "application/json"}
        if target:
            h[TARGET_HEADER] = target
        return self.request("POST", f"/core/v1/slots/{q(slot)}/{q(operation)}", json.dumps(args if args is not None else {}).encode(), h)

    def call_raw(self, slot: str, operation: str, data: bytes, args: Any = None, target: str | None = None) -> bytes:
        """An operation with a binary argument (media.image/v1 resize)."""
        path = f"/core/v1/slots/{q(slot)}/{q(operation)}"
        if args is not None:
            path += "?args=" + urllib.parse.quote(json.dumps(args))
        h = {"Content-Type": "application/octet-stream"}
        if target:
            h[TARGET_HEADER] = target
        out, _ = self.request("POST", path, data, h, raw=True)
        return out

    # Documents, in the core's database.
    def get_document(self, collection: str, doc_id: str):
        try:
            return self.request("GET", f"/core/v1/data/{q(collection)}/{q(doc_id)}")["data"]
        except Error as e:
            if e.code == "not_found":
                return None
            raise

    def put_document(self, collection: str, doc_id: str, value: Any) -> None:
        self.json("PUT", f"/core/v1/data/{q(collection)}/{q(doc_id)}", value)

    def delete_document(self, collection: str, doc_id: str) -> None:
        self.request("DELETE", f"/core/v1/data/{q(collection)}/{q(doc_id)}")

    def documents(self, collection: str, prefix: str = "", limit: int = 100, after: str = "") -> dict:
        query = urllib.parse.urlencode({k: v for k, v in {"prefix": prefix, "limit": limit, "after": after}.items() if v})
        return self.request("GET", f"/core/v1/data/{q(collection)}?{query}")

    # Files, in the blob store.
    def put_blob(self, key: str, data: bytes, content_type: str = "application/octet-stream") -> None:
        self.request("PUT", "/core/v1/blobs/" + qpath(key), data, {"Content-Type": content_type})

    def get_blob(self, key: str) -> bytes | None:
        try:
            data, _ = self.request("GET", "/core/v1/blobs/" + qpath(key), raw=True)
            return data
        except Error as e:
            if e.code == "not_found":
                return None
            raise

    def delete_blob(self, key: str) -> None:
        self.request("DELETE", "/core/v1/blobs/" + qpath(key))

    def blob_url(self, key: str, ttl: int = 3600) -> str:
        return self.json("POST", "/core/v1/blob-urls", {"key": key, "ttl": ttl})["url"]

    def temp(self, data: bytes, content_type: str, ttl: int = 3600) -> str:
        return self.request("POST", f"/core/v1/temp?ttl={ttl}", data, {"Content-Type": content_type})["url"]

    def fetch_local(self, url: str) -> tuple[bytes, str]:
        """The file behind one of the core's own URLs."""
        data, headers = self.request("GET", "/core/v1/fetch?url=" + urllib.parse.quote(url, safe=""), raw=True)
        return data, headers.get("Content-Type", "")

    # Secrets, encrypted in the core.
    def get_secret(self, name: str) -> str | None:
        try:
            return self.request("GET", "/core/v1/secrets/" + q(name))["value"]
        except Error as e:
            if e.code == "not_found":
                return None
            raise

    def put_secret(self, name: str, value: str) -> None:
        self.json("PUT", "/core/v1/secrets/" + q(name), {"value": value})

    def delete_secret(self, name: str) -> None:
        self.request("DELETE", "/core/v1/secrets/" + q(name))

    def emit(self, event_type: str, data: Any) -> None:
        self.json("POST", "/core/v1/events", {"type": event_type, "data": data})

    def metric(self, name: str, value: float, kind: str = "add", **labels: str) -> None:
        self.json("POST", "/core/v1/metrics", {"values": [{"name": name, "value": value, "kind": kind, "labels": labels}]})


def q(s: str) -> str:
    return urllib.parse.quote(s, safe="")


def qpath(key: str) -> str:
    return "/".join(q(p) for p in key.split("/"))


def error_from(status: int, body: bytes) -> Error:
    try:
        e = json.loads(body).get("error")
        if isinstance(e, dict) and e.get("code"):
            return Error(e["code"], e.get("message", ""), e.get("cause"), e.get("status"))
    except (ValueError, AttributeError):
        pass
    if status == 404:
        return Error("not_found", body.decode(errors="replace")[:200])
    return Error("unavailable", f"core answered {status} {body.decode(errors='replace')[:200]}")


# ---- instances ----

@dataclass
class Context:
    """What an instance gets: its name, config, connections, the core."""

    service: "Service"
    name: str
    config: dict
    slots: dict
    targets: dict
    core: Core
    log: logging.Logger

    @property
    def public_url(self) -> str:
        return self.service.public_url

    @property
    def data_dir(self) -> str:
        return self.service.data_dir

    def fetch(self, url: str, timeout: int = 60) -> tuple[bytes, str]:
        """A file by URL: one of the core's (an image to post) through the
        core, any other through the egress proxy (HTTP_PROXY)."""
        pub = self.public_url.rstrip("/")
        if pub and (url.startswith(pub + "/api/core/tmp/") or url.startswith(pub + "/api/core/blob/")):
            return self.core.fetch_local(url)
        req = urllib.request.Request(url, headers={"User-Agent": self.service.user_agent})
        try:
            with urllib.request.urlopen(req, timeout=timeout) as resp:
                return resp.read(MAX_BODY + 1)[:MAX_BODY], resp.headers.get("Content-Type", "")
        except urllib.error.HTTPError as e:
            raise Error("fetch", f"{url}: {e.code}", status=e.code) from None


@dataclass
class Request:
    """A request to the service's own API (/api/…, /public/…)."""

    method: str
    path: str
    query: dict
    headers: Any
    body: bytes

    def json(self):
        return json.loads(self.body) if self.body.strip() else None


@dataclass
class Response:
    status: int = 200
    body: Any = None
    content_type: str = "application/json"


@dataclass
class _Instance:
    name: str
    raw: str
    obj: Any
    ctx: Context


@dataclass
class _Route:
    method: str
    pattern: re.Pattern
    handler: Callable
    public: bool


# Markers the decorators leave on methods.
_OP = "_companion_operation"
_JOB = "_companion_job"
_EVENT = "_companion_event"


class Service:
    """A building block as a service. Declare its instance class with
    @service.instance, its capabilities' operations, jobs and events on that
    class, its API with @service.route, then service.run()."""

    def __init__(self, id: str, name: str = "", version: str = "", port: int = 0,
                 requests: dict | None = None, config: list | None = None, ui: str | None = None,
                 ui_manifest: dict | None = None, slots: list | None = None):
        self.id = id
        self.name = name or id
        self.version = version
        self.port = port or int(os.environ.get("PORT", "8080"))
        self.requests = requests or {}
        self.config_fields = config or []
        self.ui_dir = ui
        self.ui_manifest = ui_manifest
        self.slots = slots or []
        self.cls: type | None = None
        self.ops: dict[str, dict[str, str]] = {}  # capability → operation → method name
        self.jobs: dict[str, dict] = {}
        self.subscribes: list[str] = []
        self.event_attr: str | None = None
        self.routes: list[_Route] = []
        self.instances: dict[str, _Instance] = {}
        self.version_configured = ""
        self.public_url = ""
        self.lock = threading.Lock()
        self.seen: dict[str, float] = {}
        self.data_dir = os.environ.get("COMPANION_DATA", "/data")
        self.core_url = os.environ.get("COMPANION_URL", "http://companion:3128")
        self.token_file = os.environ.get("COMPANION_TOKEN_FILE", "/run/companion/token")
        self.token_value = os.environ.get("COMPANION_TOKEN", "")
        self.user_agent = f"companion-service {id}/{version or 'dev'}"
        self.log = logging.getLogger(id)
        self.core = Core(self.core_url, self.token)

    # -- declarations --

    def instance(self, cls: type) -> type:
        self.cls = cls
        for attr, fn in inspect.getmembers(cls, inspect.isfunction):
            if hasattr(fn, _OP):
                cap, op = getattr(fn, _OP)
                self.ops.setdefault(cap, {})[op] = attr
            if hasattr(fn, _JOB):
                self.jobs[getattr(fn, _JOB)["name"]] = {**getattr(fn, _JOB), "attr": attr}
            if hasattr(fn, _EVENT):
                for t in getattr(fn, _EVENT):
                    if t not in self.subscribes:
                        self.subscribes.append(t)
                self.event_attr = attr
        return cls

    @staticmethod
    def operation(capability: str, name: str):
        def mark(fn):
            setattr(fn, _OP, (capability, name))
            return fn
        return mark

    @staticmethod
    def job(name: str, schedule: str = "", schedule_from: str = "", timeout: str = ""):
        def mark(fn):
            setattr(fn, _JOB, {"name": name, "schedule": schedule, "scheduleFrom": schedule_from, "timeout": timeout})
            return fn
        return mark

    @staticmethod
    def events(*types: str):
        def mark(fn):
            setattr(fn, _EVENT, list(types))
            return fn
        return mark

    def route(self, method: str, path: str, public: bool = False):
        """The service's API: path under /api/ (or /public/), with {name}
        parts. The handler gets (instance or None, request, **parts)."""
        regex = "^" + re.sub(r"\{(\w+)\}", r"(?P<\1>[^/]+)", path.strip("/")) + "$"

        def add(fn):
            self.routes.append(_Route(method.upper(), re.compile(regex), fn, public))
            return fn
        return add

    # -- the manifest --

    def manifest(self) -> dict:
        m: dict[str, Any] = {"id": self.id, "name": self.name, "version": self.version}
        if self.port != 8080:
            m["port"] = self.port
        m["provides"] = [{"capability": c, "operations": sorted(ops)} for c, ops in sorted(self.ops.items())]
        if self.slots:
            m["slots"] = self.slots
        jobs = []
        for j in self.jobs.values():
            jobs.append({k: v for k, v in j.items() if v and k != "attr"})
        if jobs:
            m["jobs"] = jobs
        if self.subscribes:
            m["subscribes"] = self.subscribes
        if self.requests:
            m["requests"] = self.requests
        if self.config_fields:
            m["config"] = self.config_fields
        if self.ui_manifest:
            m["ui"] = self.ui_manifest
        return m

    def token(self) -> str:
        if self.token_value:
            return self.token_value
        with open(self.token_file) as f:
            return f.read().strip()

    # -- the protocol --

    def configure(self, body: dict) -> dict:
        self.public_url = body.get("publicUrl", "")
        errors: dict[str, str] = {}
        jobs: dict[str, list] = {}
        nxt: dict[str, _Instance] = {}
        with self.lock:
            old = dict(self.instances)
        for name, ci in sorted(body.get("instances", {}).items()):
            raw = json.dumps(ci, sort_keys=True)
            have = old.get(name)
            if have and have.raw == raw:
                nxt[name] = have
                continue
            log = logging.getLogger(f"{self.id}.{name}")
            ctx = Context(self, name, ci.get("config") or {}, ci.get("slots") or {}, ci.get("targets") or {}, self.core.for_instance(name), log)
            try:
                obj = self.cls(ctx) if self.cls else None
            except Exception as e:  # the instance's config is refused
                errors[name] = str(e) or e.__class__.__name__
                continue
            nxt[name] = _Instance(name, raw, obj, ctx)
        for name, inst in old.items():
            if nxt.get(name) is not inst and hasattr(inst.obj, "stop"):
                try:
                    inst.obj.stop()
                except Exception:
                    self.log.exception("%s: stop", name)
        for name in nxt:
            jobs[name] = []
            for j in self.jobs.values():
                sched = j.get("schedule", "")
                if j.get("scheduleFrom"):
                    sched = str(nxt[name].ctx.config.get(j["scheduleFrom"], ""))
                jobs[name].append({k: v for k, v in {"name": j["name"], "schedule": sched, "timeout": j.get("timeout", "")}.items() if v})
        with self.lock:
            self.instances = nxt
            self.version_configured = body.get("version", "")
        return {"errors": errors, "jobs": jobs}

    def instance_named(self, name: str) -> _Instance:
        with self.lock:
            inst = self.instances.get(name)
        if inst is None:
            raise Error("unknown_instance", f"{self.id} has no instance {name!r}")
        return inst

    def capability(self, name: str, cap: str, op: str, args: Any, raw: bytes | None):
        inst = self.instance_named(name)
        attr = self.ops.get(cap, {}).get(op)
        if attr is None:
            raise Error("unsupported", f"{self.id} does not offer {cap} {op}", http_status=501)
        fn = getattr(inst.obj, attr)
        if raw is not None:
            return fn(raw, **(args or {}))
        sig = inspect.signature(fn)
        if len(sig.parameters) == 0:
            return fn()
        return fn(args)

    def run_job(self, name: str, job: str) -> None:
        j = self.jobs.get(job)
        if j is None:
            raise Error("not_found", f"no job {job}")
        getattr(self.instance_named(name).obj, j["attr"])()

    def event(self, name: str, ev: dict) -> None:
        ev_id = ev.get("id", "")
        if not ev_id:
            raise Error("bad_request", "an event with an id")
        with self.lock:
            if ev_id in self.seen:
                return
            self.seen[ev_id] = time.time()
            if len(self.seen) > 10000:
                cutoff = time.time() - 86400
                self.seen = {k: v for k, v in self.seen.items() if v > cutoff}
        try:
            if name:
                inst = self.instance_named(name)
                if self.event_attr:
                    getattr(inst.obj, self.event_attr)(ev)
        except Exception:
            with self.lock:
                self.seen.pop(ev_id, None)  # delivered again later
            raise

    def handle_api(self, req: Request, public: bool) -> Response:
        rel = req.path.split("/", 2)[2] if req.path.count("/") >= 2 else ""
        name = req.headers.get(INSTANCE_HEADER, "") or req.query.get("instance", [""])[0]
        inst = None
        if name:
            with self.lock:
                inst = self.instances.get(name)
        for r in self.routes:
            if r.public != public or r.method != req.method:
                continue
            m = r.pattern.match(rel.strip("/"))
            if m:
                out = r.handler(inst, req, **m.groupdict())
                return out if isinstance(out, Response) else Response(200, out)
        raise Error("not_found", f"no route {req.method} {req.path}", http_status=404)

    def ui_file(self, rel: str) -> tuple[bytes, str]:
        if not self.ui_dir:
            raise Error("not_found", "no UI", http_status=404)
        rel = posixpath.normpath("/" + rel).lstrip("/") or "index.html"
        path = os.path.join(self.ui_dir, *rel.split("/"))
        if not os.path.abspath(path).startswith(os.path.abspath(self.ui_dir)) or not os.path.isfile(path):
            raise Error("not_found", rel, http_status=404)
        with open(path, "rb") as f:
            data = f.read()
        return data, mimetypes.guess_type(path)[0] or "application/octet-stream"

    def health(self) -> dict:
        with self.lock:
            return {"status": "ok", "configVersion": self.version_configured}

    # -- running --

    def handler(self):
        service = self

        class Handler(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"
            server_version = "companion-service"

            def log_message(self, fmt, *args):  # quiet; errors are logged
                pass

            def reply(self, status: int, body: Any = None, content_type: str = "application/json"):
                if isinstance(body, (bytes, bytearray)):
                    data = bytes(body)
                elif content_type == "application/json":
                    data = b"" if body is None and status == 204 else json.dumps({} if body is None else body).encode()
                else:
                    data = str(body).encode()
                self.send_response(status)
                if status != 204:
                    self.send_header("Content-Type", content_type)
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                if self.command != "HEAD" and status != 204:
                    self.wfile.write(data)

            def handle_any(self):
                url = urllib.parse.urlsplit(self.path)
                path = url.path
                try:
                    if path == "/health":
                        return self.reply(200, service.health())
                    if path == "/manifest":
                        return self.reply(200, service.manifest())
                    n = int(self.headers.get("Content-Length") or 0)
                    if n > MAX_BODY:
                        raise Error("bad_request", "body too large", http_status=413)
                    body = self.rfile.read(n) if n else b""
                    instance = self.headers.get(INSTANCE_HEADER, "")
                    try:
                        key = service.token().encode()
                    except OSError as e:
                        raise Error("unavailable", f"no token yet: {e}", http_status=503)
                    if not verify(key, self.headers.get(SIGNATURE_HEADER, ""), self.command, self.path, instance, body):
                        raise Error("unauthorized", "missing or invalid Companion-Signature", http_status=401)
                    self.dispatch(path, url.query, instance, body)
                except Error as e:
                    self.reply(e.response_status(), e.to_json())
                except Exception as e:  # a bug in the service
                    service.log.error("%s %s: %s", self.command, path, traceback.format_exc())
                    self.reply(500, Error("internal", str(e) or e.__class__.__name__).to_json())

            def dispatch(self, path: str, query: str, instance: str, body: bytes):
                method = self.command
                if path == "/lifecycle/configure" and method == "POST":
                    return self.reply(200, service.configure(json.loads(body or b"{}")))
                if path in ("/lifecycle/pause", "/lifecycle/resume") and method == "POST":
                    return self.reply(204)
                if path.startswith("/capabilities/") and method == "POST":
                    rest = path[len("/capabilities/"):]
                    cap, _, op = rest.rpartition("/")
                    raw = None
                    args: Any = None
                    if self.headers.get("Content-Type", "").startswith("application/octet-stream"):
                        raw = body
                        a = urllib.parse.parse_qs(query).get("args", [""])[0]
                        args = json.loads(a) if a else {}
                    else:
                        args = json.loads(body) if body.strip() else {}
                    out = service.capability(instance, cap, op, args, raw)
                    if isinstance(out, (bytes, bytearray)):
                        return self.reply(200, out, "application/octet-stream")
                    return self.reply(200, out if out is not None else {})
                if path.startswith("/jobs/") and method == "POST":
                    service.run_job(instance, path[len("/jobs/"):])
                    return self.reply(204)
                if path == "/events" and method == "POST":
                    service.event(instance, json.loads(body))
                    return self.reply(204)
                if path.startswith("/api/") or path.startswith("/public/"):
                    req = Request(method, path, urllib.parse.parse_qs(query), self.headers, body)
                    out = service.handle_api(req, path.startswith("/public/"))
                    ct = out.content_type
                    return self.reply(out.status, out.body, ct)
                if path.startswith("/ui/") and method == "GET":
                    data, ct = service.ui_file(path[len("/ui/"):])
                    return self.reply(200, data, ct)
                raise Error("not_found", f"no route {method} {path}", http_status=404)

            do_GET = do_POST = do_PUT = do_DELETE = do_PATCH = do_HEAD = handle_any

        return Handler

    def serve(self, host: str = "", port: int | None = None) -> ThreadingHTTPServer:
        """Starts the HTTP server in a thread and returns it (tests)."""
        srv = ThreadingHTTPServer((host, self.port if port is None else port), self.handler())
        srv.daemon_threads = True
        threading.Thread(target=srv.serve_forever, daemon=True).start()
        return srv

    def run(self) -> None:
        """Serves until SIGTERM; with COMPANION_MANIFEST=print, prints the
        manifest and exits (hpc apply)."""
        if os.environ.get("COMPANION_MANIFEST") == "print":
            json.dump(self.manifest(), sys.stdout, indent=2)
            sys.stdout.write("\n")
            return
        setup_logging(self)
        srv = ThreadingHTTPServer(("", self.port), self.handler())
        srv.daemon_threads = True
        import signal
        signal.signal(signal.SIGTERM, lambda *_: threading.Thread(target=srv.shutdown, daemon=True).start())
        self.log.info("serving %s on :%d", self.id, self.port)
        srv.serve_forever()
        for inst in list(self.instances.values()):
            if hasattr(inst.obj, "stop"):
                inst.obj.stop()


class CoreLogHandler(logging.Handler):
    """Sends log lines to the core (POST /core/v1/logs), a batch a second,
    besides stdout, which Docker keeps."""

    def __init__(self, service: Service):
        super().__init__()
        self.service = service
        self.q: queue.Queue = queue.Queue(maxsize=1000)
        threading.Thread(target=self.forward, daemon=True).start()

    def emit(self, record: logging.LogRecord) -> None:
        if record.name.startswith("urllib"):
            return
        instance = record.name.split(".", 1)[1] if "." in record.name else ""
        level = {logging.ERROR: "error", logging.WARNING: "warn", logging.CRITICAL: "error"}.get(record.levelno, "info")
        try:
            self.q.put_nowait({"level": level, "message": self.format(record), "instance": instance})
        except queue.Full:
            pass

    def forward(self) -> None:
        while True:
            batch = [self.q.get()]
            time.sleep(1)
            while not self.q.empty() and len(batch) < 100:
                batch.append(self.q.get_nowait())
            try:
                self.service.core.json("POST", "/core/v1/logs", {"lines": batch})
            except Exception:
                pass


def setup_logging(service: Service) -> None:
    root = logging.getLogger()
    root.setLevel(logging.INFO)
    out = logging.StreamHandler(sys.stdout)
    out.setFormatter(logging.Formatter("%(asctime)s %(name)s %(message)s"))
    root.addHandler(out)
    core = CoreLogHandler(service)
    core.setFormatter(logging.Formatter("%(message)s"))
    root.addHandler(core)
