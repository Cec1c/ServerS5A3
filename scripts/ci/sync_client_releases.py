"""Synchronize client EXE Releases between GitGud and GitHub by content identity.

Only Release assets and their tags are synchronized; source branches are never
written and no server binary is built. Conflicts stop before writes.
"""
import argparse
import base64
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
import zipfile

MAX_ASSET = 512 * 1024 * 1024
MAX_ARCHIVE_MEMBERS = 32
MARKER = "<!-- client-release-sync:v1 "
TAG = re.compile(r"client-[A-Za-z0-9][A-Za-z0-9._-]{0,120}\Z")
NAME = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]{0,150}\.zip\Z", re.I)
SPLIT_NAME = re.compile(r"([A-Za-z0-9][A-Za-z0-9._-]{0,150}\.zip)\.([0-9]{3})\Z", re.I)


class SyncError(RuntimeError):
    pass


def require_no_tag_workflow(document):
    """Conservatively audit historical YAML before a deploy-key tag push."""
    try:
        import yaml
    except ImportError:
        raise SyncError("Install scripts/ci/requirements-release-sync.txt for SSH tag audit") from None

    class UniqueLoader(yaml.BaseLoader):
        def construct_mapping(self, node, deep=False):
            result = {}
            for key_node, value_node in node.value:
                key = self.construct_object(key_node, deep=deep)
                if not isinstance(key, str) or key == "<<" or key in result:
                    raise SyncError("Ambiguous historical workflow mapping")
                result[key] = self.construct_object(value_node, deep=deep)
            return result

    try:
        data = yaml.load(document, Loader=UniqueLoader)
    except (yaml.YAMLError, RecursionError):
        raise SyncError("Invalid historical workflow YAML") from None
    events = data.get("on") if isinstance(data, dict) else None
    if isinstance(events, str):
        events = {events: None}
    elif isinstance(events, list) and all(isinstance(x, str) for x in events):
        events = dict.fromkeys(events)
    if not isinstance(events, dict) or not events:
        raise SyncError("Historical workflow trigger cannot be verified")
    for event, config in events.items():
        if event in ("workflow_dispatch", "workflow_call", "schedule", "release"):
            continue
        # Explicit branch-only push filters exclude tag pushes in Actions.
        if (event == "push" and isinstance(config, dict) and
                set(config) <= {"branches", "paths", "paths-ignore"} and
                isinstance(config.get("branches"), list) and config["branches"] and
                all(isinstance(x, str) and x for x in config["branches"])):
            continue
        raise SyncError("Historical workflow may run on tag creation; SSH tag transfer refused")


def audit_tag_workflows(git):
    listing = git("workflow-list", "ls-tree", "-r", "-z", "--long", "refs/sync/source", "--", ".github/workflows")
    entries = [row for row in listing.split("\0") if row]
    if len(entries) > 64:
        raise SyncError("Historical workflow tree exceeds audit limit")
    for row in entries:
        metadata, path = row.split("\t", 1)
        if not path.lower().endswith((".yml", ".yaml")):
            continue
        mode, kind, oid, size = metadata.split()
        if mode not in ("100644", "100755") or kind != "blob" or not size.isdigit() or int(size) > 256 * 1024:
            raise SyncError("Historical workflow is not a bounded regular file")
        require_no_tag_workflow(git("workflow-read", "cat-file", "blob", oid))


def digest(path):
    h = hashlib.sha256()
    with Path(path).open("rb") as f:
        for block in iter(lambda: f.read(1024 * 1024), b""):
            h.update(block)
    return h.hexdigest()


def safe_url(url):
    p = urllib.parse.urlsplit(url)
    if p.scheme != "https" or p.username or p.password or p.fragment:
        raise SyncError("Invalid HTTPS asset URL")
    return p


class HTTPSRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        safe_url(newurl)
        if req.has_header("Authorization") or req.has_header("Private-token"):
            raise SyncError("Authenticated API redirect refused")
        return super().redirect_request(req, fp, code, msg, headers, newurl)


