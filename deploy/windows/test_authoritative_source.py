"""Exercise real pulls against temporary Git repositories; no network or production writes."""
import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('update_gate_tests', Path(__file__).with_name('game_update.py'))
update = importlib.util.module_from_spec(spec)
spec.loader.exec_module(update)


class AuthorityTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.authority = self.root / 'authority'
        self.authority.mkdir()
        self.git(self.authority, 'init', '-q', '-b', 'master')
        self.git(self.authority, 'config', 'user.name', 'Test')
        self.git(self.authority, 'config', 'user.email', 'test@example.invalid')
        self.git(self.authority, 'config', 'commit.gpgsign', 'false')
        self.advance('one')
        self.candidate = self.root / 'candidate'
        self.git(self.root, 'clone', '-q', str(self.authority), str(self.candidate))
        self.audit = self.root / 'audit'
        self.audit.mkdir()

    def git(self, root, *args):
        return subprocess.check_output(['git', *args], cwd=root, text=True, stderr=subprocess.PIPE).strip()

    def advance(self, value):
        (self.authority / 'source.txt').write_text(value)
        self.git(self.authority, 'add', '.')
        self.git(self.authority, 'commit', '-qm', value)
        return self.git(self.authority, 'rev-parse', 'HEAD')

    def gate(self):
        # Only tests replace the fixed production authority; no CLI/profile bypass exists.
        return update.AuthorityGate(self.root / 'checkout', self.audit, 'test-run')

    def check(self, gate, phase='start'):
        with patch.object(update, 'AUTHORITATIVE_URL', str(self.authority)):
            return gate.check(self.candidate, phase)

    def test_actual_pull_records_before_after_and_candidate(self):
        gate = self.gate()
        receipt = self.check(gate)
        self.assertEqual(receipt['after'], self.git(self.authority, 'rev-parse', 'HEAD'))
        self.assertEqual(receipt['candidate'], self.git(self.candidate, 'rev-parse', 'HEAD'))
        self.assertTrue(receipt['containsMaster'])
        self.assertIn('--ff-only', receipt['command'])
        self.assertEqual(self.check(gate, 'package')['before'], receipt['after'])

    def test_stale_candidate_is_rejected(self):
        self.advance('two')
        with self.assertRaisesRegex(ValueError, 'does not contain'):
            self.check(self.gate())

    def test_remote_moves_during_validation_even_if_candidate_updates(self):
        gate = self.gate()
        self.check(gate)
        self.advance('two')
        self.git(self.candidate, 'pull', '--ff-only')
        with self.assertRaisesRegex(ValueError, 'changed during'):
            self.check(gate, 'package')

    def test_old_receipt_does_not_replace_live_pull(self):
        first = self.check(self.gate())
        gate = self.gate()
        self.assertNotEqual(gate.nonce, first['nonce'])
        with patch.object(update, 'AUTHORITATIVE_URL', str(self.root / 'missing')):
            with self.assertRaises(subprocess.CalledProcessError):
                gate.check(self.candidate, 'start')

    def test_dirty_or_diverged_pull_checkout_is_rejected_without_reset(self):
        self.check(self.gate())
        checkout = self.root / 'checkout'
        (checkout / 'source.txt').write_text('dirty')
        with self.assertRaisesRegex(ValueError, 'clean'):
            self.check(self.gate())
        self.assertEqual((checkout / 'source.txt').read_text(), 'dirty')
        self.git(checkout, 'config', 'user.name', 'Test')
        self.git(checkout, 'config', 'user.email', 'test@example.invalid')
        self.git(checkout, 'config', 'commit.gpgsign', 'false')
        self.git(checkout, 'add', '.')
        self.git(checkout, 'commit', '-qm', 'divergent')
        self.advance('two')
        with self.assertRaises(subprocess.CalledProcessError):
            self.check(self.gate())

    def test_candidate_cannot_be_authority_checkout(self):
        gate = update.AuthorityGate(self.candidate, self.audit, 'test-run')
        with self.assertRaisesRegex(ValueError, 'separate'):
            self.check(gate)


if __name__ == '__main__':
    unittest.main()
