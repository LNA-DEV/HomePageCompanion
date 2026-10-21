"""Lemmy as a service of the companion (docs/concepts/modular-architecture.md
§7, Stage 3): social.publisher/v1 and social.interactions/v1 for one account
posting to one community, against Lemmy 0.19's /api/v3.

One instance per account and community:

    services:
      lemmy:
        from: oci://ghcr.io/lna-dev/companion-lemmy:latest
        grants: { egress: [lemmy.world] }
        instances:
          main: { server: https://lemmy.world, username: lna, password: "${LEMMY_PASSWORD}", community: photography }
"""

from __future__ import annotations

import json
import logging
import os
import re
import urllib.error
import urllib.parse
import urllib.request
import uuid

from companion_service import Error, Service

TITLE_MAX = 200    # Lemmy: 3 to 200 characters, one line
BODY_MAX = 10000   # Lemmy allows 50,000; captions are far shorter
ALT_MAX = 1500

PUBLISHER = "social.publisher/v1"
INTERACTIONS = "social.interactions/v1"

HERE = os.path.dirname(os.path.abspath(__file__))

service = Service(
    "lemmy",
    name="Lemmy",
    version="1.0.0",
    # The server is per instance: grant its host as egress in companion.yaml.
    requests={"volume": False},
    config=[
        {"name": "server", "type": "string", "help": "the Lemmy instance, https://lemmy.world"},
        {"name": "username", "type": "string", "help": "the posting account"},
        {"name": "password", "type": "string", "secret": True},
        {"name": "community", "type": "string", "help": "where to post: photography, or photography@lemmy.world for a remote one"},
        {"name": "nsfw", "type": "boolean"},
    ],
    ui=os.path.join(HERE, "ui"),
    ui_manifest={
        "nav": [{"label": "Lemmy", "icon": "message-square", "page": "", "group": "Services"}],
        "pages": [{"path": "", "title": "Lemmy"}],
    },
)

log = logging.getLogger("lemmy")


class LemmyAPI:
    """Lemmy's HTTP API. Requests go through the egress proxy (HTTP_PROXY);
    Lemmy's errors become the capability's: a refused login is the
    account's problem, a rate limit or an outage a transient one, a refused
    post its content's."""

    def __init__(self, server: str, user_agent: str):
        self.server = server.rstrip("/")
        self.user_agent = user_agent

    def call(self, method: str, path: str, body=None, jwt: str | None = None, query: dict | None = None, data: bytes | None = None, content_type: str | None = None):
        url = self.server + path
        if query:
            url += "?" + urllib.parse.urlencode(query)
        payload = data
        if body is not None:
            payload, content_type = json.dumps(body).encode(), "application/json"
        req = urllib.request.Request(url, data=payload, method=method, headers={"User-Agent": self.user_agent, "Accept": "application/json"})
        if content_type:
            req.add_header("Content-Type", content_type)
        if jwt:
            req.add_header("Authorization", "Bearer " + jwt)
        try:
            with urllib.request.urlopen(req, timeout=60) as resp:
                raw = resp.read()
                return json.loads(raw) if raw.strip() else {}
        except urllib.error.HTTPError as e:
            raise lemmy_error(e.code, e.read()) from None
        except urllib.error.URLError as e:
            raise Error("network", f"lemmy {self.server}: {e.reason}", cause="transient") from None


AUTH_ERRORS = {"incorrect_login", "not_logged_in", "password_incorrect", "email_not_verified", "registration_application_pending", "site_ban", "banned_from_community"}
CONTENT_ERRORS = {"invalid_post_title", "invalid_body_field", "alt_text_length_overflow", "invalid_url", "image_upload_disabled", "too_large", "no_file_uploaded"}


def lemmy_error(status: int, raw: bytes) -> Error:
    code = ""
    try:
        code = json.loads(raw).get("error", "")
    except (ValueError, AttributeError):
        pass
    msg = f"lemmy: {code or raw.decode(errors='replace')[:200]} (status: {status})"
    if code == "rate_limit_error" or status == 429:
        return Error("rate_limited", msg, cause="transient", status=429)
    if code in AUTH_ERRORS or status in (401, 403):
        return Error("auth", msg, cause="account", status=401 if status < 500 else status)
    if code in CONTENT_ERRORS or status == 413:
        return Error("content", msg, cause="content", status=status)
    if status >= 500:
        return Error("server", msg, cause="transient", status=status)
    return Error(code or "client", msg, status=status)


PROFILE = re.compile(r"^https?://([^/]+)/u/([A-Za-z0-9_]+)/?$")