OPENER = urllib.request.build_opener(HTTPSRedirect())


def request(url, method="GET", token="", provider="", body=None, raw=False, missing=False):
    safe_url(url)
    headers = {"User-Agent": "ServerS5A3-client-release-sync/1", "Accept": "application/json"}
    if token:
        headers["Authorization" if provider == "github" else "PRIVATE-TOKEN"] = ("Bearer " if provider == "github" else "") + token
    if body is not None:
        if not raw:
            body = json.dumps(body).encode()
        headers["Content-Type"] = "application/octet-stream" if raw else "application/json"
    for attempt in range(3):
        try:
            with OPENER.open(urllib.request.Request(url, data=body, headers=headers, method=method), timeout=60) as response:
                data = response.read(16 * 1024 * 1024 + 1)
                if len(data) > 16 * 1024 * 1024:
                    raise SyncError("API response exceeds limit")
                return json.loads(data) if data else None
        except urllib.error.HTTPError as error:
            if missing and error.code == 404:
                return None
            if method != "GET" or error.code not in (429, 500, 502, 503, 504) or attempt == 2:
                raise SyncError(f"API {method} failed with HTTP {error.code}") from None
        except (TimeoutError, urllib.error.URLError):
            if method != "GET" or attempt == 2:
                raise SyncError(f"API {method} network failure") from None
        time.sleep(2 * (attempt + 1))


def download(url, cache, expected=None, fresh=False, missing=False):
    safe_url(url)
    cache = Path(cache)
    cache.mkdir(parents=True, exist_ok=True)
    target = cache / (hashlib.sha256(url.encode()).hexdigest() + ".zip")
    if not fresh and target.exists() and (expected is None or digest(target) == expected):
        return target
    partial = target.with_suffix(".part")
    for attempt in range(3):
        try:
            # Public assets only: never forward an API credential to a CDN.
            with OPENER.open(urllib.request.Request(url, headers={"User-Agent": "ServerS5A3-client-release-sync/1"}), timeout=60) as response, partial.open("wb") as out:
                size = 0
                while block := response.read(1024 * 1024):
                    size += len(block)
                    if size > MAX_ASSET:
                        raise SyncError("Asset exceeds size limit")
                    out.write(block)
            if expected and digest(partial) != expected:
                raise SyncError("Asset digest conflicts with recorded identity")
            partial.replace(target)
            return target
        except urllib.error.HTTPError as error:
            if missing and error.code == 404:
                return None
            raise SyncError(f"Asset download failed with HTTP {error.code}") from None
        except (TimeoutError, urllib.error.URLError):
            if attempt == 2:
                raise SyncError("Asset download failed") from None
            time.sleep(2 * (attempt + 1))


