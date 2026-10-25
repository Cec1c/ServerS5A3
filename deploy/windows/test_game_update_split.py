"""Split-host snapshots retain pinned transport and reject stale/mixed evidence."""
import datetime
import hashlib
import importlib.util
import json
from pathlib import Path
import unittest

def module(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    result = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(result)
    return result

worker = module('split_worker', Path(__file__).parents[1] / 'remote/game_update.py')
controller = module('split_controller', Path(__file__).with_name('game_update.py'))

class SplitEvidenceTests(unittest.TestCase):
    def setUp(self):
        self.now = datetime.datetime.now(datetime.timezone.utc)
        self.value = {'format': 1, 'checkedAt': self.now.isoformat(),
                      'published': {'profile': {'PvfSha256': 'a' * 64}},
                      'management': {'mode': 'external-gm', 'pvfSha256': 'a' * 64},
                      'configHashes': {'/srv/example/gateway.json': 'b' * 64}}

    def check(self):
        raw = json.dumps(self.value).encode()
        return worker.checked_edge_evidence(raw, hashlib.sha256(raw).hexdigest(), self.now)

    def test_live_evidence_preserves_remote_config_hashes(self):
        self.assertEqual(self.check(), self.value)

    def test_modified_transfer_rejected(self):
        raw = json.dumps(self.value).encode()
        with self.assertRaises(ValueError): worker.checked_edge_evidence(raw, '0' * 64, self.now)

    def test_expired_evidence_rejected(self):
        self.value['checkedAt'] = (self.now - datetime.timedelta(seconds=301)).isoformat()
        with self.assertRaises(ValueError): self.check()

    def test_future_evidence_rejected(self):
        self.value['checkedAt'] = (self.now + datetime.timedelta(seconds=1)).isoformat()
        with self.assertRaises(ValueError): self.check()

    def test_different_player_and_management_pvf_rejected(self):
        self.value['management']['pvfSha256'] = 'b' * 64
        with self.assertRaises(ValueError): self.check()

    def test_missing_remote_configuration_rejected(self):
        self.value['configHashes'] = {}
        with self.assertRaises(ValueError): self.check()

    def test_wrong_topology_rejected(self):
        self.value['management']['mode'] = 'container'
        with self.assertRaises(ValueError): self.check()

    def test_edge_target_injection_and_parent_paths_rejected(self):
        profile = {'ssh_alias': 'game', 'remote_root': '/srv/game', 'compose_project': 'game',
                   'runtime_base_image': 'sha256:' + 'a' * 64, 'probe_command': ['probe'],
                   'edge_ssh_alias': 'gateway', 'edge_remote_root': '/srv/gateway'}
        controller.check_profile(profile)
        for field, value in [('edge_ssh_alias', 'host;cmd'), ('edge_remote_root', '/srv/../root'), ('edge_remote_root', '/')]:
            with self.subTest(field=field, value=value), self.assertRaises(ValueError):
                controller.check_profile(dict(profile, **{field: value}))

if __name__ == '__main__': unittest.main()
