"""Mirror the fixed 2026-11-27 selected resource Release to GitGud.

This command intentionally contains one pinned Release payload. It accepts no
arbitrary tag, commit, project, asset names, or release body.
"""

import argparse
from dataclasses import dataclass
import hashlib
import json
import os
from pathlib import Path
import sys
import urllib.error
import urllib.parse
import urllib.request


GITGUD_HOST = "gitgud.io"
PROJECT_ID = 51684
PROJECT_PATH = "16ww/servers5a3"
SOURCE_REPOSITORY = "Cec1c/ServerS5A3"
SOURCE_RELEASE_ID = 459806188
TAG = "selected-resources-20261127"
SOURCE_COMMIT = "94026b515a60ef13feea35c0193c6649d4c8651b"
RELEASED_AT = "2026-11-26T19:06:36Z"
RELEASE_NAME = "Selected resource assets 2026-11-27"
PACKAGE_NAMESPACE = "dnf-selected"
MAX_RESPONSE = 16 * 1024 * 1024


@dataclass(frozen=True)
class AssetSpec:
    name: str
    size: int
    sha256: str


ASSETS = (
    AssetSpec(
        "SHA256SUMS",
        86,
        "22660b731b2d8edec3f1c4edcd2a93707960760fb8a76ebde2f88f3e31184739",
    ),
    AssetSpec(
        "weapon-skin-npk.manifest.json",
        1083,
        "56650225b6f71ff8d44f94dd624761642be00191123705dc5bafad1a1bdb462b",
    ),
    AssetSpec(
        "weapon-skin-npk.zip",
        6965428,
        "0c65cefd06ca855247ed908c59f6f80aa0202f39414261372bc65ef2c88cd27b",
    ),
)


class MirrorError(RuntimeError):
    pass


def initial_receipt(apply=False):
    return {
        "mode": "apply" if apply else "plan",
        "project_id": PROJECT_ID,
        "project": PROJECT_PATH,
        "source_release_id": SOURCE_RELEASE_ID,
        "tag": TAG,
        "commit": SOURCE_COMMIT,
        "status": "starting",
        "passed": False,
        "write_attempted": False,
        "last_operation": None,
        "operations": [],
        "tag_status": None,
        "assets": [],
        "release_status": None,
        "release_url": (
            "https://"
            + GITGUD_HOST
            + "/"
            + PROJECT_PATH
            + "/-/releases/"
            + TAG
        ),
        "error": None,
    }


def _checkpoint(receipt, checkpoint):
    if checkpoint is not None:
        checkpoint(receipt)


def _write_operation(receipt, checkpoint, operation, write):
    receipt["status"] = "writing"
    receipt["write_attempted"] = True
    receipt["last_operation"] = operation
    entry = {"operation": operation, "status": "started"}
    receipt["operations"].append(entry)
    _checkpoint(receipt, checkpoint)
    try:
        result = write()
    except Exception:
        entry["status"] = "unknown"
        receipt["status"] = "unknown"
        _checkpoint(receipt, checkpoint)
        raise
    entry["status"] = "completed"
    receipt["status"] = "in_progress"
    _checkpoint(receipt, checkpoint)
    return result


def sha256_bytes(data):
    return hashlib.sha256(data).hexdigest()


def load_assets(asset_dir, assets=ASSETS):
    root = Path(asset_dir)
    if not root.is_dir():
        raise MirrorError("asset directory is missing")
    expected = {asset.name: asset for asset in assets}
    try:
        actual = {path.name for path in root.iterdir() if path.is_file()}
    except OSError:
        raise MirrorError("asset directory cannot be read") from None
    unexpected = sorted(actual - set(expected))
    if unexpected:
        raise MirrorError("unexpected asset files: " + ", ".join(unexpected))

    loaded = {}
    for name, spec in expected.items():
        path = root / name
        if not path.is_file():
            raise MirrorError("missing asset " + name)
        data = path.read_bytes()
        if len(data) != spec.size or sha256_bytes(data) != spec.sha256:
            raise MirrorError(name + ": size or SHA256 mismatch")
        loaded[name] = data
    return loaded


