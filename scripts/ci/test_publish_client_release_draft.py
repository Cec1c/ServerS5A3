import copy
from contextlib import contextmanager
import io
import json
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
import urllib.error
import urllib.request
import zipfile

import publish_client_release_draft as publication
import sync_client_releases as sync


HEAD = "a" * 40
TAG = "client-v211-trusted-draft"
RELEASE_ID = 21101
ASSET_ID = 21102


class Recorder:
    """Small local spy; importing unittest.mock loads asyncio on this host."""

    def __init__(self, *, result=None, error=None):
        self.result = result
        self.error = error
        self.calls = []

    def __call__(self, *args, **kwargs):
        self.calls.append(SimpleNamespace(args=args, kwargs=kwargs))
        if self.error is not None:
            raise self.error
        return self.result

    @property
    def call_args(self):
        if not self.calls:
            raise AssertionError("recorder was not called")
        return self.calls[-1]

    def assert_not_called(self):
        if self.calls:
            raise AssertionError(f"expected no calls, got {self.calls!r}")

    def assert_called_once(self):
        if len(self.calls) != 1:
            raise AssertionError(f"expected one call, got {self.calls!r}")


@contextmanager
def replace_attr(owner, name, value):
    original = getattr(owner, name)
    setattr(owner, name, value)
    try:
        yield value
    finally:
        setattr(owner, name, original)


def make_client_zip(path, executable=b"MZtest"):
    with zipfile.ZipFile(path, "w") as archive:
        archive.writestr("DNF.exe", executable)
        archive.writestr("evidence/static-check.log", "pass\n")
    return sync.digest(path), path.stat().st_size, sync.inspect_zip(path)


class FakeGitHub:
    def __init__(self, *, head, tag, release, tag_sha=HEAD, fetch_path,
                 latest_id=None, readback_mutator=None, after_fetch=None):
        self.branch_sha = head
        self.tag = tag
        self.release_id = str(release["id"])
        self.release = copy.deepcopy(release)
        self.published = copy.deepcopy(release)
        self.published["draft"] = False
        self.published.setdefault("html_url", "https://github.test/client-v211")
        self.tags = {}
        if tag_sha is not None:
            self.tags[tag] = {
                "ref": "refs/tags/" + tag,
                "object": {"type": "commit", "sha": tag_sha, "url": "ignored"},
                "extra": "ignored by the publication contract",
            }
        self.fetch_path = Path(fetch_path)
        self.latest_id = release["id"] if latest_id is None else latest_id
        self.readback_mutator = readback_mutator
        self.after_fetch = after_fetch
        self.calls = []
        self.fetch_calls = []
        if readback_mutator is not None:
            readback_mutator(self.published)

    def call(self, method, path, body=None, missing=False):
        self.calls.append((method, path, copy.deepcopy(body), missing))
        prefix = "repos/" + publication.REPOSITORY
        if method == "GET" and path.startswith("users/"):
            actor = path.split("/", 1)[1]
            return {"id": publication.ACTORS.get(actor, -1)}
        if method == "GET" and path.startswith(prefix + "/collaborators/"):
            return {"permission": "admin"}
        if method == "GET" and path == prefix + "/branches/master":
            return {"commit": {"sha": self.branch_sha}}
        if method == "GET" and path.startswith(prefix + "/git/ref/tags/"):
            return copy.deepcopy(self.tags.get(path.rsplit("/", 1)[1]))
        if method == "POST" and path == prefix + "/git/refs":
            ref = body["ref"]
            tag = ref.removeprefix("refs/tags/")
            self.tags[tag] = {"ref": ref, "object": {"type": "commit", "sha": body["sha"]}}
            return {}
        if method == "GET" and path == prefix + "/releases/" + self.release_id:
            return copy.deepcopy(self.release)
        if method == "PATCH" and path == prefix + "/releases/" + self.release_id:
            self.release["draft"] = False
            return copy.deepcopy(self.release)
        if method == "GET" and path == prefix + "/releases/tags/" + self.tag:
            return copy.deepcopy(self.published)
        if method == "GET" and path == prefix + "/releases/latest":
            return {"id": self.latest_id}
        raise AssertionError(f"unexpected API call: {method} {path}")

    def fetch(self, asset_id, expected):
        self.fetch_calls.append((asset_id, expected))
        if self.after_fetch is not None:
            self.after_fetch(self)
        return self.fetch_path

    def writes(self):
        return [(method, path, body) for method, path, body, _ in self.calls if method != "GET"]


class PublicationTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.zip_path = Path(self.temporary.name) / "reviewed-client.zip"
        self.zip_sha, self.zip_size, self.exe = make_client_zip(self.zip_path)

    def record(self):
        return {
            "format": 1,
            "tag": TAG,
            "commit": HEAD,
            "preview": False,
            "assets": {
                TAG + ".zip": {
                    "sha256": self.zip_sha,
                    "length": self.zip_size,
                    "exe": copy.deepcopy(self.exe),
                }
            },
        }

    def release_body(self, record):
        return sync.MARKER + json.dumps(record) + " -->"

    def row(self, record=None, draft=True):
        record = self.record() if record is None else record
        return {
            "id": RELEASE_ID,
            "tag_name": TAG,
            "draft": draft,
            "prerelease": False,
            "body": self.release_body(record),
            "html_url": "https://github.test/client-v211",
            "assets": [{
                "id": ASSET_ID,
                "name": TAG + ".zip",
                "size": self.zip_size,
                "state": "uploaded",
                "digest": "sha256:" + self.zip_sha,
                "download_count": 2,
            }],
        }

    def kwargs(self, **changes):
        values = {
            "stage": "publish",
            "repository": publication.REPOSITORY,
            "head": HEAD,
            "tag": TAG,
            "release_id": str(RELEASE_ID),
            "zip_sha": self.zip_sha,
            "exe_sha": self.exe["sha256"],
            "actor": "16www",
            "event": "workflow_dispatch",
            "ref": "refs/heads/master",
            "workflow_sha": HEAD,
        }
        values.update(changes)
        return values

    def fake(self, **changes):
        values = {
            "head": HEAD,
            "tag": TAG,
            "release": self.row(),
            "fetch_path": self.zip_path,
        }
        values.update(changes)
        fake = FakeGitHub(**values)
        return fake, fake.call, fake.fetch

    def assert_no_writes(self, fake):
        self.assertEqual(fake.writes(), [])

    def test_untrusted_repository_event_ref_actor_or_head_is_rejected_without_writes(self):
        cases = [
            ("repository", {"repository": "Other/Repo"}),
            ("event", {"event": "push"}),
            ("ref", {"ref": "refs/heads/feature"}),
            ("actor", {"actor": "mallory"}),
            ("head", {"head": "b" * 40}),
        ]
        for name, changes in cases:
            with self.subTest(name=name):
                calls = []

                def call(method, path, body=None, missing=False):
                    calls.append((method, path))
                    if path.startswith("users/"):
                        return {"id": publication.ACTORS["16www"]}
                    if "/collaborators/" in path:
                        return {"permission": "admin"}
                    raise AssertionError(f"unexpected call: {method} {path}")

                fetch = Recorder()
                with self.assertRaises(sync.SyncError):
                    publication.publish(**self.kwargs(**changes), call=call, fetch=fetch)
                self.assertTrue(all(method == "GET" for method, _ in calls))
                fetch.assert_not_called()

    def test_actor_identity_and_repository_permission_are_bound(self):
        for identity, permission in [(-1, "admin"), (publication.ACTORS["16www"], "read")]:
            with self.subTest(identity=identity, permission=permission):
                calls = []

                def call(method, path, body=None, missing=False):
                    calls.append((method, path))
                    if path.startswith("users/"):
                        return {"id": identity}
                    if "/collaborators/" in path:
                        return {"permission": permission}
                    raise AssertionError(f"unexpected call: {method} {path}")

                with self.assertRaises(sync.SyncError):
                    publication.publish(**self.kwargs(), call=call, fetch=Recorder())
                self.assertTrue(all(method == "GET" for method, _ in calls))

    def test_default_branch_must_still_equal_frozen_head(self):
        fake, call, fetch = self.fake()
        fake.branch_sha = "b" * 40
        with self.assertRaisesRegex(sync.SyncError, "default branch moved"):
            publication.publish(**self.kwargs(stage="prepare-tag"), call=call, fetch=fetch)
        self.assert_no_writes(fake)
        self.assertEqual(fake.fetch_calls, [])

    def test_prepare_tag_creates_exact_tag_and_reads_it_back(self):
        fake, call, fetch = self.fake(tag_sha=None)
        result = publication.publish(**self.kwargs(stage="prepare-tag"), call=call, fetch=fetch)
        self.assertEqual(fake.writes(), [(
            "POST",
            "repos/" + publication.REPOSITORY + "/git/refs",
            {"ref": "refs/tags/" + TAG, "sha": HEAD},
        )])
        self.assertEqual(result["state"], "prepared-tag")
        self.assertFalse(result["tagAlreadyPresent"])
        self.assertEqual(fake.fetch_calls, [])

    def test_prepare_tag_same_commit_is_idempotent_and_different_commit_is_refused(self):
        for tag_sha, expected_error, already in [
            (HEAD, None, True),
            ("b" * 40, "tag differs", None),
        ]:
            with self.subTest(tag_sha=tag_sha):
                fake, call, fetch = self.fake(tag_sha=tag_sha)
                if expected_error:
                    with self.assertRaisesRegex(sync.SyncError, expected_error):
                        publication.publish(**self.kwargs(stage="prepare-tag"), call=call, fetch=fetch)
                else:
                    result = publication.publish(**self.kwargs(stage="prepare-tag"), call=call, fetch=fetch)
                    self.assertTrue(result["tagAlreadyPresent"])
                self.assert_no_writes(fake)
                self.assertEqual(fake.fetch_calls, [])

    def test_release_tag_provenance_and_unique_asset_are_checked_before_download(self):
        def source_tag(record, row):
            record["tag"] = "client-v210"

        def source_commit(record, row):
            record["commit"] = "b" * 40

        def source_preview(record, row):
            record["preview"] = True

        def extra_record_asset(record, row):
            record["assets"]["extra.zip"] = copy.deepcopy(next(iter(record["assets"].values())))

        def expected_zip(record, row):
            record["assets"][TAG + ".zip"]["sha256"] = "f" * 64

        def expected_length(record, row):
            record["assets"][TAG + ".zip"]["length"] += 1

        def expected_exe(record, row):
            record["assets"][TAG + ".zip"]["exe"]["sha256"] = "f" * 64

        cases = [
            ("release-tag", lambda record, row: row.update(tag_name="client-v210")),
            ("prerelease", lambda record, row: row.update(prerelease=True)),
            ("source-marker", lambda record, row: row.update(body="missing marker")),
            ("source-tag", source_tag),
            ("source-commit", source_commit),
            ("source-preview", source_preview),
            ("source-assets", extra_record_asset),
            ("no-asset", lambda record, row: row.update(assets=[])),
            ("two-assets", lambda record, row: row["assets"].append(copy.deepcopy(row["assets"][0]))),
            ("asset-name", lambda record, row: row["assets"][0].update(name="other.zip")),
            ("asset-state", lambda record, row: row["assets"][0].update(state="processing")),
            ("asset-digest", lambda record, row: row["assets"][0].update(digest="sha256:" + "f" * 64)),
            ("record-zip", expected_zip),
            ("record-length", expected_length),
            ("record-exe", expected_exe),
        ]
        for name, mutate in cases:
            with self.subTest(name=name):
                record = self.record()
                row = self.row(record)
                mutate(record, row)
                if name != "source-marker":
                    row["body"] = self.release_body(record)
                fake, call, fetch = self.fake(release=row)
                with self.assertRaises(sync.SyncError):
                    publication.publish(**self.kwargs(), call=call, fetch=fetch)
                self.assertEqual(fake.fetch_calls, [])
                self.assert_no_writes(fake)

    def test_wrong_archive_or_executable_identity_never_patches(self):
        alternate = Path(self.temporary.name) / "alternate.zip"
        alternate_sha, alternate_size, alternate_exe = make_client_zip(alternate, b"MZother")
        broken = Path(self.temporary.name) / "broken.zip"
        broken.write_bytes(b"not a ZIP")
        broken_sha, broken_size, _ = sync.digest(broken), broken.stat().st_size, None

        cases = [
            ("raw-zip", alternate, self.zip_sha, self.zip_size, self.exe),
            ("invalid-zip", broken, broken_sha, broken_size, self.exe),
            ("actual-exe", alternate, alternate_sha, alternate_size, self.exe),
        ]
        for name, fetched, zip_sha, size, expected_exe in cases:
            with self.subTest(name=name):
                record = self.record()
                record["assets"][TAG + ".zip"] = {
                    "sha256": zip_sha,
                    "length": size,
                    "exe": copy.deepcopy(expected_exe),
                }
                row = self.row(record)
                row["assets"][0].update(size=size, digest="sha256:" + zip_sha)
                fake, call, fetch = self.fake(release=row, fetch_path=fetched)
                with self.assertRaises(sync.SyncError):
                    publication.publish(
                        **self.kwargs(zip_sha=zip_sha, exe_sha=expected_exe["sha256"]),
                        call=call,
                        fetch=fetch,
                    )
                self.assert_no_writes(fake)

    def test_exact_draft_is_published_after_real_zip_and_exe_verification(self):
        fake, call, fetch = self.fake(
            readback_mutator=lambda actual: actual["assets"][0].update(download_count=999),
        )
        result = publication.publish(**self.kwargs(), call=call, fetch=fetch)
        self.assertEqual(fake.writes(), [(
            "PATCH",
            "repos/" + publication.REPOSITORY + "/releases/" + str(RELEASE_ID),
            {"draft": False, "make_latest": "true"},
        )])
        self.assertEqual(fake.fetch_calls, [(ASSET_ID, self.zip_sha)])
        self.assertEqual(result["state"], "published")
        self.assertTrue(result["fullDownloadVerified"])
        self.assertFalse(result["alreadyPublished"])

    def test_already_published_identical_input_is_verified_without_patch(self):
        row = self.row(draft=False)
        fake, call, fetch = self.fake(release=row)
        result = publication.publish(**self.kwargs(), call=call, fetch=fetch)
        self.assert_no_writes(fake)
        self.assertEqual(fake.fetch_calls, [(ASSET_ID, self.zip_sha)])
        self.assertTrue(result["alreadyPublished"])
        self.assertTrue(result["fullDownloadVerified"])

    def test_experimental_draft_keeps_stable_latest(self):
        record = self.record()
        record["preview"] = True
        row = self.row(record)
        row["prerelease"] = True
        fake, call, fetch = self.fake(release=row, latest_id=21200)
        result = publication.publish(**self.kwargs(), call=call, fetch=fetch)
        self.assertEqual(fake.writes(), [(
            "PATCH", "repos/" + publication.REPOSITORY + "/releases/" + str(RELEASE_ID),
            {"draft": False, "make_latest": "false"},
        )])
        self.assertEqual(fake.latest_id, 21200)
        self.assertTrue(result["prerelease"])
        self.assertTrue(result["stableLatestPreserved"])
        self.assertTrue(result["fullDownloadVerified"])

    def test_experimental_release_cannot_already_be_stable_latest(self):
        record = self.record()
        record["preview"] = True
        row = self.row(record)
        row["prerelease"] = True
        fake, call, fetch = self.fake(release=row, latest_id=RELEASE_ID)
        with self.assertRaisesRegex(sync.SyncError, "must not be stable Latest"):
            publication.publish(**self.kwargs(), call=call, fetch=fetch)
        self.assert_no_writes(fake)
        self.assertEqual(fake.fetch_calls, [])

    def test_published_readback_mismatch_is_rejected(self):
        def release_id(actual):
            actual["id"] += 1

        def draft(actual):
            actual["draft"] = True

        def prerelease(actual):
            actual["prerelease"] = True

        def asset_id(actual):
            actual["assets"][0]["id"] += 1

        def asset_digest(actual):
            actual["assets"][0]["digest"] = "sha256:" + "f" * 64

        def provenance(actual):
            actual["body"] = "missing marker"

        for name, mutate in [
            ("release-id", release_id), ("draft", draft), ("prerelease", prerelease),
            ("asset-id", asset_id), ("asset-digest", asset_digest), ("provenance", provenance),
        ]:
            with self.subTest(name=name):
                fake, call, fetch = self.fake(readback_mutator=mutate)
                with self.assertRaisesRegex(sync.SyncError, "published release readback differs"):
                    publication.publish(**self.kwargs(), call=call, fetch=fetch)
                self.assertIn("PATCH", [method for method, _, _ in fake.writes()])

    def test_latest_pointer_or_post_download_races_block_publication(self):
        def move_branch(fake):
            fake.branch_sha = "b" * 40

        def move_tag(fake):
            fake.tags[TAG]["object"]["sha"] = "b" * 40

        for name, race, message in [
            ("branch", move_branch, "default branch moved"),
            ("tag", move_tag, "tag moved before publication"),
        ]:
            with self.subTest(name=name):
                fake, call, fetch = self.fake(after_fetch=race)
                with self.assertRaisesRegex(sync.SyncError, message):
                    publication.publish(**self.kwargs(), call=call, fetch=fetch)
                self.assert_no_writes(fake)

        fake, call, fetch = self.fake(latest_id=RELEASE_ID + 1)
        with self.assertRaisesRegex(sync.SyncError, "Latest pointer differs"):
            publication.publish(**self.kwargs(), call=call, fetch=fetch)
        self.assertIn("PATCH", [method for method, _, _ in fake.writes()])


