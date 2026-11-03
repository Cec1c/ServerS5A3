import hashlib
import io
from pathlib import Path
import tempfile
import unittest
import urllib.request
from unittest.mock import patch

import sync_client_releases as sync


class PackageAuthenticationTests(unittest.TestCase):
    def setUp(self):
        self.repo = sync.Repository("gitgud", "gitlab", 123, "test-project-token", True)
        self.url = "https://gitgud.io/api/v4/projects/123/packages/generic/dnf-client/client-v210/client.zip"

    def test_public_project_exact_body_and_header_verified(self):
        data = b"package bytes"
        expected = hashlib.sha256(data).hexdigest()
        with tempfile.TemporaryDirectory() as directory, \
                patch.object(self.repo, "api", return_value={"id": 123, "visibility": "public"}) as api, \
                patch.object(sync.OPENER, "open", return_value=io.BytesIO(data)) as opened:
            path = self.repo.download_asset(self.url, directory, expected, fresh=True)
            self.assertEqual(Path(path).read_bytes(), data)
            request = opened.call_args.args[0]
            self.assertEqual(request.full_url, self.url)
            self.assertEqual(request.get_header("Private-token"), "test-project-token")
            api.assert_called_once_with("")

    def test_foreign_or_noncanonical_path_refused_before_token_or_network(self):
        urls = [self.url.replace("gitgud.io", "evil.test"),
                self.url.replace("/123/", "/124/"), self.url + "?x=1",
                self.url.replace("/dnf-client/", "/../"),
                self.url.replace("/dnf-client/", "/%2e%2e/"),
                self.url.replace("/client-v210/", "/x/../../"),
                self.url.replace("gitgud.io", "gitgud.io:443"),
                self.url.replace("/packages/generic/", "/repository/files/"),
                self.url.replace("client.zip", "client.exe")]
        with tempfile.TemporaryDirectory() as directory, patch.object(self.repo, "api") as api, \
                patch.object(sync.OPENER, "open") as opened:
            for url in urls:
                with self.subTest(url=url), self.assertRaises(sync.SyncError):
                    self.repo.download_asset(url, directory, fresh=True)
            api.assert_not_called()
            opened.assert_not_called()

    def test_private_or_wrong_project_cannot_be_mirrored(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(sync.OPENER, "open") as opened:
            for project in ({"id": 123, "visibility": "private"}, {"id": 123, "visibility": "internal"},
                            {"id": 124, "visibility": "public"}, None):
                with self.subTest(project=project), patch.object(self.repo, "api", return_value=project), \
                        self.assertRaises(sync.SyncError):
                    self.repo.download_asset(self.url, directory, fresh=True)
            opened.assert_not_called()

    def test_authenticated_redirect_never_reaches_second_request(self):
        request = urllib.request.Request(self.url, headers={"PRIVATE-TOKEN": "test-project-token"})
        for target in (self.url + "?redirected=1", "https://cdn.example.test/client.zip"):
            with self.subTest(target=target), self.assertRaises(sync.SyncError):
                sync.HTTPSRedirect().redirect_request(request, None, 302, "", {}, target)

    def test_authenticated_download_retains_digest_and_size_guards(self):
        with tempfile.TemporaryDirectory() as directory, \
                patch.object(self.repo, "api", return_value={"id": 123, "visibility": "public"}):
            with patch.object(sync.OPENER, "open", return_value=io.BytesIO(b"changed")), \
                    self.assertRaisesRegex(sync.SyncError, "digest conflicts"):
                self.repo.download_asset(self.url, directory, "a" * 64, fresh=True)
            with patch.object(sync.OPENER, "open", return_value=io.BytesIO(b"12345")), \
                    patch.object(sync, "MAX_ASSET", 4), self.assertRaisesRegex(sync.SyncError, "size limit"):
                self.repo.download_asset(self.url, directory, fresh=True)

    def test_github_assets_remain_anonymous(self):
        github = sync.Repository("github", "github", "owner/repo", "github-secret", True)
        with tempfile.TemporaryDirectory() as directory, patch.object(github, "api") as api, \
                patch.object(sync.OPENER, "open", return_value=io.BytesIO(b"public")) as opened:
            github.download_asset("https://github.com/owner/repo/releases/download/client-v210/client.zip", directory, fresh=True)
            request = opened.call_args.args[0]
            self.assertFalse(request.has_header("Private-token"))
            self.assertFalse(request.has_header("Authorization"))
            api.assert_not_called()


if __name__ == "__main__":
    unittest.main()
