import copy
import hashlib
import json
from pathlib import Path
import tempfile
import unittest
import urllib.error
import urllib.request

import mirror_selected_resource_release as mirror


TEST_ASSETS = (
    mirror.AssetSpec("SHA256SUMS", 10, hashlib.sha256(b"checksums\n").hexdigest()),
    mirror.AssetSpec("manifest.json", 12, hashlib.sha256(b'{"schema":1}').hexdigest()),
    mirror.AssetSpec("resource.zip", 12, hashlib.sha256(b"resource-zip").hexdigest()),
)
LOCAL_BYTES = {
    "SHA256SUMS": b"checksums\n",
    "manifest.json": b'{"schema":1}',
    "resource.zip": b"resource-zip",
}


class FakeGitGud:
    def __init__(self):
        self.project = {
            "id": mirror.PROJECT_ID,
            "path_with_namespace": mirror.PROJECT_PATH,
            "visibility": "public",
        }
        self.tag = None
        self.packages = {}
        self.release = None
        self.writes = []
        self.reads = []

    def get_json(self, path, missing=False):
        self.reads.append(("GET", path))
        if path == "":
            return self.project
        if path == "/repository/tags/" + mirror.TAG:
            return self.tag
        if path == "/releases/" + mirror.TAG:
            return self.release
        if missing:
            return None
        raise AssertionError("unexpected JSON read " + path)

    def public_get_bytes(self, path, missing=False):
        self.reads.append(("GET", path))
        value = self.packages.get(path)
        if value is None and not missing:
            raise AssertionError("unexpected missing package " + path)
        return value

    def post_json(self, path, payload):
        self.writes.append(("POST", path, payload))
        if path == "/repository/tags":
            self.tag = {"name": payload["tag_name"], "commit": {"id": payload["ref"]}}
            return self.tag
        if path == "/releases":
            self.release = {
                "tag_name": payload["tag_name"],
                "name": payload["name"],
                "description": payload["description"],
                "commit": {"id": mirror.SOURCE_COMMIT},
                "assets": payload["assets"],
            }
            return self.release
        raise AssertionError("unexpected JSON write " + path)

    def put_bytes(self, path, data):
        self.writes.append(("PUT", path, data))
        self.packages[path] = data
        return {"message": "201 Created"}


class FailingOpener:
    def __init__(self, error):
        self.error = error

    def open(self, request, timeout):
        raise self.error


class FailingPutGitGud(FakeGitGud):
    def put_bytes(self, path, data):
        if path.endswith("/manifest.json"):
            raise mirror.MirrorError("package upload failed")
        return super().put_bytes(path, data)


