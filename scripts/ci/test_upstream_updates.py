"""Real Git graphs with a fake GitHub PR API; no network or credentials."""
import copy
import os
import tempfile
import unittest
import urllib.error
from pathlib import Path
from unittest.mock import patch

import upstream_updates as updates
from sync_repositories import SyncError, git


class FakeGitHub:
    repository = "test/fork"

    def __init__(self, repo):
        self.repo, self.pr, self.dispatches = repo, None, []

    def refresh(self):
        base = git(self.repo, "rev-parse", "master").stdout.strip()
        head = git(self.repo, "rev-parse", updates.BRANCH).stdout.strip()
        merged = git(self.repo, "merge-tree", "--write-tree", base, head, check=False)
        if merged.returncode == 0:
            tree = merged.stdout.splitlines()[0]
            merge = git(self.repo, "commit-tree", tree, "-p", base, "-p", head,
                        "-m", "PR candidate").stdout.strip()
            git(self.repo, "update-ref", "refs/pull/1/merge", merge)
        self.pr.update(base={"sha": base, "ref": "master"},
                       head={"sha": head, "ref": updates.BRANCH,
                             "repo": {"full_name": self.repository}})

    def request(self, method, path, body=None):
        if method == "GET" and path.startswith("pulls?"):
            return [] if self.pr is None else [copy.deepcopy(self.pr)]
        if method == "POST" and path == "pulls":
            self.pr = {"number": 1, "state": "open", **body}
            self.refresh()
        elif method == "PATCH" and path == "pulls/1":
            self.pr.update(body)
            self.refresh()
        elif method == "POST" and path.endswith("/dispatches"):
            self.dispatches.append((path, body))
            return None
        elif method != "GET" or path != "pulls/1":
            raise AssertionError((method, path))
        return copy.deepcopy(self.pr)


class UpstreamUpdatesTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.env = patch.dict(os.environ, {
            "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.devnull,
            "GIT_AUTHOR_NAME": "Test", "GIT_COMMITTER_NAME": "Test",
            "GIT_AUTHOR_EMAIL": "test@example.invalid", "GIT_COMMITTER_EMAIL": "test@example.invalid"})
        self.env.start()
        self.addCleanup(self.env.stop)
        self.root = Path(self.temp.name)
        self.origin, self.upstream, self.work = [self.root / name for name in ("origin", "upstream", "work")]
        for repo in (self.origin, self.upstream):
            git(self.root, "init", "--bare", str(repo))
        git(self.root, "init", "-b", "master", str(self.work))
        self.base = self.commit("base.txt", "initial\n")
        for repo in (self.origin, self.upstream):
            git(self.work, "push", str(repo), "master")
        git(self.work, "remote", "add", "origin", str(self.origin))
        self.api = FakeGitHub(self.origin)

    def commit(self, name, content):
        target = self.work / name
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(content, encoding="utf-8")
        git(self.work, "add", name)
        git(self.work, "commit", "-m", "test change")
        return git(self.work, "rev-parse", "HEAD").stdout.strip()

    def head(self, repo=None):
        return git(repo or self.origin, "rev-parse", "master").stdout.strip()

    def incoming(self, name="new.txt", content="upstream\n"):
        head = self.commit(name, content)
        git(self.work, "push", str(self.upstream), "master")
        return head

    def prepare(self):
        return updates.prepare(self.work, str(self.upstream), self.api, attempts=1)

    def promote(self, result):
        return updates.promote(self.work, self.api, result["base"], result["head"], result["merge"], result["pr"])

    def test_already_current_does_not_open_pr(self):
        self.assertEqual(self.prepare()["state"], "current")
        self.assertIsNone(self.api.pr)
        self.assertEqual(self.head(), self.base)

    def test_exact_tested_merge_is_promoted_and_mirror_dispatched(self):
        incoming = self.incoming()
        git(self.work, "reset", "--hard", self.base)
        fork = self.commit("fork.txt", "fork feature")
        git(self.work, "push", "origin", "master")
        result = self.prepare()
        self.assertEqual(result["state"], "validate")
        self.assertEqual(self.head(), fork)
        self.assertEqual(git(self.work, "show", "-s", "--format=%P", result["merge"]).stdout.split(),
                         [fork, incoming])
        self.assertEqual(self.promote(result)["state"], "merged")
        self.assertEqual(self.head(), result["merge"])
        self.assertEqual(self.api.dispatches, [("actions/workflows/repository-sync.yml/dispatches", {"ref": "master"})])

    def test_conflict_keeps_pr_open_and_master_unchanged(self):
        self.incoming("base.txt", "upstream change\n")
        git(self.work, "reset", "--hard", self.base)
        fork = self.commit("base.txt", "fork change\n")
        git(self.work, "push", "origin", "master")
        self.assertEqual(self.prepare()["state"], "review")
        self.assertEqual(self.head(), fork)
        self.assertEqual(self.api.pr["state"], "open")
        self.assertIn("conflict", self.api.pr["body"])
        self.assertFalse(self.api.dispatches)

    def test_ci_changes_require_review(self):
        self.incoming(".github/workflows/unsafe.yml", "name: changed")
        result = self.prepare()
        self.assertEqual(result["state"], "review")
        self.assertEqual(self.head(), self.base)
        self.assertIn("manual review", self.api.pr["body"])
        merge = git(self.origin, "rev-parse", "refs/pull/1/merge").stdout.strip()
        with self.assertRaisesRegex(SyncError, "CI changes"):
            self.promote({**result, "merge": merge})

    def test_moving_base_rejects_promotion(self):
        self.incoming()
        result = self.prepare()
        git(self.work, "reset", "--hard", self.base)
        advanced = self.commit("another.txt", "concurrent work")
        git(self.work, "push", "origin", "master")
        with self.assertRaisesRegex(SyncError, "Master advanced"):
            self.promote(result)
        self.assertEqual(self.head(), advanced)
        self.assertFalse(self.api.dispatches)

    def test_changed_head_or_closed_pr_rejects_promotion(self):
        self.incoming()
        result = self.prepare()
        original = copy.deepcopy(self.api.pr)
        for change in ({"head": {**original["head"], "sha": "f" * 40}}, {"state": "closed"}):
            self.api.pr = {**original, **change}
            with self.assertRaisesRegex(SyncError, "PR/base changed"):
                self.promote(result)
        self.assertEqual(self.head(), self.base)
        self.assertFalse(self.api.dispatches)

    def test_changed_merge_ref_rejects_promotion(self):
        self.incoming()
        result = self.prepare()
        git(self.origin, "update-ref", "refs/pull/1/merge", self.base)
        with self.assertRaisesRegex(SyncError, "Merge candidate changed"):
            self.promote(result)
        self.assertEqual(self.head(), self.base)

    def test_concurrent_push_cannot_overwrite_master(self):
        self.incoming()
        result = self.prepare()
        git(self.work, "reset", "--hard", self.base)
        advanced = self.commit("racing.txt", "concurrent change")
        real_git = updates.git
        def racing_git(directory, *args, **kwargs):
            if args[:2] == ("push", "origin"):
                real_git(self.origin, "update-ref", "refs/heads/master", advanced)
            return real_git(directory, *args, **kwargs)
        # Transfer the racing object without advancing master yet.
        git(self.work, "push", "origin", f"{advanced}:refs/heads/racing")
        with patch.object(updates, "git", side_effect=racing_git):
            with self.assertRaisesRegex(SyncError, "git push failed"):
                self.promote(result)
        self.assertEqual(self.head(), advanced)
        self.assertFalse(self.api.dispatches)

    def test_divergent_automation_branch_is_never_forced(self):
        self.incoming()
        self.prepare()
        previous = git(self.origin, "rev-parse", updates.BRANCH).stdout.strip()
        git(self.work, "reset", "--hard", self.base)
        rewritten = self.commit("rewritten.txt", "rewritten upstream")
        git(self.work, "push", "--force", str(self.upstream), "master")
        self.assertNotEqual(previous, rewritten)
        with self.assertRaisesRegex(SyncError, "diverged"):
            self.prepare()
        self.assertEqual(git(self.origin, "rev-parse", updates.BRANCH).stdout.strip(), previous)
        self.assertEqual(self.head(), self.base)

    def test_api_errors_do_not_disclose_provider_data(self):
        api = updates.GitHub("test/fork", "SYNTHETIC_SECRET")
        for error in (urllib.error.HTTPError("secret-url", 403, "secret-message", {}, None),
                      urllib.error.URLError("secret-connection")):
            with patch("urllib.request.urlopen", side_effect=error):
                with self.assertRaises(SyncError) as raised:
                    api.request("GET", "pulls/1")
            self.assertNotIn("secret", str(raised.exception).lower())


if __name__ == "__main__":
    unittest.main()
