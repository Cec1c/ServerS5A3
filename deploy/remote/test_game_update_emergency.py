import copy
import importlib.util
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('recovery_worker', Path(__file__).with_name('game_update.py'))
worker = importlib.util.module_from_spec(spec)
spec.loader.exec_module(worker)


class EmergencyTests(unittest.TestCase):
    def test_split_backup_keeps_actual_configs_and_edge_snapshot_without_local_hub(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            config = root / 'runtime/docker/instance.json'
            config.parent.mkdir(parents=True)
            config.write_bytes(b'private-config')
            edge = root / 'edge.json'
            edge.write_bytes(b'verified-edge')
            backup = root / 'backup'
            worker.backup_game_context(root, root / 'runtime/launcher-hub', backup,
                                       ['runtime/docker/instance.json'], edge)
            self.assertEqual((backup / 'runtime/docker/instance.json').read_bytes(), b'private-config')
            self.assertEqual((backup / 'edge-snapshot.json').read_bytes(), b'verified-edge')
            self.assertFalse((root / 'runtime/launcher-hub').exists())

    def test_single_host_requires_and_preserves_real_hub_state(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            hub = root / 'runtime/launcher-hub'
            with self.assertRaises(FileNotFoundError):
                worker.backup_game_context(root, hub, root / 'backup', [])
            hub.mkdir(parents=True)
            (hub / 'state.json').write_bytes(b'actual-state')
            worker.backup_game_context(root, hub, root / 'backup', [])
            self.assertEqual((root / 'backup/runtime/launcher-hub/state.json').read_bytes(), b'actual-state')

    def example(self):
        names = ['Docs/CHARACTER_SELECTION_TIMEOUT.md', 'changlog.md'] + [
            'go-server/internal/services/dnfbridge/' + p for p in
            ('game_select_scene_entry.go', 'game_select_state.go', 'character_rename_current.go', 'character_select_deadline_test.go')]
        old = {'active': {'sourceArchiveSha256': 'a' * 64},
               'sourceManifest': {'upstream': 'b' * 40, 'files': {p: 'c' * 64 for p in names}}}
        old['sourceManifest']['files']['README.md'] = 'd' * 64
        changes = [dict(path=p, beforeSha256='c' * 64, afterSha256='e' * 64) for p in names]
        src = dict(integrationMode='emergency-selected-mr', upstream='b' * 40,
                   emergency=dict(baseArchiveSha256='a' * 64, changes=changes),
                   changedFiles=names, files={p: 'e' * 64 for p in names})
        src['files']['README.md'] = 'd' * 64
        return src, old

    def test_exact_six_file_scope_and_unchanged_remainder(self):
        src, old = self.example()
        worker.checked_emergency_scope(src, old)
        for mutate in (
            lambda s: s['files'].update({'README.md': 'f' * 64}),
            lambda s: s['files'].update({'extra.go': 'f' * 64}),
            lambda s: s.update(upstream='f' * 40),
            lambda s: s.update(mainQuestExpPercent=25),
            lambda s: s['emergency'].update(baseArchiveSha256='f' * 64),
            lambda s: s['emergency']['changes'][0].update(beforeSha256='f' * 64),
            lambda s: s['emergency']['changes'][0].update(path='Docs/unrelated.md'),
        ):
            candidate = copy.deepcopy(src)
            mutate(candidate)
            with self.assertRaises(ValueError):
                worker.checked_emergency_scope(candidate, old)


if __name__ == '__main__':
    unittest.main()
