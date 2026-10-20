import os
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from sync_repositories import SyncError, git, main, synchronize


class RepositorySyncErrorTests(unittest.TestCase):
    def test_failed_git_omits_provider_output_and_arguments(self):
        secret = "SYNTHETIC_SYNC_SECRET"
        url = f"https://test-user:{secret}@example.invalid/repo.git"
        failure = subprocess.CompletedProcess([], 128, stdout=secret, stderr=f"fatal: {url}")
        with patch("sync_repositories.subprocess.run", return_value=failure):
            with self.assertRaises(SyncError) as raised:
                git(Path("."), "fetch", url)
        self.assertEqual(str(raised.exception), "git fetch failed (exit 128)")
        self.assertNotIn(secret, str(raised.exception))

    def test_cli_failure_does_not_print_provider_output(self):
        secret = "SYNTHETIC_SYNC_SECRET"
        failure = subprocess.CompletedProcess([], 128, stdout="", stderr=secret)
        with patch("sys.argv", ["sync", "--github", "example-gh", "--gitgud", "example-gg"]), \
             patch.dict(os.environ, {"GH_SYNC_TOKEN": ""}), \
             patch("sync_repositories.subprocess.run", return_value=failure), \
             patch("builtins.print") as output:
            with self.assertRaises(SystemExit) as raised:
                main()
        self.assertEqual(raised.exception.code, 1)
        output.assert_called_once_with("Synchronization stopped: git init failed (exit 128)")


class RepositorySyncTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.gh, self.gg, self.work = [self.root / name for name in ("github.git", "gitgud.git", "work")]
        self.run_git(self.root, "init", "--bare", str(self.gh))
        self.run_git(self.root, "init", "--bare", str(self.gg))
        self.run_git(self.root, "init", "-b", "master", str(self.work))
        self.run_git(self.work, "config", "user.name", "Sync test")
        self.run_git(self.work, "config", "user.email", "sync-test@example.invalid")
        self.commit("initial.txt", "base")
        for remote in (self.gh, self.gg):
            self.run_git(self.work, "push", str(remote), "master")
        self.base = self.head(self.gh)

    def run_git(self, cwd, *arguments):
        env = {**os.environ, "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.devnull}
        result = subprocess.run(["git", "-C", str(cwd), *arguments], capture_output=True, text=True, env=env)
        self.assertEqual(result.returncode, 0, result.stderr)
        return result.stdout.strip()

    def commit(self, path, content):
        target = self.work / path
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(content, encoding="utf-8")
        self.run_git(self.work, "add", path)
        self.run_git(self.work, "commit", "-m", "test change")

    def head(self, repository):
        return self.run_git(repository, "rev-parse", "refs/heads/master")

    def test_equal_is_noop(self):
        self.assertEqual(synchronize(str(self.gh), str(self.gg), apply=True)["direction"], "equal")
        self.assertEqual(self.head(self.gh), self.base)

    def test_github_ahead_and_dry_run(self):
        self.commit("source.txt", "github")
        self.run_git(self.work, "push", str(self.gh), "master")
        result = synchronize(str(self.gh), str(self.gg))
        self.assertEqual(result["direction"], "github-to-gitgud")
        self.assertEqual(self.head(self.gg), self.base)
        synchronize(str(self.gh), str(self.gg), apply=True)
        self.assertEqual(self.head(self.gh), self.head(self.gg))

    def test_gitgud_ahead(self):
        self.commit("source.txt", "gitgud")
        self.run_git(self.work, "push", str(self.gg), "master")
        result = synchronize(str(self.gh), str(self.gg), apply=True)
        self.assertEqual(result["direction"], "gitgud-to-github")
        self.assertEqual(self.head(self.gh), self.head(self.gg))

    def test_divergence_never_writes(self):
        self.commit("github.txt", "github")
        self.run_git(self.work, "push", str(self.gh), "master")
        self.run_git(self.work, "reset", "--hard", self.base)
        self.commit("gitgud.txt", "gitgud")
        self.run_git(self.work, "push", str(self.gg), "master")
        before = (self.head(self.gh), self.head(self.gg))
        with self.assertRaisesRegex(SyncError, "diverged"):
            synchronize(str(self.gh), str(self.gg), apply=True)
        self.assertEqual((self.head(self.gh), self.head(self.gg)), before)

    def test_imported_workflow_is_blocked(self):
        self.commit(".github/workflows/unreviewed.yml", "name: unreviewed")
        self.run_git(self.work, "push", str(self.gg), "master")
        with self.assertRaisesRegex(SyncError, "CI definitions"):
            synchronize(str(self.gh), str(self.gg), apply=True)
        self.assertEqual(self.head(self.gh), self.base)

    def test_imported_sync_code_is_blocked(self):
        self.commit("scripts/ci/sync_repositories.py", "unreviewed")
        self.run_git(self.work, "push", str(self.gg), "master")
        with self.assertRaisesRegex(SyncError, "CI definitions"):
            synchronize(str(self.gh), str(self.gg), apply=True)
        self.assertEqual(self.head(self.gh), self.base)

    def test_missing_branch_does_not_create_it(self):
        with self.assertRaises(SyncError):
            synchronize(str(self.gh), str(self.gg), branch="missing", apply=True)
        self.assertEqual(self.head(self.gh), self.base)
        self.assertEqual(self.head(self.gg), self.base)


if __name__ == "__main__":
    unittest.main()
