import copy
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import localhost_publish_persistence as persistence


@unittest.skipUnless(os.name == 'posix' and getattr(os, 'geteuid', lambda: -1)() == 0,
                     'requires an isolated Linux root filesystem')
class PersistenceTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix='guard-install-')
        self.root = Path(self.temp.name)
        self.helper = self.root / 'usr/local/libexec/guard.py'
        self.hook = self.root / 'etc/systemd/system/docker.service.d/guard.conf'
        self.patch = patch.multiple(persistence, HELPER=self.helper, HOOK=self.hook)
        self.patch.start();self.addCleanup(self.patch.stop);self.addCleanup(self.temp.cleanup)
        self.source = b'print("synthetic pinned helper")\n'
        self.baseline = persistence.capture(self.source)
        self.calls = []

    def manager(self, argv, **options):
        self.assertEqual(argv, ['systemctl', 'daemon-reload'])
        self.assertEqual(options['timeout'], 20)
        self.calls.append(argv)
        return subprocess.CompletedProcess(argv, 0, '', '')

    def install(self):
        return persistence.install(self.baseline, self.source, self.manager)

    def restore(self):
        return persistence.restore(self.baseline, self.source, self.manager)

    def test_install_repeat_and_restore_preserve_original_absence(self):
        self.install();first = persistence.capture(self.source)
        self.install();self.assertEqual(persistence.capture(self.source), first)
        self.assertEqual(self.helper.read_bytes(), self.source)
        self.assertEqual(self.hook.read_bytes(), persistence.HOOK_BYTES)
        self.restore();self.restore()
        self.assertEqual(persistence.capture(self.source), self.baseline)

    def test_preexisting_matching_files_and_directory_attributes_are_preserved(self):
        self.install();self.baseline = persistence.capture(self.source)
        self.hook.parent.chmod(0o750)
        before = self.hook.parent.stat()
        self.install();self.restore()
        self.assertEqual(persistence.capture(self.source), self.baseline)
        self.assertEqual(self.hook.parent.stat().st_mode, before.st_mode)

    def test_foreign_file_is_not_overwritten_or_deleted(self):
        self.install();self.helper.write_bytes(b'foreign modification')
        for action in (self.install, self.restore):
            with self.assertRaises(ValueError):action()
        self.assertEqual(self.helper.read_bytes(), b'foreign modification')
        self.assertTrue(self.hook.exists())

    def test_partial_install_recovers_with_same_sealed_baseline(self):
        persistence.create_file(self.helper, self.source, 0o755)
        self.install();self.restore()
        self.assertEqual(persistence.capture(self.source), self.baseline)

    def test_manager_reload_failure_keeps_helper_until_retry(self):
        self.install()
        def failed(argv, **options):
            return subprocess.CompletedProcess(argv, 1, '', 'injected reload failure')
        with self.assertRaises(RuntimeError):persistence.restore(self.baseline, self.source, failed)
        self.assertFalse(self.hook.exists());self.assertTrue(self.helper.exists())
        self.restore();self.assertEqual(persistence.capture(self.source), self.baseline)

    def test_unrelated_created_directory_contents_are_preserved(self):
        self.install();other = self.hook.parent / 'foreign.conf';other.write_text('foreign')
        self.restore();self.assertEqual(other.read_text(), 'foreign')
        self.assertFalse(self.hook.exists());self.assertFalse(self.helper.exists())

    def test_symlink_and_writable_directory_are_rejected(self):
        self.helper.parent.mkdir(parents=True)
        target=self.root/'foreign';target.write_text('keep')
        self.helper.symlink_to(target)
        with self.assertRaises(ValueError):self.install()
        self.assertEqual(target.read_text(), 'keep')
        self.helper.unlink();self.helper.parent.chmod(0o777)
        with self.assertRaises(ValueError):self.install()
        self.assertFalse(self.hook.exists())

    def test_baseline_tampering_is_rejected_before_changes(self):
        for key, value in (('helperSha256', '0'*64), ('directories', {}), ('schema', 2)):
            baseline=copy.deepcopy(self.baseline);baseline[key]=value
            with self.assertRaises(ValueError):persistence.install(baseline, self.source, self.manager)
        self.assertFalse(self.helper.exists());self.assertFalse(self.hook.exists())

    def test_missing_preexisting_helper_is_not_recreated(self):
        self.install();self.baseline=persistence.capture(self.source);self.helper.unlink()
        with self.assertRaises(ValueError):self.install()
        self.assertFalse(self.helper.exists());self.assertTrue(self.hook.exists())


if __name__ == '__main__':
    unittest.main()