def inspect_zip(path, *, with_pvf=False):
    try:
        with zipfile.ZipFile(path) as z:
            names = set()
            exe = None
            pvf = None
            total = 0
            for entry in z.infolist():
                raw_name = entry.orig_filename
                parts = raw_name.split("/")
                if any(x in ("", ".", "..") for x in parts) or "\\" in raw_name or ":" in raw_name or '\0' in raw_name:
                    raise SyncError("Unsafe ZIP member")
                if entry.filename.casefold() in names or entry.is_dir() or ((entry.external_attr >> 16) & 0o170000) == 0o120000:
                    raise SyncError("Duplicate or non-file ZIP member")
                names.add(entry.filename.casefold())
                total += entry.file_size
                if total > MAX_ASSET:
                    raise SyncError(f"Client ZIP exceeds extraction limit: uncompressed bytes {total} > {MAX_ASSET}")
                if len(names) > MAX_ARCHIVE_MEMBERS:
                    raise SyncError(f"Client ZIP exceeds extraction limit: member count {len(names)} > {MAX_ARCHIVE_MEMBERS}")
                leaf = parts[-1].lower()
                if leaf == "dnf.exe":
                    if exe is not None:
                        raise SyncError("Multiple DNF executables")
                    h = hashlib.sha256()
                    with z.open(entry) as stream:
                        if stream.read(2) != b"MZ":
                            raise SyncError("Invalid DNF executable")
                        h.update(b"MZ")
                        for block in iter(lambda: stream.read(1024 * 1024), b""):
                            h.update(block)
                    exe = {"sha256": h.hexdigest(), "length": entry.file_size}
                elif with_pvf and raw_name == "client/Script.pvf":
                    if entry.file_size == 0:
                        raise SyncError("Empty client PVF")
                    h = hashlib.sha256()
                    with z.open(entry) as stream:
                        for block in iter(lambda: stream.read(1024 * 1024), b""):
                            h.update(block)
                    pvf = {"sha256": h.hexdigest(), "length": entry.file_size}
                elif not (leaf.endswith((".txt", ".md", ".json")) or
                          (len(parts) == 2 and parts[0] == "evidence" and leaf.endswith(".log")) or
                          (len(parts) > 1 and parts[0] == "scripts" and leaf.endswith(".py"))):
                    raise SyncError("Unexpected client ZIP member")
                else:
                    # Static evidence is read for ZIP integrity only, never extracted or executed.
                    with z.open(entry) as stream:
                        while stream.read(1024 * 1024):
                            pass
            if exe is None:
                raise SyncError("Client ZIP has no DNF.exe")
            if with_pvf:
                if pvf is None:
                    raise SyncError("Split client ZIP has no PVF")
                return {"exe": exe, "pvf": pvf}
            return exe
    except (zipfile.BadZipFile, EOFError):
        raise SyncError("Invalid client ZIP") from None


def split_metadata(name, recorded):
    match = SPLIT_NAME.fullmatch(name)
    meta = recorded.get("split_archive") if isinstance(recorded, dict) else None
    if not match or not isinstance(meta, dict) or set(meta) != {"count", "index", "length", "sha256"}:
        raise SyncError("Invalid split archive provenance")
    if any(type(meta[k]) is not int for k in ("count", "index", "length")):
        raise SyncError("Invalid split archive dimensions")
    if not (2 <= meta["count"] <= 16 and 1 <= meta["index"] <= meta["count"] and
            int(match[2]) == meta["index"] and 0 < meta["length"] <= MAX_ASSET):
        raise SyncError("Invalid split archive dimensions")
    if (not isinstance(meta["sha256"], str) or not re.fullmatch(r"[a-f0-9]{64}", meta["sha256"]) or
            type(recorded.get("length")) is not int or not 0 < recorded["length"] <= meta["length"] or
            not isinstance(recorded.get("sha256"), str) or not re.fullmatch(r"[a-f0-9]{64}", recorded["sha256"])):
        raise SyncError("Invalid split archive digest or size")
    return match[1], meta


def inspect_split_archives(assets, cache):
    groups = {}
    executables = [a["exe"] for a in assets.values() if "exe" in a]
    for name, asset in assets.items():
        if "split_archive" in asset:
            base, meta = split_metadata(name, asset)
            groups.setdefault(base, []).append((meta["index"], asset))
    for parts in groups.values():
        parts.sort(key=lambda part: part[0])
        expected = parts[0][1]["split_archive"]
        if [i for i, _ in parts] != list(range(1, expected["count"] + 1)):
            raise SyncError("Incomplete split archive")
        for _, asset in parts:
            if any(asset["split_archive"][k] != expected[k] for k in ("count", "length", "sha256")):
                raise SyncError("Conflicting split archive provenance")
        if sum(a["length"] for _, a in parts) != expected["length"]:
            raise SyncError("Split archive length mismatch")
        Path(cache).mkdir(parents=True, exist_ok=True)
        with tempfile.TemporaryDirectory(prefix="split-", dir=cache) as directory:
            combined = Path(directory) / "client.zip"
            with combined.open("wb") as out:
                for _, asset in parts:
                    count, h = 0, hashlib.sha256()
                    with Path(asset["path"]).open("rb") as stream:
                        for block in iter(lambda: stream.read(1024 * 1024), b""):
                            count += len(block)
                            if count > asset["length"]:
                                raise SyncError("Split part changed during verification")
                            h.update(block)
                            out.write(block)
                    if count != asset["length"] or h.hexdigest() != asset["sha256"]:
                        raise SyncError("Split part changed during verification")
            if digest(combined) != expected["sha256"]:
                raise SyncError("Combined split archive digest mismatch")
            executables.append(inspect_zip(combined, with_pvf=True)["exe"])
    if groups and any(exe != executables[0] for exe in executables):
        raise SyncError("Split and standalone executable identities differ")