class AssetDownloadAuthenticationTests(unittest.TestCase):
    API = f"https://api.github.com/repos/Cec1c/ServerS5A3/releases/assets/{ASSET_ID}"
    CDN = "https://release-assets.githubusercontent.com/private/client.zip?sig=test"

    def redirect(self, location):
        return urllib.error.HTTPError(self.API, 302, "Found", {"Location": location}, None)

    def test_api_receives_token_and_cdn_redirect_is_anonymous(self):
        data = b"reviewed ZIP bytes"
        expected = sync.hashlib.sha256(data).hexdigest()
        api_open = Recorder(error=self.redirect(self.CDN))
        cdn_open = Recorder(result=io.BytesIO(data))
        build_opener = Recorder(result=SimpleNamespace(open=api_open))
        with tempfile.TemporaryDirectory() as directory, \
                replace_attr(urllib.request, "build_opener", build_opener), \
                replace_attr(sync.OPENER, "open", cdn_open):
            result = publication.fetch_draft_asset(
                publication.REPOSITORY, "workflow-secret", ASSET_ID, expected, directory)
            self.assertEqual(Path(result).read_bytes(), data)

        api_request = api_open.call_args.args[0]
        self.assertEqual(api_request.full_url, self.API)
        self.assertEqual(api_request.get_method(), "GET")
        self.assertEqual(api_request.get_header("Authorization"), "Bearer workflow-secret")
        self.assertEqual(api_request.get_header("Accept"), "application/octet-stream")
        self.assertIsInstance(build_opener.call_args.args[0], urllib.request.HTTPRedirectHandler)

        cdn_request = cdn_open.call_args.args[0]
        self.assertEqual(cdn_request.full_url, self.CDN)
        self.assertFalse(cdn_request.has_header("Authorization"))
        self.assertFalse(cdn_request.has_header("Private-token"))

    def test_cdn_userinfo_and_non_github_origins_are_refused(self):
        locations = [
            "https://user:secret@release-assets.githubusercontent.com/client.zip",
            "https://release-assets.githubusercontent.com.evil.test/client.zip",
            "https://release-assets.githubusercontent.com:444/client.zip",
            "http://release-assets.githubusercontent.com/client.zip",
        ]
        for location in locations:
            with self.subTest(location=location), tempfile.TemporaryDirectory() as directory, \
                    replace_attr(urllib.request, "build_opener", Recorder(result=SimpleNamespace(
                        open=Recorder(error=self.redirect(location))))), \
                    replace_attr(sync, "download", download := Recorder()):
                with self.assertRaises(sync.SyncError):
                    publication.fetch_draft_asset(
                        publication.REPOSITORY, "workflow-secret", ASSET_ID, "a" * 64, directory)
                download.assert_not_called()

    def test_non_redirect_api_error_never_reaches_cdn(self):
        error = urllib.error.HTTPError(self.API, 404, "Not Found", {}, None)
        with tempfile.TemporaryDirectory() as directory, \
                replace_attr(urllib.request, "build_opener", Recorder(result=SimpleNamespace(open=Recorder(error=error)))), \
                replace_attr(sync, "download", download := Recorder()):
            with self.assertRaisesRegex(sync.SyncError, "API download failed"):
                publication.fetch_draft_asset(
                    publication.REPOSITORY, "workflow-secret", ASSET_ID, "a" * 64, directory)
            download.assert_not_called()


if __name__ == "__main__":
    unittest.main()
