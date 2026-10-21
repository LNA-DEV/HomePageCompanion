"""Tests of the Python SDK against a fake core: signatures, configuration,
capabilities, jobs, events, the API and the UI files."""

import json
import os
import sys
import tempfile
import threading
import unittest
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from companion_service import Error, Response, Service, sign  # noqa: E402

KEY = "the-token"


class FakeCore(BaseHTTPRequestHandler):
    """The core's API for services, as far as these tests call it."""

    secrets: dict = {}
    calls: list = []

    def log_message(self, *a):
        pass

    def answer(self, status, body=None):
        data = b"" if body is None else json.dumps(body).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_any(self):
        n = int(self.headers.get("Content-Length") or 0)
        body = self.rfile.read(n) if n else b""
        FakeCore.calls.append((self.command, self.path, self.headers.get("Companion-Instance"), self.headers.get("Companion-Target"), body))
        if self.headers.get("Authorization") != "Bearer " + KEY:
            return self.answer(401, {"error": {"code": "unauthorized", "message": "no"}})
        if self.path.startswith("/core/v1/secrets/"):
            name = self.path.rsplit("/", 1)[1]
            if self.command == "PUT":
                FakeCore.secrets[name] = json.loads(body)["value"]
                return self.answer(204)
            if name not in FakeCore.secrets:
                return self.answer(404, {"error": {"code": "not_found", "message": "no secret"}})
            return self.answer(200, {"value": FakeCore.secrets[name]})
        if self.path.startswith("/core/v1/slots/source/listItems"):
            return self.answer(200, [{"id": "photo-1", "kind": "photo"}])
        if self.path.startswith("/core/v1/slots/source/getItem"):
            return self.answer(404, {"error": {"code": "not_found", "message": "no such item"}})
        return self.answer(404, {"error": {"code": "not_found", "message": self.path}})

    do_GET = do_PUT = do_POST = do_DELETE = do_any


def make_service(core_url, ui_dir=None):
    svc = Service("poster", name="Poster", version="1.2.3", requests={"egress": ["example.com"]},
                  config=[{"name": "server", "type": "string"}], ui=ui_dir,
                  slots=[{"name": "source", "needs": "feed.source/v1"}],
                  ui_manifest={"pages": [{"path": "", "title": "Poster"}]})
    svc.token_value = KEY
    svc.core = type(svc.core)(core_url, svc.token)
    state = {"runs": 0, "events": 0, "stopped": []}

    @svc.instance
    class Poster:
        def __init__(self, ctx):
            if not ctx.config.get("server"):
                raise ValueError("server is required")
            self.ctx = ctx

        def stop(self):
            state["stopped"].append(self.ctx.name)

        @svc.operation("social.publisher/v1", "describe")
        def describe(self):
            return {"platform": "poster", "maxLength": 500}

        @svc.operation("social.publisher/v1", "publish")
        def publish(self, post):
            if post.get("text") == "slow down":
                raise Error("rate_limited", "later", cause="transient")
            items = self.ctx.core.call("source", "listItems")
            return {"id": items[0]["id"], "url": self.ctx.config["server"] + "/1"}

        @svc.operation("media.image/v1", "resize")
        def resize(self, data, limits=None):
            return data.upper()

        @svc.job("sync", schedule_from="cron")
        def sync(self):
            state["runs"] += 1

        @svc.events("feed.item.discovered/v1")
        def on_event(self, ev):
            state["events"] += 1

    @svc.route("GET", "hello/{who}")
    def hello(inst, req, who):
        return {"hello": who, "instance": inst.name if inst else None}

    @svc.route("GET", "open", public=True)
    def open_route(inst, req):
        return Response(200, "open", "text/plain")

    return svc, state


class ServiceTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.core = ThreadingHTTPServer(("127.0.0.1", 0), FakeCore)
        threading.Thread(target=cls.core.serve_forever, daemon=True).start()
        cls.core_url = f"http://127.0.0.1:{cls.core.server_address[1]}"

    @classmethod
    def tearDownClass(cls):
        cls.core.shutdown()
        cls.core.server_close()

    def setUp(self):
        self.ui = tempfile.mkdtemp()
        with open(os.path.join(self.ui, "index.html"), "w") as f:
            f.write("<p>poster ui</p>")
        self.svc, self.state = make_service(self.core_url, self.ui)
        self.srv = self.svc.serve("127.0.0.1", 0)
        self.url = f"http://127.0.0.1:{self.srv.server_address[1]}"

    def tearDown(self):
        self.srv.shutdown()
        self.srv.server_close()

    def call(self, method, path, body=None, instance="", signed=True, content_type="application/json"):
        data = b"" if body is None else (body if isinstance(body, bytes) else json.dumps(body).encode())
        req = urllib.request.Request(self.url + path, data=data if method != "GET" else None, method=method)
        req.add_header("Content-Type", content_type)
        if instance:
            req.add_header("Companion-Instance", instance)
        if signed:
            req.add_header("Companion-Signature", sign(KEY.encode(), method, path, instance, data if method != "GET" else b""))
        try:
            with urllib.request.urlopen(req) as resp:
                raw = resp.read()
                return resp.status, raw
        except urllib.error.HTTPError as e:
            return e.code, e.read()

    def configure(self, instances, version="v1"):
        status, raw = self.call("POST", "/lifecycle/configure", {"version": version, "service": "poster", "publicUrl": "https://c.example", "instances": instances})
        self.assertEqual(status, 200, raw)
        return json.loads(raw)

    def test_manifest(self):
        with urllib.request.urlopen(self.url + "/manifest") as resp:
            m = json.loads(resp.read())
        self.assertEqual(m["id"], "poster")
        self.assertEqual({p["capability"]: p["operations"] for p in m["provides"]},
                         {"media.image/v1": ["resize"], "social.publisher/v1": ["describe", "publish"]})
        self.assertEqual(m["jobs"], [{"name": "sync", "scheduleFrom": "cron"}])
        self.assertEqual(m["subscribes"], ["feed.item.discovered/v1"])
        self.assertEqual(m["requests"], {"egress": ["example.com"]})
        self.assertEqual(m["slots"][0]["needs"], "feed.source/v1")

    def test_signatures(self):
        self.assertEqual(self.call("POST", "/lifecycle/configure", {}, signed=False)[0], 401)
        req = urllib.request.Request(self.url + "/lifecycle/configure", data=b"{}", method="POST")
        req.add_header("Companion-Signature", sign(b"other key", "POST", "/lifecycle/configure", "", b"{}"))
        with self.assertRaises(urllib.error.HTTPError) as e:
            urllib.request.urlopen(req)
        self.assertEqual(e.exception.code, 401)
        # Too old.
        req = urllib.request.Request(self.url + "/lifecycle/configure", data=b"{}", method="POST")
        req.add_header("Companion-Signature", sign(KEY.encode(), "POST", "/lifecycle/configure", "", b"{}", now=1))
        with self.assertRaises(urllib.error.HTTPError) as e:
            urllib.request.urlopen(req)
        self.assertEqual(e.exception.code, 401)

    def test_configure_and_capabilities(self):
        with urllib.request.urlopen(self.url + "/health") as resp:
            self.assertEqual(json.loads(resp.read())["configVersion"], "")
        res = self.configure({"main": {"config": {"server": "https://x.example", "cron": "0 0 18 * * *"}, "slots": {"source": ["rss:photos"]}},
                              "broken": {"config": {}}})
        self.assertIn("server is required", res["errors"]["broken"])
        self.assertEqual(res["jobs"]["main"], [{"name": "sync", "schedule": "0 0 18 * * *"}])
        with urllib.request.urlopen(self.url + "/health") as resp:
            self.assertEqual(json.loads(resp.read())["configVersion"], "v1")

        status, raw = self.call("POST", "/capabilities/social.publisher/v1/describe", {}, "main")
        self.assertEqual((status, json.loads(raw)["platform"]), (200, "poster"))
        # The slot call goes to the core with the instance.
        status, raw = self.call("POST", "/capabilities/social.publisher/v1/publish", {"text": "hi"}, "main")
        self.assertEqual(json.loads(raw), {"id": "photo-1", "url": "https://x.example/1"})
        method, path, instance, _, _ = [c for c in FakeCore.calls if "/slots/" in c[1]][-1]
        self.assertEqual((method, path, instance), ("POST", "/core/v1/slots/source/listItems", "main"))
        # Errors travel as the wire's.
        status, raw = self.call("POST", "/capabilities/social.publisher/v1/publish", {"text": "slow down"}, "main")
        self.assertEqual(status, 429)
        self.assertEqual(json.loads(raw)["error"], {"code": "rate_limited", "message": "later", "cause": "transient"})
        status, raw = self.call("POST", "/capabilities/social.publisher/v1/listPosts", {}, "main")
        self.assertEqual((status, json.loads(raw)["error"]["code"]), (501, "unsupported"))
        status, raw = self.call("POST", "/capabilities/social.publisher/v1/describe", {}, "nobody")
        self.assertEqual(json.loads(raw)["error"]["code"], "unknown_instance")
        # A binary body.
        status, raw = self.call("POST", "/capabilities/media.image/v1/resize?args=%7B%7D", b"jpeg", "main", content_type="application/octet-stream")
        self.assertEqual((status, raw), (200, b"JPEG"))

    def test_jobs_events_reconfigure(self):
        self.configure({"main": {"config": {"server": "https://x.example"}}})
        self.assertEqual(self.call("POST", "/jobs/sync", {}, "main")[0], 204)
        self.assertEqual(self.state["runs"], 1)
        ev = {"id": "e1", "type": "feed.item.discovered/v1", "data": {}}
        self.call("POST", "/events", ev, "main")
        self.call("POST", "/events", ev, "main")
        self.assertEqual(self.state["events"], 1)
        # The same configuration keeps the instance, another replaces it.
        self.configure({"main": {"config": {"server": "https://x.example"}}}, "v2")
        self.assertEqual(self.state["stopped"], [])
        self.configure({"main": {"config": {"server": "https://y.example"}}}, "v3")
        self.assertEqual(self.state["stopped"], ["main"])

    def test_api_and_ui(self):
        self.configure({"main": {"config": {"server": "https://x.example"}}})
        status, raw = self.call("GET", "/api/hello/jane", None, "main")
        self.assertEqual(json.loads(raw), {"hello": "jane", "instance": "main"})
        status, raw = self.call("GET", "/public/open")
        self.assertEqual((status, raw), (200, b"open"))
        self.assertEqual(self.call("GET", "/api/open")[0], 404)
        status, raw = self.call("GET", "/ui/index.html")
        self.assertEqual((status, raw), (200, b"<p>poster ui</p>"))
        self.assertEqual(self.call("GET", "/ui/../../etc/passwd")[0], 404)

    def test_core_client(self):
        self.svc.core.put_secret("jwt:main", "abc")
        self.assertEqual(self.svc.core.get_secret("jwt:main"), "abc")
        self.assertIsNone(self.svc.core.get_secret("missing"))
        with self.assertRaises(Error) as e:
            self.svc.core.for_instance("main").call("source", "getItem", {"id": "x"})
        self.assertEqual(e.exception.code, "not_found")


if __name__ == "__main__":
    unittest.main()
