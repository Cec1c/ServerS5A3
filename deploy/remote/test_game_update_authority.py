import datetime
import hashlib
import importlib.util
import json
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('authority_worker', Path(__file__).with_name('game_update.py'))
worker = importlib.util.module_from_spec(spec)
spec.loader.exec_module(worker)


class AuthorityProofTests(unittest.TestCase):
    def setUp(self):
        self.now = datetime.datetime.now(datetime.timezone.utc)
        self.manifest = {'upstream': 'a' * 40, 'sourceCommit': 'b' * 40, 'authorityInvocation': 'c' * 64}
        self.proof = {'format': 1, 'runId': 'run', 'nonce': 'c' * 64, 'phase': 'deploy',
                      'repository': 'https://gitgud.io/16ww/servers5a3.git', 'branch': 'master',
                      'passed': True, 'containsMaster': True, 'after': 'a' * 40,
                      'candidate': 'b' * 40, 'checkedAt': self.now.isoformat()}

    def check(self, proof=None):
        raw = json.dumps(proof or self.proof).encode()
        return worker.checked_authority_proof(raw, hashlib.sha256(raw).hexdigest(), 'c' * 64,
                                             'run', 'deploy', self.manifest, self.now)

    def test_live_matching_proof(self):
        self.assertEqual(self.check(), self.proof)

    def test_stale_future_wrong_run_phase_source_or_repository_rejected(self):
        for key, value in [('runId', 'old-run'), ('nonce', 'd' * 64), ('phase', 'prepare'),
                           ('candidate', 'e' * 40), ('after', 'f' * 40), ('passed', False),
                           ('repository', 'https://example.invalid/fork.git'), ('branch', 'dev'),
                           ('checkedAt', (self.now - datetime.timedelta(seconds=301)).isoformat()),
                           ('checkedAt', (self.now + datetime.timedelta(seconds=1)).isoformat())]:
            with self.subTest(key=key, value=value), self.assertRaises(ValueError):
                self.check(dict(self.proof, **{key: value}))

    def test_corrupt_transport_is_rejected(self):
        with self.assertRaisesRegex(ValueError, 'hash mismatch'):
            worker.checked_authority_proof(b'{}', '0' * 64, 'c' * 64, 'run', 'deploy', self.manifest, self.now)


if __name__ == '__main__': unittest.main()