def package_path(asset, tag=TAG):
    version = tag + "-" + asset.sha256[:16]
    return "/packages/generic/" + PACKAGE_NAMESPACE + "/" + version + "/" + asset.name


def package_url(asset, tag=TAG):
    return (
        "https://"
        + GITGUD_HOST
        + "/api/v4/projects/"
        + str(PROJECT_ID)
        + package_path(asset, tag)
    )


def release_description():
    asset_lines = "\n".join(
        "- `" + asset.name + "` — " + str(asset.size) + " bytes; SHA256 `" + asset.sha256 + "`"
        for asset in ASSETS
    )
    return """Selected weapon-skin resource assets for 2026-11-27.

Public source tag: `""" + TAG + """`
Public source commit: `""" + SOURCE_COMMIT + """`
GitHub Release: https://github.com/""" + SOURCE_REPOSITORY + """/releases/tag/""" + TAG + """

Assets:
""" + asset_lines + """

This is a resource-only mirror. It contains no DNF.exe, Script.pvf,
player-channel update, server deployment, or announcement.
"""


def release_payload(assets=ASSETS):
    return {
        "tag_name": TAG,
        "name": RELEASE_NAME,
        "description": release_description(),
        "released_at": RELEASED_AT,
        "assets": {
            "links": [
                {
                    "name": asset.name,
                    "url": package_url(asset),
                    "link_type": "package",
                }
                for asset in assets
            ]
        },
    }


def _verify_project(project):
    if not isinstance(project, dict):
        raise MirrorError("GitGud project response is missing")
    if str(project.get("id")) != str(PROJECT_ID):
        raise MirrorError("GitGud project ID mismatch")
    if project.get("path_with_namespace") != PROJECT_PATH:
        raise MirrorError("GitGud project path mismatch")
    if project.get("visibility") != "public":
        raise MirrorError("GitGud project is not public")


def _verify_tag(row):
    if not isinstance(row, dict) or row.get("name") != TAG:
        raise MirrorError("GitGud tag response mismatch")
    commit = row.get("commit")
    if not isinstance(commit, dict) or commit.get("id") != SOURCE_COMMIT:
        raise MirrorError("GitGud tag commit mismatch")


def _verify_package(name, data, spec):
    if data is None:
        raise MirrorError(name + ": package is missing after readback")
    if len(data) != spec.size or sha256_bytes(data) != spec.sha256:
        raise MirrorError(name + ": package size or SHA256 mismatch")


def _verify_release(row, assets=ASSETS):
    if not isinstance(row, dict) or row.get("tag_name") != TAG:
        raise MirrorError("GitGud release response mismatch")
    if row.get("name") != RELEASE_NAME or row.get("description") != release_description():
        raise MirrorError("GitGud release metadata mismatch")
    commit = row.get("commit")
    if not isinstance(commit, dict) or commit.get("id") != SOURCE_COMMIT:
        raise MirrorError("GitGud release commit mismatch")
    actual = row.get("assets")
    actual = actual.get("links") if isinstance(actual, dict) else None
    if not isinstance(actual, list):
        raise MirrorError("GitGud release assets mismatch")
    expected_links = sorted(
        (asset.name, package_url(asset), "package") for asset in assets
    )
    actual_links = []
    for link in actual:
        if not isinstance(link, dict):
            raise MirrorError("GitGud release assets mismatch")
        name = link.get("name")
        url = link.get("url")
        link_type = link.get("link_type")
        if not all(isinstance(value, str) for value in (name, url, link_type)):
            raise MirrorError("GitGud release assets mismatch")
        actual_links.append((name, url, link_type))
    actual_links.sort()
    if actual_links != expected_links or len(actual) != len(expected_links):
        raise MirrorError("GitGud release assets mismatch")


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise MirrorError("HTTP redirect refused")


class SafeAnonymousRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        parsed = urllib.parse.urlsplit(newurl)
        if (
            parsed.scheme != "https"
            or parsed.username
            or parsed.password
            or parsed.fragment
        ):
            raise MirrorError("unsafe anonymous redirect refused")
        return super().redirect_request(req, fp, code, msg, headers, newurl)


class GitGudClient:
    def __init__(self, token, project=PROJECT_ID, host=GITGUD_HOST):
        if not token or "\n" in token or "\r" in token:
            raise MirrorError("GitGud token is missing or malformed")
        self.token = token
        self.project = int(project)
        self.host = host
        self.auth_opener = urllib.request.build_opener(NoRedirect())
        self.public_opener = urllib.request.build_opener(SafeAnonymousRedirect())

    def _url(self, path):
        return (
            "https://"
            + self.host
            + "/api/v4/projects/"
            + str(self.project)
            + path
        )

    def _request(
        self,
        method,
        path,
        *,
        authenticated,
        payload=None,
        raw=None,
        missing=False,
    ):
        headers = {
            "User-Agent": "ServerS5A3-selected-resource-mirror/1",
            "Accept": "application/json",
        }
        if authenticated:
            headers["PRIVATE-TOKEN"] = self.token
        body = None
        if payload is not None:
            body = json.dumps(payload, separators=(",", ":")).encode("utf-8")
            headers["Content-Type"] = "application/json"
        elif raw is not None:
            body = raw
            headers["Content-Type"] = "application/octet-stream"
        request = urllib.request.Request(
            self._url(path), data=body, headers=headers, method=method
        )
        try:
            opener = self.auth_opener if authenticated else self.public_opener
            with opener.open(request, timeout=120) as response:
                data = response.read(MAX_RESPONSE + 1)
        except urllib.error.HTTPError as error:
            if missing and error.code == 404:
                return None
            raise MirrorError(
                "GitGud API " + method + " " + path + " failed with HTTP " + str(error.code)
            ) from None
        except (TimeoutError, urllib.error.URLError):
            raise MirrorError(
                "GitGud API " + method + " " + path + " network failure"
            ) from None
        if len(data) > MAX_RESPONSE:
            raise MirrorError("GitGud API response exceeds limit")
        return data

    def get_json(self, path, missing=False):
        data = self._request(
            "GET", path, authenticated=True, missing=missing
        )
        if data is None or not data:
            return None
        try:
            return json.loads(data)
        except (UnicodeDecodeError, json.JSONDecodeError):
            raise MirrorError("GitGud API returned invalid JSON") from None

    def public_get_bytes(self, path, missing=False):
        return self._request(
            "GET", path, authenticated=False, missing=missing
        )

    def post_json(self, path, payload):
        data = self._request(
            "POST", path, authenticated=True, payload=payload
        )
        if not data:
            return None
        try:
            return json.loads(data)
        except (UnicodeDecodeError, json.JSONDecodeError):
            raise MirrorError("GitGud API returned invalid JSON") from None

    def put_bytes(self, path, data):
        result = self._request(
            "PUT", path, authenticated=True, raw=data
        )
        if not result:
            return None
        try:
            return json.loads(result)
        except (UnicodeDecodeError, json.JSONDecodeError):
            return None


def _ensure_tag(client, apply, receipt, checkpoint):
    path = "/repository/tags/" + urllib.parse.quote(TAG, safe="")
    current = client.get_json(path, missing=True)
    if current is not None:
        _verify_tag(current)
        receipt["tag_status"] = "existing"
        return
    if not apply:
        receipt["tag_status"] = "pending"
        return
    _write_operation(
        receipt,
        checkpoint,
        "create_tag",
        lambda: client.post_json(
            "/repository/tags", {"tag_name": TAG, "ref": SOURCE_COMMIT}
        ),
    )
    _verify_tag(client.get_json(path))
    receipt["tag_status"] = "created"