class MirrorTests(unittest.TestCase):
    def asset_dir(self, directory):
        root = Path(directory)
        for name, data in LOCAL_BYTES.items():
            (root / name).write_bytes(data)
        return root

    def test_fixed_payload_is_resource_only_and_exact(self):
        self.assertEqual(mirror.PROJECT_ID, 51684)
        self.assertEqual(mirror.PROJECT_PATH, "16ww/servers5a3")
        self.assertEqual(mirror.TAG, "selected-resources-20261127")
        self.assertEqual(mirror.SOURCE_COMMIT, "94026b515a60ef13feea35c0193c6649d4c8651b")
        self.assertEqual(mirror.SOURCE_RELEASE_ID, 459806188)
        actual = {
            (asset.name, asset.size, asset.sha256)
            for asset in mirror.ASSETS
        }
        self.assertEqual(actual, {
            ("SHA256SUMS", 86, "22660b731b2d8edec3f1c4edcd2a93707960760fb8a76ebde2f88f3e31184739"),
            ("weapon-skin-npk.manifest.json", 1083, "56650225b6f71ff8d44f94dd624761642be00191123705dc5bafad1a1bdb462b"),
            ("weapon-skin-npk.zip", 6965428, "0c65cefd06ca855247ed908c59f6f80aa0202f39414261372bc65ef2c88cd27b"),
        })

    def test_plan_mode_inspects_without_writes(self):
        client = FakeGitGud()
        with tempfile.TemporaryDirectory() as directory:
            receipt = mirror.mirror_resource_release(
                client, self.asset_dir(directory), apply=False, assets=TEST_ASSETS
            )
        self.assertEqual(client.writes, [])
        self.assertEqual(receipt["mode"], "plan")
        self.assertEqual({item["status"] for item in receipt["assets"]}, {"pending"})
        self.assertEqual(receipt["release_status"], "pending")

    def test_apply_creates_tag_three_packages_and_release_then_is_idempotent(self):
        client = FakeGitGud()
        with tempfile.TemporaryDirectory() as directory:
            assets = self.asset_dir(directory)
            first = mirror.mirror_resource_release(client, assets, apply=True, assets=TEST_ASSETS)
            writes_after_first = list(client.writes)
            second = mirror.mirror_resource_release(client, assets, apply=True, assets=TEST_ASSETS)

        self.assertEqual(first["tag_status"], "created")
        self.assertEqual([item["status"] for item in first["assets"]], ["created", "created", "created"])
        self.assertEqual(first["release_status"], "created")
        self.assertEqual(len(writes_after_first), 5)
        self.assertEqual(client.writes, writes_after_first)
        self.assertEqual(second["tag_status"], "existing")
        self.assertEqual([item["status"] for item in second["assets"]], ["existing", "existing", "existing"])
        self.assertEqual(second["release_status"], "existing")

        tag_payload = client.writes[0][2]
        self.assertEqual(tag_payload, {"tag_name": mirror.TAG, "ref": mirror.SOURCE_COMMIT})
        release_payload = client.writes[-1][2]
        self.assertEqual(release_payload["tag_name"], mirror.TAG)
        self.assertEqual(release_payload["released_at"], "2026-11-26T19:06:36Z")
        self.assertEqual(
            {link["name"] for link in release_payload["assets"]["links"]},
            {asset.name for asset in TEST_ASSETS},
        )
        self.assertEqual(
            {call[0] for call in client.writes},
            {"POST", "PUT"},
        )

    def test_mismatched_existing_tag_refuses_before_writes(self):
        client = FakeGitGud()
        client.tag = {"name": mirror.TAG, "commit": {"id": "0" * 40}}
        with tempfile.TemporaryDirectory() as directory:
            with self.assertRaisesRegex(mirror.MirrorError, "tag"):
                mirror.mirror_resource_release(
                    client, self.asset_dir(directory), apply=True, assets=TEST_ASSETS
                )
        self.assertEqual(client.writes, [])

    def test_local_asset_mismatch_refuses_before_client_access(self):
        client = FakeGitGud()
        with tempfile.TemporaryDirectory() as directory:
            assets = self.asset_dir(directory)
            (assets / "resource.zip").write_bytes(b"changed")
            with self.assertRaisesRegex(mirror.MirrorError, "resource.zip"):
                mirror.mirror_resource_release(client, assets, apply=True, assets=TEST_ASSETS)
        self.assertEqual(client.reads, [])
        self.assertEqual(client.writes, [])

    def test_missing_asset_directory_is_a_domain_error(self):
        with tempfile.TemporaryDirectory() as directory:
            missing = Path(directory) / "missing"
            with self.assertRaisesRegex(mirror.MirrorError, "asset directory"):
                mirror.load_assets(missing)

    def test_unexpected_asset_file_refuses_before_client_access(self):
        client = FakeGitGud()
        with tempfile.TemporaryDirectory() as directory:
            assets = self.asset_dir(directory)
            (assets / "extra.bin").write_bytes(b"extra")
            with self.assertRaisesRegex(mirror.MirrorError, "extra.bin"):
                mirror.mirror_resource_release(
                    client, assets, apply=True, assets=TEST_ASSETS
                )
        self.assertEqual(client.reads, [])
        self.assertEqual(client.writes, [])

    def test_wrong_project_refuses_before_writes(self):
        client = FakeGitGud()
        client.project["id"] = 1
        with tempfile.TemporaryDirectory() as directory:
            with self.assertRaisesRegex(mirror.MirrorError, "project"):
                mirror.mirror_resource_release(
                    client, self.asset_dir(directory), apply=True, assets=TEST_ASSETS
                )
        self.assertEqual(client.writes, [])

    def test_authenticated_errors_and_redirects_never_expose_or_forward_token(self):
        client = mirror.GitGudClient("test-secret")
        error = urllib.error.HTTPError(
            "https://gitgud.io/api/v4/projects/51684", 401, "unauthorized", {}, None
        )
        client.auth_opener = FailingOpener(error)
        with self.assertRaises(mirror.MirrorError) as raised:
            client.get_json("")
        self.assertNotIn("test-secret", str(raised.exception))

        request = urllib.request.Request(
            "https://gitgud.io/api/v4/projects/51684",
            headers={"PRIVATE-TOKEN": "test-secret"},
        )
        for target in (
            "https://gitgud.io/redirected",
            "http://gitgud.io/plain-http",
            "https://user:pass@gitgud.io/has-userinfo",
        ):
            with self.subTest(target=target), self.assertRaises(mirror.MirrorError):
                redirect = (
                    mirror.NoRedirect()
                    if target == "https://gitgud.io/redirected"
                    else mirror.SafeAnonymousRedirect()
                )
                redirect.redirect_request(request, None, 302, "", {}, target)

    def test_receipt_checkpoints_each_write_and_records_unknown_on_failure(self):
        snapshots = []

        def checkpoint(value):
            snapshots.append(copy.deepcopy(value))

        client = FakeGitGud()
        with tempfile.TemporaryDirectory() as directory:
            mirror.mirror_resource_release(
                client,
                self.asset_dir(directory),
                apply=True,
                assets=TEST_ASSETS,
                checkpoint=checkpoint,
            )
        for operation in (
            "create_tag",
            "upload:SHA256SUMS",
            "upload:manifest.json",
            "upload:resource.zip",
            "publish_release",
        ):
            with self.subTest(operation=operation):
                self.assertTrue(
                    any(
                        item["last_operation"] == operation
                        and item["operations"][-1]["status"] == "started"
                        for item in snapshots
                    )
                )

        snapshots.clear()
        client = FailingPutGitGud()
        with tempfile.TemporaryDirectory() as directory:
            with self.assertRaisesRegex(mirror.MirrorError, "package upload failed"):
                mirror.mirror_resource_release(
                    client,
                    self.asset_dir(directory),
                    apply=True,
                    assets=TEST_ASSETS,
                    checkpoint=checkpoint,
                )
        final = snapshots[-1]
        self.assertFalse(final["passed"])
        self.assertEqual(final["status"], "unknown")
        self.assertEqual(final["last_operation"], "upload:manifest.json")
        self.assertEqual(final["operations"][-1]["status"], "unknown")
        self.assertEqual(final["error"], "package upload failed")
        serialized = json.dumps(final)
        self.assertNotIn("test-secret", serialized)
        self.assertNotIn("resource-zip", serialized)

    def test_existing_package_mismatch_refuses_before_writes(self):
        client = FakeGitGud()
        client.tag = {"name": mirror.TAG, "commit": {"id": mirror.SOURCE_COMMIT}}
        first = TEST_ASSETS[0]
        client.packages[mirror.package_path(first, mirror.TAG)] = b"wrong-size"
        with tempfile.TemporaryDirectory() as directory:
            with self.assertRaisesRegex(mirror.MirrorError, first.name):
                mirror.mirror_resource_release(
                    client, self.asset_dir(directory), apply=True, assets=TEST_ASSETS
                )
        self.assertEqual(client.writes, [])

    def test_existing_release_mismatch_refuses_before_writes(self):
        client = FakeGitGud()
        client.tag = {"name": mirror.TAG, "commit": {"id": mirror.SOURCE_COMMIT}}
        for asset in TEST_ASSETS:
            client.packages[mirror.package_path(asset, mirror.TAG)] = LOCAL_BYTES[asset.name]
        client.release = {
            "tag_name": mirror.TAG,
            "name": mirror.RELEASE_NAME,
            "description": mirror.release_description(),
            "commit": {"id": mirror.SOURCE_COMMIT},
            "assets": {"links": []},
        }
        with tempfile.TemporaryDirectory() as directory:
            with self.assertRaisesRegex(mirror.MirrorError, "release"):
                mirror.mirror_resource_release(
                    client, self.asset_dir(directory), apply=True, assets=TEST_ASSETS
                )
        self.assertEqual(client.writes, [])

    def test_existing_release_metadata_or_malformed_links_refuse(self):
        client = FakeGitGud()
        client.tag = {"name": mirror.TAG, "commit": {"id": mirror.SOURCE_COMMIT}}
        for asset in TEST_ASSETS:
            client.packages[mirror.package_path(asset, mirror.TAG)] = LOCAL_BYTES[asset.name]
        payload = mirror.release_payload(TEST_ASSETS)
        for release in (
            {
                "tag_name": mirror.TAG,
                "name": "wrong",
                "description": payload["description"],
                "commit": {"id": mirror.SOURCE_COMMIT},
                "assets": payload["assets"],
            },
            {
                "tag_name": mirror.TAG,
                "name": payload["name"],
                "description": payload["description"],
                "commit": {"id": mirror.SOURCE_COMMIT},
                "assets": {"links": [{"name": None}]},
            },
        ):
            client.release = release
            with tempfile.TemporaryDirectory() as directory:
                with self.assertRaisesRegex(mirror.MirrorError, "release"):
                    mirror.mirror_resource_release(
                        client, self.asset_dir(directory), apply=True, assets=TEST_ASSETS
                    )
        self.assertEqual(client.writes, [])

    def test_workflow_is_manual_read_only_and_uses_installed_secret_and_project(self):
        workflow = (
            Path(__file__).resolve().parents[2] / ".github/workflows/selected-resource-mirror.yml"
        ).read_text(encoding="utf-8")
        self.assertIn("workflow_dispatch:", workflow)
        self.assertNotIn("  release:", workflow)
        self.assertIn("permissions:\n  contents: read", workflow)
        self.assertIn("${{ secrets.GITGUD_CLIENT_RELEASE_TOKEN }}", workflow)
        self.assertIn("${{ vars.GITGUD_CLIENT_RELEASE_PROJECT }}", workflow)
        self.assertIn("github.ref == 'refs/heads/master'", workflow)
        self.assertIn("selected-resources-20261127", workflow)
        self.assertIn(mirror.SOURCE_COMMIT, workflow)
        self.assertIn(str(mirror.SOURCE_RELEASE_ID), workflow)
        self.assertIn("persist-credentials: false", workflow)
        self.assertIn("ref: ${{ github.sha }}", workflow)
        self.assertNotIn("ref: master", workflow)
        self.assertNotIn("git push", workflow)
        self.assertNotIn("sync_repositories", workflow)


if __name__ == "__main__":
    unittest.main()
