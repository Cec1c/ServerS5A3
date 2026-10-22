import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import sync_client_releases as sync


class TagSSHTests(unittest.TestCase):
    def test_safe_triggers(self):
        for triggers in (
            '  workflow_dispatch:\n',
            '  release:\n    types: [published]\n',
            '  schedule:\n    - cron: "7 * * * *"\n',
            '  push:\n    branches: [master]\n',
        ):
            with self.subTest(triggers=triggers):
                sync.require_no_tag_workflow('name: Example\non:\n' + triggers)
        sync.require_no_tag_workflow('on: [workflow_dispatch, release]\n')

    def test_tag_and_ambiguous_triggers_refused(self):
        for document in (
            'on: push', 'on: create', 'on: [push, workflow_dispatch]',
            'on:\n  push:\n', 'on:\n  push:\n    tags: ["client-*"]',
            'on:\n  push:\n    branches: [master]\n    tags: ["*"]',
            'on:\n  push:\n    paths: [docs/**]',
            'on:\n  workflow_run:\n    workflows: [Verify]',
            'on: workflow_dispatch\non: push',
            'on:\n  push:\n    branches: [master]\n    branches: []',
            'on:\n  <<: {create: {}}', 'on: [', 'name: no trigger',
        ):
            with self.subTest(document=document):
                with self.assertRaises(sync.SyncError):
                    sync.require_no_tag_workflow(document)

    def exercise_transfer(self, workflow, *, trusted=True, annotated=False, bad_mode=False):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            source, work = root / 'source', root / 'work'
            source.mkdir(); work.mkdir()
            def run(*args):
                return subprocess.check_output(['git', '-C', str(source), *args], text=True).strip()
            run('init', '--quiet')
            run('config', 'user.email', 'test@example.invalid')
            run('config', 'user.name', 'test')
            path = source / '.github/workflows/check.yml'
            path.parent.mkdir(parents=True)
            path.write_text(workflow, encoding='utf-8')
            run('add', '.')
            if bad_mode:
                blob = run('hash-object', '-w', str(path))
                run('update-index', '--cacheinfo', '120000,' + blob + ',.github/workflows/check.yml')
            run('commit', '--quiet', '-m', 'fixture')
            commit = run('rev-parse', 'HEAD')
            tag = 'client-v137-test'
            run('tag', *(['-a', '-m', 'annotated'] if annotated else []), tag)
            origin = sync.Repository('source', 'gitlab', 1)
            origin.git = str(source)
            target = sync.Repository('github', 'github', 'owner/repo', 'token', True)
            target.github_tag_ssh = True
            item = {'tag': tag, 'commit': commit, 'repo': origin}
            env = {'GITHUB_ACTIONS': 'true', 'GITHUB_TOKEN': 'token',
                   'GITHUB_REPOSITORY': 'owner/repo' if trusted else 'other/repo',
                   'GIT_SSH_COMMAND': 'ssh -o BatchMode=yes'}
            original = subprocess.run
            pushes = []
            def intercept(argv, **kwargs):
                if 'push' in argv:
                    pushes.append(argv)
                    return subprocess.CompletedProcess(argv, 0, stdout=b'', stderr=b'')
                return original(argv, **kwargs)
            with patch.dict(os.environ, env), patch.object(target, 'tag_commit', side_effect=[None, commit]), \
                 patch.object(target, 'api') as api, patch.object(sync.subprocess, 'run', side_effect=intercept):
                try:
                    target.ensure_tag(item, work)
                except sync.SyncError:
                    self.assertEqual(pushes, [])
                    raise
                api.assert_not_called()
            self.assertEqual(len(pushes), 1)
            self.assertEqual(pushes[0][-2:], ['git@github.com:owner/repo.git', 'refs/sync/source:refs/tags/' + tag])
            self.assertNotIn('--force', pushes[0])

    def test_audited_exact_tag_push_and_readback(self):
        self.exercise_transfer('on:\n  push:\n    branches: [master]\n  workflow_dispatch:\n')

    def test_annotated_tag_preserved(self):
        self.exercise_transfer('on: workflow_dispatch\n', annotated=True)

    def test_old_build_workflow_prevents_push(self):
        with self.assertRaises(sync.SyncError):
            self.exercise_transfer('on: push\n')

    def test_foreign_ci_repository_prevents_push(self):
        with self.assertRaises(sync.SyncError):
            self.exercise_transfer('on: workflow_dispatch\n', trusted=False)

    def test_symlink_workflow_prevents_push(self):
        with self.assertRaises(sync.SyncError):
            self.exercise_transfer('on: workflow_dispatch\n', bad_mode=True)


if __name__ == '__main__':
    unittest.main()