def provenance(body):
    if MARKER not in body:
        return None
    try:
        raw = body.split(MARKER, 1)[1].split(" -->", 1)[0]
        value = json.loads(raw)
        if value["format"] != 1 or not re.fullmatch(r"[a-f0-9]{40}", value["commit"]):
            raise ValueError()
        return value
    except (ValueError, KeyError, TypeError):
        raise SyncError("Malformed release provenance") from None


def _git_failure_classification(stderr):
    text = stderr.lower()
    if b"workflow" in text and (b"refusing to allow" in text or b"workflows permission" in text):
        return "github-workflow-write-permission"
    permission = (
        b"permission", b"authentication failed", b"not authorized",
        b"access denied", b"forbidden", b"returned error: 403",
        b"resource not accessible by integration", b"protected tag",
        b"branch protection",
    )
    non_fast_forward = (
        b"non-fast-forward", b"fetch first", b"cannot lock ref",
        b"stale info", b"tag already exists",
    )
    network = (
        b"could not resolve host", b"temporary failure in name resolution",
        b"failed to connect", b"connection timed out", b"connection reset",
        b"network is unreachable", b"operation timed out",
    )
    if any(marker in text for marker in permission):
        return "workflow-permission-or-auth"
    if any(marker in text for marker in non_fast_forward):
        return "non-fast-forward"
    if any(marker in text for marker in network):
        return "network"
    return "unknown"