def _ensure_packages(client, local_assets, apply, receipt, assets, checkpoint):
    for asset in assets:
        path = package_path(asset)
        data = local_assets[asset.name]
        current = client.public_get_bytes(path, missing=True)
        if current is not None:
            _verify_package(asset.name, current, asset)
            status = "existing"
        elif not apply:
            status = "pending"
        else:
            _write_operation(
                receipt,
                checkpoint,
                "upload:" + asset.name,
                lambda: client.put_bytes(path, data),
            )
            _verify_package(
                asset.name,
                client.public_get_bytes(path),
                asset,
            )
            status = "created"
        receipt["assets"].append(
            {
                "name": asset.name,
                "bytes": asset.size,
                "sha256": asset.sha256,
                "url": package_url(asset),
                "status": status,
            }
        )


def _ensure_release(client, apply, receipt, assets, checkpoint):
    path = "/releases/" + urllib.parse.quote(TAG, safe="")
    current = client.get_json(path, missing=True)
    if current is not None:
        _verify_release(current, assets)
        receipt["release_status"] = "existing"
        return
    if not apply:
        receipt["release_status"] = "pending"
        return
    _write_operation(
        receipt,
        checkpoint,
        "publish_release",
        lambda: client.post_json("/releases", release_payload(assets)),
    )
    _verify_release(client.get_json(path), assets)
    receipt["release_status"] = "created"


def mirror_resource_release(
    client,
    asset_dir,
    apply=False,
    assets=ASSETS,
    receipt=None,
    checkpoint=None,
):
    receipt = receipt or initial_receipt(apply)
    try:
        _checkpoint(receipt, checkpoint)
        local_assets = load_assets(asset_dir, assets)
        project = client.get_json("")
        _verify_project(project)
        _ensure_tag(client, apply, receipt, checkpoint)
        _ensure_packages(client, local_assets, apply, receipt, assets, checkpoint)
        _ensure_release(client, apply, receipt, assets, checkpoint)
        receipt["status"] = "complete"
        receipt["passed"] = True
        _checkpoint(receipt, checkpoint)
        return receipt
    except Exception as error:
        if receipt["write_attempted"]:
            receipt["status"] = "unknown"
            if receipt["operations"]:
                last = receipt["operations"][-1]
                if last["status"] in ("started", "completed"):
                    last["status"] = "unknown"
        else:
            receipt["status"] = "failed"
        receipt["passed"] = False
        receipt["error"] = str(error)[:500]
        _checkpoint(receipt, checkpoint)
        raise


def parse_args(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--asset-dir", type=Path, required=True)
    parser.add_argument("--receipt", type=Path, required=True)
    parser.add_argument("--apply", action="store_true")
    return parser.parse_args(argv)


def save_receipt(path, receipt):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_name(path.name + ".tmp")
    temporary.write_text(
        json.dumps(receipt, ensure_ascii=False, indent=2) + "\n",
        encoding="utf-8",
    )
    temporary.replace(path)


def main(argv=None):
    args = parse_args(argv)
    receipt = initial_receipt(args.apply)
    try:
        save_receipt(args.receipt, receipt)
        project = int(os.environ.get("RELEASE_PROJECT", ""))
        if project != PROJECT_ID:
            raise MirrorError("RELEASE_PROJECT must be " + str(PROJECT_ID))
        token = os.environ.get("GITGUD_RELEASE_TOKEN", "")
        client = GitGudClient(token, project)
        receipt = mirror_resource_release(
            client,
            args.asset_dir,
            args.apply,
            receipt=receipt,
            checkpoint=lambda value: save_receipt(args.receipt, value),
        )
        print(json.dumps(receipt, ensure_ascii=False, sort_keys=True))
        return 0
    except Exception as error:
        receipt["status"] = (
            "unknown" if receipt["write_attempted"] else "failed"
        )
        receipt["passed"] = False
        receipt["error"] = str(error)[:500]
        save_receipt(args.receipt, receipt)
        print("selected resource mirror failed: " + str(error), file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
