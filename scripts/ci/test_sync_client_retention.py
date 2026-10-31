import contextlib
import io
import json
from pathlib import Path
from types import SimpleNamespace
import tempfile
import unittest
from unittest.mock import Mock, patch

import sync_client_releases as sync


class ClientReleaseRetentionTests(unittest.TestCase):
    def repository(self, key, tags):
        rows = [{"tag_name": tag} for tag in tags]
        repo = SimpleNamespace(key=key, writable=True, list=Mock(return_value=rows),
                               prepare=Mock(), publish=Mock())

        def identity(row, *_args, **_kwargs):
            tag = row["tag_name"]
            if tag in ("client-v99-old", "client-v179-old", "client-unversioned"):
                raise AssertionError("removed or unversioned release reached asset download")
            return {"tag": tag, "commit": "a" * 40, "source": "https://example.invalid/releases/" + tag,
                    "preview": False, "assets": {}, "repo": repo}

        repo.identity = Mock(side_effect=identity)
        repo.get = Mock(side_effect=lambda tag: next(r for r in rows if r["tag_name"] == tag))
        return repo

    def invoke(self, gl, gh, tags=()):
        with tempfile.TemporaryDirectory() as directory:
            receipt = Path(directory) / "receipt.json"
            argv = ["sync_client_releases.py", "--github", "owner/repo", "--gitgud-project", "1",
                    "--cache", directory, "--receipt", str(receipt), "--apply"]
            for tag in tags:
                argv.extend(["--tag", tag])
            with patch("sys.argv", argv), patch.object(sync, "Repository", side_effect=[gl, gh]), \
                 contextlib.redirect_stdout(io.StringIO()):
                sync.main()
            return json.loads(receipt.read_text(encoding="utf-8"))

    def test_removed_old_releases_are_not_downloaded_or_recreated_in_either_direction(self):
        for old_source in ("gitgud", "github"):
            with self.subTest(old_source=old_source):
                repos = [self.repository(key, ["client-v180-boundary", "client-v183-new"] +
                                        (["client-v99-old", "client-v179-old", "client-unversioned"]
                                         if key == old_source else []))
                         for key in ("gitgud", "github")]
                receipt = self.invoke(*repos)
                self.assertEqual(receipt["actions"], [])
                for repo in repos:
                    repo.prepare.assert_not_called()
                    repo.publish.assert_not_called()

    def test_retained_missing_release_still_publishes_in_both_directions(self):
        for source in ("gitgud", "github"):
            with self.subTest(source=source):
                repos = [self.repository(key, ["client-v180-boundary"] +
                                        (["client-v184-new", "client-v179-old"] if key == source else []))
                         for key in ("gitgud", "github")]
                receipt = self.invoke(*repos)
                destination = next(r for r in repos if r.key != source)
                self.assertEqual(receipt["completed"], [{"target": destination.key, "tag": "client-v184-new", "verified": True}])
                self.assertEqual(destination.publish.call_args.args[0]["tag"], "client-v184-new")

    def test_numeric_boundary_and_unversioned_tags(self):
        rows = [{"tag_name": tag} for tag in ["client-v99", "client-v179-old", "client-v180",
                                             "client-v181-new", "client-v1800-new", "client-latest",
                                             "client-v180oops", "client-v0180"]]
        selected, skipped = sync.select_release_rows(rows, [])
        self.assertEqual([r["tag_name"] for r in selected], ["client-v180", "client-v181-new", "client-v1800-new"])
        self.assertEqual(len(skipped), 5)

    def test_explicit_old_or_unversioned_tag_cannot_bypass_before_credentials_or_network(self):
        for tag in ("client-v179-old", "client-latest", "client-v180oops"):
            with self.subTest(tag=tag), patch("sys.argv", ["sync", "--github", "owner/repo", "--gitgud-project", "1",
                                                           "--cache", ".", "--receipt", "unused.json", "--local-auth", "--tag", tag]), \
                 patch.object(sync, "Repository") as repo, patch.object(sync, "local_token") as token:
                with self.assertRaisesRegex(sync.SyncError, "V180"):
                    sync.main()
                repo.assert_not_called()
                token.assert_not_called()

    def test_current_tag_selection_and_policy_receipt(self):
        repos = [self.repository(key, ["client-v179-old", "client-v180-boundary", "client-v183-new"])
                 for key in ("gitgud", "github")]
        receipt = self.invoke(*repos, tags=["client-v180-boundary"])
        self.assertEqual(receipt["minimumClientVersion"], 180)
        for repo in repos:
            self.assertEqual([r["tag"] for r in receipt["verified"][repo.key]], ["client-v180-boundary"])
            self.assertIn("client-v179-old", receipt["skippedByRetention"][repo.key])


if __name__ == "__main__":
    unittest.main()