class Repository:
    def __init__(self, key, provider, repository, token="", writable=False, host="gitgud.io"):
        self.key, self.provider, self.repository = key, provider, repository
        self.token, self.writable, self.host = token, writable, host
        self.github_tag_ssh = False
        if provider == "github":
            self.base = f"https://api.github.com/repos/{repository}"
            self.git = f"https://github.com/{repository}.git"
        else:
            self.base = f"https://{host}/api/v4/projects/{repository}"
            self.git = None

    def api(self, path, **kwargs):
        return request(self.base + path, token=self.token, provider=self.provider, **kwargs)

    def list(self, include_drafts=False):
        result = []
        for page in range(1, 21):
            rows = self.api(f"/releases?per_page=100&page={page}")
            result.extend(x for x in rows if TAG.fullmatch(x["tag_name"]) and (include_drafts or not x.get("draft")))
            if len(rows) < 100:
                return result
        raise SyncError("Release pagination limit exceeded")

    def get(self, tag):
        prefix = "/releases/tags/" if self.provider == "github" else "/releases/"
        return self.api(prefix + urllib.parse.quote(tag, safe=""), missing=True)

    def tag_commit(self, tag):
        if self.provider != "github":
            ref = self.api("/repository/tags/" + tag, missing=True)
            return ref["commit"]["id"] if ref else None
        ref = self.api("/git/ref/tags/" + tag, missing=True)
        if ref is None:
            return None
        obj = ref["object"]
        for _ in range(5):
            if obj["type"] == "commit":
                return obj["sha"]
            if obj["type"] != "tag":
                break
            obj = self.api("/git/tags/" + obj["sha"])["object"]
        raise SyncError("Unsupported or recursive tag target")

    def require_tag_policy(self, existing):
        if self.provider == "github" and existing is None:
            if os.environ.get("GITHUB_ACTIONS") != "true" or not self.token or self.token != os.environ.get("GITHUB_TOKEN"):
                raise SyncError("Create new GitHub tags through trusted CI using GITHUB_TOKEN to avoid historical build workflows")
            if self.github_tag_ssh and (os.environ.get("GITHUB_REPOSITORY") != self.repository or not os.environ.get("GIT_SSH_COMMAND")):
                raise SyncError("SSH tag transfer requires this CI repository and its dedicated mirror identity")

    def prepare(self, item, cache=None, publishing=False):
        commit = self.tag_commit(item["tag"])
        if commit is not None and commit != item["commit"]:
            raise SyncError("Destination tag differs; never replace it")
        if publishing:
            self.require_tag_policy(commit)
        if self.provider != "github" and cache is not None:
            for name,asset in item["assets"].items():
                url = self.base + self.package_path(item,name,asset)
                download(url,cache,asset["sha256"],fresh=True,missing=True)
        if self.provider == "github":
            drafts = [x for x in self.list(include_drafts=True) if x.get("draft") and x["tag_name"] == item["tag"]]
            if len(drafts) > 1:
                raise SyncError("Multiple destination drafts")
            if drafts:
                draft = drafts[0]
                if provenance(draft.get("body") or "") != release_record(item):
                    raise SyncError("Existing draft has a different release identity")
                if draft.get("prerelease") is not item.get("preview", False):
                    raise SyncError("Existing draft preview state differs from release identity")
                names = set()
                for asset in draft["assets"]:
                    name = asset["name"]
                    expected = item["assets"].get(name)
                    if name in names or expected is None or asset["size"] != expected["length"] or asset.get("digest") != "sha256:" + expected["sha256"]:
                        raise SyncError("Existing draft asset cannot be verified")
                    names.add(name)
                return draft
        return None

    @staticmethod
    def package_path(item,name,asset):
        return f"/packages/generic/dnf-client/{item['tag']}-{asset['sha256'][:16]}/{name}"

    def identity(self, row, cache, fresh=False):
        tag = row["tag_name"]
        body = row.get("body") or row.get("description") or ""
        record = provenance(body)
        if self.provider == "github":
            ref = self.api("/git/ref/tags/" + urllib.parse.quote(tag, safe=""))
            obj = ref["object"]
            for _ in range(5):
                if obj["type"] == "commit":
                    break
                if obj["type"] != "tag":
                    raise SyncError("Unsupported tag target")
                obj = self.api("/git/tags/" + obj["sha"])["object"]
            if obj["type"] != "commit":
                raise SyncError("Tag recursion limit")
            commit = obj["sha"]
            links = [{"url": a["browser_download_url"], "name": a["name"]} for a in row["assets"]]
            public = row["html_url"]
        else:
            commit = row["commit"]["id"]
            links = row["assets"]["links"]
            public = row["_links"]["self"]
        if record and (record["tag"] != tag or record["commit"] != commit):
            raise SyncError("Release provenance/tag mismatch")
        assets = {}
        for a in links:
            url = a["url"]
            p = safe_url(url)
            name = urllib.parse.unquote(p.path.rsplit("/", 1)[-1])
            is_split = SPLIT_NAME.fullmatch(name) is not None
            if not NAME.fullmatch(name) and not is_split:
                raise SyncError("Unexpected client asset name")
            if self.provider == "github":
                allowed = p.netloc == "github.com" and p.path.startswith(f"/{self.repository}/releases/download/{tag}/")
            else:
                allowed = p.netloc == self.host and p.path.startswith(f"/api/v4/projects/{self.repository}/packages/generic/")
            if not allowed or p.query:
                raise SyncError("Asset is outside the source repository package scope")
            if name in assets:
                raise SyncError("Duplicate asset identity")
            expected = record["assets"].get(name, {}).get("sha256") if record else None
            if record and expected is None:
                raise SyncError("Asset missing from release provenance")
            metadata = split_metadata(name, record["assets"].get(name) if record else None)[1] if is_split else None
            path = download(url, cache, expected, fresh)
            assets[name] = {"sha256": digest(path), "length": path.stat().st_size, "path": str(path), "url": url}
            if is_split:
                assets[name]["split_archive"] = metadata
            else:
                assets[name]["exe"] = inspect_zip(path)
            if record:
                observed = {k:v for k,v in assets[name].items() if k in ("sha256", "length", "exe", "split_archive")}
                if observed != record["assets"][name]:
                    raise SyncError("Release asset differs from provenance")
        if record and set(assets) != set(record["assets"]):
            raise SyncError("Incomplete release asset set")
        if not assets:
            raise SyncError("Client release has no assets")
        inspect_split_archives(assets, cache)
        preview = (row.get("prerelease", False) or "preview" in body.lower() or "预览" in body) if record is None else record.get("preview")
        if not isinstance(preview, bool):
            raise SyncError("Invalid preview provenance")
        if record and self.provider == "github" and row["prerelease"] != preview:
            raise SyncError("Preview state differs from recorded identity")
        return {"tag":tag, "commit":commit, "name":row.get("name") or tag, "body":body.split(MARKER)[0].rstrip(), "source":record["source"] if record else public, "published":row.get("published_at") or row.get("released_at"), "preview":preview, "assets":assets, "repo":self}

    def ensure_tag(self, item, directory):
        tag, commit = item["tag"], item["commit"]
        existing = self.tag_commit(tag)
        if existing is not None:
            if existing != commit:
                raise SyncError("Destination tag differs; never replace it")
            return
        self.require_tag_policy(existing)
        source = item["repo"]
        if source.git is None:
            source.git = source.api("")["http_url_to_repo"]
        if self.git is None:
            self.git = self.api("")["http_url_to_repo"]
        def git(stage, *args, env=None):
            result = subprocess.run(["git", "-C", str(directory), *args], capture_output=True, env=env)
            if result.returncode:
                classification = _git_failure_classification(result.stderr)
                raise SyncError(
                    f"Tag transfer refused or failed (tag={tag}; stage={stage}; "
                    f"exit={result.returncode}; classification={classification})"
                )
            return result.stdout.decode().strip()
        git("init", "init", "--bare", "--quiet")
        git("fetch", "fetch", "--no-tags", "--quiet", source.git, f"refs/tags/{tag}:refs/sync/source")
        observed = git("rev-parse", "rev-parse", "refs/sync/source^{}")
        if observed != commit:
            raise SyncError(
                f"Tag transfer refused or failed (tag={tag}; stage=rev-parse; "
                "exit=0; classification=source-tag-moved)"
            )
        if not self.token:
            raise SyncError("Missing destination credential")
        if self.provider == "github" and self.github_tag_ssh:
            audit_tag_workflows(git)
            # Preserve the fetched tag object; never push a branch or force a ref.
            git("push", "push", "--quiet", f"git@github.com:{self.repository}.git", f"refs/sync/source:refs/tags/{tag}",
                env={**os.environ, "GIT_TERMINAL_PROMPT": "0"})
            if self.tag_commit(tag) != commit:
                raise SyncError("Created tag readback differs")
            return
        if self.provider == "github":
            # A mirrored commit already exists in GitHub's object database.
            # Creating its lightweight ref needs no Git pack/history transfer;
            # the same trusted GITHUB_TOKEN policy above still applies.
            target = self.api("/git/commits/" + commit, missing=True)
            if target is not None:
                if target.get("sha") != commit:
                    raise SyncError("Destination commit identity differs")
                kind = git("object-type", "cat-file", "-t", "refs/sync/source")
                if kind == "commit":
                    created = self.api("/git/refs", method="POST", body={"ref":"refs/tags/" + tag, "sha":commit})
                    if created.get("ref") != "refs/tags/" + tag or created.get("object", {}).get("sha") != commit:
                        raise SyncError("Created tag identity differs")
                    if self.tag_commit(tag) != commit:
                        raise SyncError("Created tag readback differs")
                    return
        host = urllib.parse.urlsplit(self.git).netloc
        login = "x-access-token" if self.provider == "github" else "oauth2"
        auth = base64.b64encode((login + ":" + self.token).encode()).decode()
        env = {**os.environ, "GIT_TERMINAL_PROMPT":"0", "GIT_CONFIG_COUNT":"1", "GIT_CONFIG_KEY_0":f"http.https://{host}/.extraheader", "GIT_CONFIG_VALUE_0":"Authorization: Basic " + auth}
        options = ["-o", "ci.skip"] if self.provider == "gitlab" else []
        git("push", "push", "--quiet", *options, self.git, f"refs/sync/source:refs/tags/{tag}", env=env)

    def publish(self, item, cache, tags):
        if not self.writable:
            raise SyncError("Read-only repository")
        draft = self.prepare(item,cache,publishing=True)
        observed = self.get(item["tag"])
        if observed is not None and not (draft and observed["id"] == draft["id"]):
            raise SyncError("Destination release appeared during synchronization; re-run")
        self.ensure_tag(item, tags)
        record = release_record(item)
        body = item["body"] + "\n\n" + MARKER + json.dumps(record,ensure_ascii=False,sort_keys=True,separators=(",",":")) + " -->"
        if self.provider == "github":
            release = draft or self.api("/releases", method="POST", body={"tag_name":item["tag"], "name":item["name"], "body":body, "draft":True, "prerelease":item["preview"]})
            present = {x["name"] for x in release.get("assets", [])}
            for name, asset in item["assets"].items():
                if name in present:
                    continue
                data=Path(asset["path"]).read_bytes()
                if hashlib.sha256(data).hexdigest()!=asset["sha256"]:
                    raise SyncError("Cached asset changed before upload")
                url = f"https://uploads.github.com/repos/{self.repository}/releases/{release['id']}/assets?name=" + urllib.parse.quote(name)
                uploaded = request(url, method="POST", token=self.token, provider=self.provider, body=data, raw=True)
                if uploaded.get("size") != asset["length"] or uploaded.get("digest") != "sha256:" + asset["sha256"]:
                    raise SyncError("Uploaded draft asset digest/size was not confirmed")
            self.api(f"/releases/{release['id']}", method="PATCH", body={"draft":False, "make_latest":"false"})
        else:
            links=[]
            for name, asset in item["assets"].items():
                data=Path(asset["path"]).read_bytes()
                if hashlib.sha256(data).hexdigest()!=asset["sha256"]:
                    raise SyncError("Cached asset changed before upload")
                # Package identity includes the immutable tag and digest-qualified
                # file; retries inspect the existing public bytes before uploading.
                path=self.package_path(item,name,asset)
                url=self.base+path
                existing=download(url,cache,asset["sha256"],fresh=True,missing=True)
                if existing is None:
                    self.api(path, method="PUT", body=data, raw=True)
                    download(url,cache,asset["sha256"],fresh=True)
                links.append({"name":name,"url":url,"link_type":"package"})
            self.api("/releases",method="POST",body={"tag_name":item["tag"],"name":item["name"],"description":body,"released_at":item["published"],"assets":{"links":links}})
        actual=self.identity(self.get(item["tag"]),cache,fresh=True)
        if content_identity(actual)!=content_identity(item):
            raise SyncError("Published release readback mismatch")


