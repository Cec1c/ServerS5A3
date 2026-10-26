"""Explicit archival review does not authorize installing deployment tools."""
import copy
import hashlib
from pathlib import Path
import subprocess
import tempfile
import unittest
import game_update as update


class AncillarySourceReview(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.source = Path(self.temp.name)
        self.git('init', '-q')
        self.git('config', 'user.name', 'Test')
        self.git('config', 'user.email', 'test@example.invalid')
        self.git('config', 'commit.gpgsign', 'false')
        self.git('config', 'core.autocrlf', 'false')
        (self.source / 'deploy').mkdir()
        self.path = 'deploy/manage.py'
        self.write(b'base\n')
        self.base = self.commit()
        self.write(b'upstream\n')
        self.upstream = self.commit()
        self.write(b'merged private and upstream\n')
        self.head = self.commit()
        self.review = dict(format=1, purpose='source-archive-only', baseCommit=self.base,
                           upstreamCommit=self.upstream, candidateCommit=self.head,
                           files={self.path: dict(baseSha256=self.hash(b'base\n'),
                              upstreamSha256=self.hash(b'upstream\n'),
                              candidateSha256=self.hash(b'merged private and upstream\n'))})

    def git(self, *args):
        return update.git(self.source, *args)

    def write(self, data):
        (self.source / self.path).write_bytes(data)

    def hash(self, data):
        return hashlib.sha256(data).hexdigest()

    def commit(self):
        self.git('add', '.')
        self.git('commit', '-qm', 'fixture')
        return self.git('rev-parse', 'HEAD')

    def check(self, review):
        return update.checked_changes(self.source, self.base, [self.path], self.upstream, review)

    def test_exact_archival_review_and_default_rejection(self):
        with self.assertRaises(ValueError):
            self.check(None)
        self.assertEqual(self.check(self.review), {self.path})

    def test_reject_stale_or_partial_or_expanded_review(self):
        for key in ('purpose', 'baseCommit', 'upstreamCommit', 'candidateCommit'):
            with self.subTest(key=key):
                review = copy.deepcopy(self.review)
                review[key] = 'wrong'
                with self.assertRaises(ValueError): self.check(review)
        for key in ('baseSha256', 'upstreamSha256', 'candidateSha256'):
            with self.subTest(key=key):
                review = copy.deepcopy(self.review)
                review['files'][self.path][key] = '0' * 64
                with self.assertRaises(ValueError): self.check(review)
        for files in ({}, self.review['files'] | {'scripts/unreviewed.py': {}}):
            with self.assertRaises(ValueError): self.check(self.review | {'files': files})

    def test_dirty_tree_and_unreviewed_scope_still_rejected(self):
        with self.assertRaises(ValueError):
            update.checked_changes(self.source, self.base, [], self.upstream, self.review)
        self.write(b'changed after review\n')
        with self.assertRaises(ValueError): self.check(self.review)

    def test_runtime_assets_and_bat_cannot_use_archive_review(self):
        for path in ('deploy/config.json', 'scripts/update.bat'):
            with self.subTest(path=path):
                target = self.source / path
                target.parent.mkdir(exist_ok=True)
                target.write_bytes(b'not Python source\n')
                self.commit()
                with self.assertRaises(ValueError):
                    update.checked_changes(self.source, self.base,
                        self.git('diff', '--name-only', self.base).splitlines(), self.upstream, self.review)

    def test_upstream_only_keeps_existing_path_without_review(self):
        self.git('checkout', '--detach', self.upstream)
        self.assertEqual(self.check(None), {self.path})
        with self.assertRaises(ValueError): self.check(self.review)


if __name__ == '__main__':
    unittest.main()