@service.instance
class Lemmy:
    def __init__(self, ctx):
        self.ctx = ctx
        c = ctx.config
        self.server = str(c.get("server", "")).rstrip("/")
        if not re.match(r"^https?://[^/]+$", self.server):
            raise ValueError("server is the Lemmy instance's address, https://lemmy.world")
        self.username = str(c.get("username", ""))
        self.password = str(c.get("password", ""))
        self.community = str(c.get("community", ""))
        if not self.username or not self.password:
            raise ValueError("username and password are required")
        if not self.community:
            raise ValueError("community is required")
        self.nsfw = bool(c.get("nsfw", False))
        self.host = urllib.parse.urlsplit(self.server).netloc
        self.api = LemmyAPI(self.server, ctx.service.user_agent)
        self._jwt: str | None = None
        self._community_id: int | None = None

    # -- the account --

    def jwt(self, fresh: bool = False) -> str:
        """Lemmy's token, kept in the core's secret store across restarts:
        logins are rate limited."""
        name = "jwt:" + self.ctx.name
        if not fresh:
            if self._jwt:
                return self._jwt
            self._jwt = self.ctx.core.get_secret(name)
            if self._jwt:
                return self._jwt
        res = self.api.call("POST", "/api/v3/user/login", {"username_or_email": self.username, "password": self.password})
        tok = res.get("jwt")
        if not tok:
            raise Error("auth", "lemmy: login answered no token (2FA or an unverified email?)", cause="account", status=401)
        self._jwt = tok
        self.ctx.core.put_secret(name, tok)
        return tok

    def authed(self, method: str, path: str, **kw):
        """A call with the token; a token Lemmy no longer takes is renewed
        once."""
        try:
            return self.api.call(method, path, jwt=self.jwt(), **kw)
        except Error as e:
            if e.code != "auth":
                raise
            return self.api.call(method, path, jwt=self.jwt(fresh=True), **kw)

    def community_id(self) -> int:
        if self._community_id is None:
            try:
                res = self.authed("GET", "/api/v3/community", query={"name": self.community})
            except Error as e:
                if e.status == 404 or "couldnt_find" in e.message:
                    raise Error("community_unknown", f"lemmy: no community {self.community} on {self.host}", cause="account", status=404) from None
                raise
            self._community_id = int(res["community_view"]["community"]["id"])
        return self._community_id

    # -- social.publisher/v1 --

    @service.operation(PUBLISHER, "describe")
    def describe(self):
        return {"platform": "lemmy", "account": f"{self.username}@{self.host}", "maxLength": BODY_MAX,
                "maxImages": 1, "linkCard": "manual", "mentionProfiles": ["lemmy"], "maxMentions": 10}

    def compose(self, post: dict) -> dict:
        """The Lemmy post a companion post becomes: the item's title (or the
        link card's, or the text's first line) as the title, the text as
        the body."""
        text = (post.get("text") or "").strip()
        if post.get("contentWarning"):
            text = f"CW: {post['contentWarning']}\n\n{text}"
        link = post.get("link") or {}
        lines = text.split("\n", 1)
        given = (post.get("title") or link.get("title") or "").strip()
        title = given or lines[0].strip()
        body = text if given else (lines[1].strip() if len(lines) > 1 else "")
        title = re.sub(r"\s+", " ", title)
        if len(title) > TITLE_MAX:
            cut = title[:TITLE_MAX - 1].rsplit(" ", 1)[0]
            body = (title + "\n\n" + body).strip()
            title = cut + "…"
        if len(title) < 3:
            title = (title + " " + (post.get("kind") or "post")).strip() if title else "Post"
        warnings = []
        if len(body) > BODY_MAX:
            body = body[:BODY_MAX - 1] + "…"
            warnings.append(f"the text is longer than {BODY_MAX} characters and was cut")
        return {"name": title, "body": body, "link": link.get("url"), "warnings": warnings}

    @service.operation(PUBLISHER, "preview")
    def preview(self, post):
        c = self.compose(post)
        out = {"text": c["name"] + ("\n\n" + c["body"] if c["body"] else ""), "warnings": c["warnings"]}
        if post.get("images"):
            out["images"] = post["images"][:1]
            if len(post["images"]) > 1:
                out["warnings"].append("Lemmy takes one image per post; the others are left out")
        if post.get("link"):
            out["link"] = post["link"]
        return out

    @service.operation(PUBLISHER, "publish")
    def publish(self, post):
        key = post.get("idempotencyKey") or ""
        doc = f"{self.ctx.name}:{key}" if key else ""
        if doc:
            # A try whose answer got lost: the post exists already.
            done = self.ctx.core.get_document("published", doc)
            if done:
                return done
        c = self.compose(post)
        body = {"name": c["name"], "community_id": self.community_id(), "nsfw": self.nsfw}
        if c["body"]:
            body["body"] = c["body"]
        images = post.get("images") or []
        if images:
            img = images[0]
            body["url"] = self.upload(img["url"])
            if img.get("alt"):
                body["alt_text"] = img["alt"][:ALT_MAX]
        elif c["link"]:
            body["url"] = c["link"]
        res = self.authed("POST", "/api/v3/post", body=body)
        p = res["post_view"]["post"]
        result = {"id": str(p["id"]), "url": f"{self.server}/post/{p['id']}", "version": p.get("ap_id", "")}
        if doc:
            self.ctx.core.put_document("published", doc, result)
        self.ctx.log.info("posted %s to %s", result["url"], self.community)
        return result

    def upload(self, url: str) -> str:
        """The image into Lemmy's pict-rs; its URL is the post's link."""
        data, ctype = self.ctx.fetch(url)
        if not data:
            raise Error("content", f"no image at {url}", cause="content")
        ctype = (ctype or "image/jpeg").split(";")[0]
        boundary = uuid.uuid4().hex
        ext = {"image/png": "png", "image/webp": "webp", "image/gif": "gif"}.get(ctype, "jpg")
        payload = (f"--{boundary}\r\nContent-Disposition: form-data; name=\"images[]\"; filename=\"photo.{ext}\"\r\n"
                   f"Content-Type: {ctype}\r\n\r\n").encode() + data + f"\r\n--{boundary}--\r\n".encode()
        res = self.authed("POST", "/pictrs/image", data=payload, content_type=f"multipart/form-data; boundary={boundary}")
        files = res.get("files") or []
        if res.get("msg") != "ok" or not files:
            raise Error("content", f"lemmy: image upload refused ({res.get('msg')})", cause="content")
        return f"{self.server}/pictrs/image/{files[0]['file']}"

    @service.operation(PUBLISHER, "resolveMention")
    def resolve_mention(self, args):
        m = PROFILE.match(args.get("profileUrl", ""))
        if not m:
            raise Error("not_a_profile", f"{args.get('profileUrl')} is not a Lemmy profile")
        host, name = m.group(1), m.group(2)
        if name == self.username and host == self.host:
            raise Error("self", "that is the posting account")
        acct = f"{name}@{host}"
        return {"text": "@" + acct, "account": acct}

    @service.operation(PUBLISHER, "listPosts")
    def list_posts(self):
        res = self.authed("GET", "/api/v3/user", query={"username": self.username, "sort": "New", "limit": 50})
        out = []
        for v in res.get("posts") or []:
            p = v["post"]
            url = p.get("url") or ""
            out.append({"ref": {"id": str(p["id"]), "url": f"{self.server}/post/{p['id']}"},
                        "text": (p.get("name", "") + "\n\n" + (p.get("body") or "")).strip(),
                        "imageUrls": [url] if "/pictrs/image/" in url else [],
                        "createdAt": normalize_time(p.get("published", ""))})
        return out

    @service.operation(PUBLISHER, "delete")
    def delete(self, ref):
        try:
            self.authed("POST", "/api/v3/post/delete", body={"post_id": int(ref["id"]), "deleted": True})
        except Error as e:
            if e.status != 404:
                raise
        return None

    # -- social.interactions/v1 --

    @service.operation(INTERACTIONS, "getStats")
    def get_stats(self, ref):
        res = self.authed("GET", "/api/v3/post", query={"id": int(ref["id"])})
        v = res["post_view"]
        counts = v.get("counts") or v.get("post") or {}  # 0.19, then 1.0
        return {"likes": int(counts.get("upvotes", counts.get("score", 0))), "replies": int(counts.get("comments", 0))}

    @service.operation(INTERACTIONS, "listReplies")
    def list_replies(self, ref):
        res = self.authed("GET", "/api/v3/comment/list", query={"post_id": int(ref["id"]), "sort": "New", "limit": 50})
        out = []
        for v in res.get("comments") or []:
            c, who = v["comment"], v.get("creator") or {}
            actor = who.get("actor_id", "")
            host = urllib.parse.urlsplit(actor).netloc or self.host
            out.append({"id": str(c["id"]), "author": f"{who.get('name', '?')}@{host}", "authorUrl": actor,
                        "avatarUrl": who.get("avatar") or "", "body": c.get("content", ""),
                        "url": c.get("ap_id", ""), "postedAt": normalize_time(c.get("published", ""))})
        return out


def normalize_time(t: str) -> str:
    """Lemmy 0.19 leaves the zone off some times; they are UTC."""
    if t and not re.search(r"(Z|[+-]\d\d:\d\d)$", t):
        return t + "Z"
    return t


@service.route("GET", "status")
def status(_inst, _req):
    """For the UI: every instance's account and its latest posts."""
    out = []
    for name, inst in sorted(service.instances.items()):
        lemmy = inst.obj
        entry = {"instance": name, "account": f"{lemmy.username}@{lemmy.host}", "community": lemmy.community, "server": lemmy.server}
        try:
            entry["posts"] = lemmy.list_posts()[:10]
        except Error as e:
            entry["error"] = e.message
        out.append(entry)
    return out


if __name__ == "__main__":
    service.run()