def release_record(item):
    return {"format":1, "tag":item["tag"], "commit":item["commit"], "source":item["source"], "preview":item.get("preview",False), "assets":{n:{k:v for k,v in a.items() if k in ("sha256", "length", "exe", "split_archive")} for n,a in sorted(item["assets"].items())}}


def content_identity(item):
    return (item["commit"], item.get("preview",False), tuple(sorted((n,a["sha256"],a["length"]) for n,a in item["assets"].items())))


def validate_configuration(github, target):
    if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+",github) or target<=0:
        raise SyncError("Invalid repository configuration")


def plan(inventory, repositories):
    canonical={}
    for key, items in inventory.items():
        for item in items:
            tag=item["tag"]
            if tag in canonical and content_identity(canonical[tag])!=content_identity(item):
                raise SyncError(f"Conflicting immutable release: {tag}")
            canonical.setdefault(tag,item)
    actions=[]
    for repo in repositories:
        if not repo.writable:continue
        present={x["tag"] for x in inventory[repo.key]}
        for tag,item in sorted(canonical.items()):
            if tag not in present:actions.append((repo,item))
    return actions


def local_token(command):
    r=subprocess.run(command,capture_output=True,text=True)
    if r.returncode:raise SyncError("Local credential helper failed")
    return r.stdout.strip()


def save_receipt(path,receipt):
    path=Path(path)
    temporary=path.with_suffix(path.suffix+'.tmp')
    temporary.write_text(json.dumps(receipt,indent=2),encoding="utf-8")
    temporary.replace(path)


def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument("--github",required=True)
    p.add_argument("--gitgud-project",required=True,type=int)
    p.add_argument("--cache",type=Path,required=True)
    p.add_argument("--receipt",type=Path,required=True)
    p.add_argument("--apply",action="store_true")
    p.add_argument("--local-auth",action="store_true")
    p.add_argument("--github-tag-ssh",action="store_true",help="Use the CI mirror SSH identity after auditing historical tag workflows")
    p.add_argument("--tag",action="append",default=[])
    p.add_argument("--write-to",choices=("github","gitgud"),action="append",default=[])
    args=p.parse_args()
    validate_configuration(args.github,args.gitgud_project)
    gh=os.environ.get("GH_TOKEN","");gl=os.environ.get("GITGUD_RELEASE_TOKEN","")
    if args.local_auth:
        gh=gh or local_token(["gh","auth","token"])
        gl=gl or local_token(["glab","config","get","token","--host","gitgud.io"])
    repos=[Repository("gitgud","gitlab",args.gitgud_project,gl,True),Repository("github","github",args.github,gh,True)]
    repos[1].github_tag_ssh = args.github_tag_ssh
    if args.write_to:
        for repo in repos:
            repo.writable = repo.writable and repo.key in args.write_to
    inventory={}
    for repo in repos:
        rows=[x for x in repo.list() if not args.tag or x["tag_name"] in args.tag]
        inventory[repo.key]=[repo.identity(row,args.cache,fresh=True) for row in rows]
        print(f"Verified {repo.key}: {len(rows)} client releases",flush=True)
    actions=plan(inventory,repos)
    for repo,item in actions:
        repo.prepare(item,args.cache,publishing=args.apply)
    receipt={"timeUtc":datetime.now(timezone.utc).isoformat(),"apply":args.apply,"verified":{key:[release_record(x) for x in rows] for key,rows in inventory.items()},"actions":[{"target":r.key,"tag":x["tag"],"identity":content_identity(x)} for r,x in actions],"completed":[]}
    args.receipt.parent.mkdir(parents=True,exist_ok=True)
    save_receipt(args.receipt,receipt)
    for repo,item in actions:
        if args.apply:
            # Revalidate the source release and tag immediately before publishing.
            observed=item["repo"].identity(item["repo"].get(item["tag"]),args.cache,fresh=True)
            if content_identity(observed)!=content_identity(item):raise SyncError("Source release moved")
            with tempfile.TemporaryDirectory(prefix="client-release-tag-") as directory:
                repo.publish(item,args.cache,directory)
            receipt["completed"].append({"target":repo.key,"tag":item["tag"],"verified":True})
            save_receipt(args.receipt,receipt)
            print(f"Published and verified {repo.key}: {item['tag']}",flush=True)
    print(json.dumps({"planned":len(actions),"completed":len(receipt['completed'])}))


if __name__=="__main__":
    try:main()
    except SyncError as error:raise SystemExit(str(error))
