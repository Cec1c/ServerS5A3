from pathlib import Path
import importlib.util
import tempfile
import unittest

import game_update as worker


def load_generator():
    path = Path(__file__).resolve().parents[1] / 'docker' / 'manage.py'
    spec = importlib.util.spec_from_file_location('candidate_manage', path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class InstalledCandidateOptionsTests(unittest.TestCase):
    def setUp(self):
        self.manage = load_generator()

    def test_preserves_bounded_installed_settings_without_copying_other_keys(self):
        raw = (b'DNFBRIDGE_MAP_CACHE=true\nDNFBRIDGE_PVF_READ_CACHE_MIB=256\n'
               b'DNFBRIDGE_GUILD_PRESENCE_COALESCE_MS=120\n'
               b'DNFBRIDGE_GUILD_PRESENCE_PERSIST_MIN_INTERVAL_SEC=30\n'
               b'GOMEMLIMIT=8GiB\nUNRELATED=value\n')
        instance = {'runtime_compatibility': {'DNFBRIDGE_MYSQL_INSERT_BATCH': '200'}}
        out = worker.installed_candidate_options(instance, raw, self.manage, 'f' * 64)
        self.assertEqual(out['go_memory_limit'], '8GiB')
        self.assertEqual(out['runtime_compatibility'], {
            'DNFBRIDGE_MYSQL_INSERT_BATCH': '200', 'DNFBRIDGE_MAP_CACHE': 'true',
            'DNFBRIDGE_PVF_READ_CACHE_MIB': '256', 'DNFBRIDGE_GUILD_PRESENCE_COALESCE_MS': '120',
            'DNFBRIDGE_GUILD_PRESENCE_PERSIST_MIN_INTERVAL_SEC': '30'})
        self.assertEqual(instance, {'runtime_compatibility': {'DNFBRIDGE_MYSQL_INSERT_BATCH': '200'}})
        self.assertNotIn('client_exe_sha256', out)

    def test_rejects_invalid_or_ambiguous_values(self):
        for raw in (b'GOMEMLIMIT=1TiB\n', b'GOMEMLIMIT=1MiB\n', b'GOMEMLIMIT=1GiB',
                    b'x=1\nx=2\n', b'DNFBRIDGE_GUILD_PRESENCE_COALESCE_MS=5001\n',
                    b'DNFBRIDGE_PVF_READ_CACHE_MIB=-1\n'):
            with self.subTest(raw=raw), self.assertRaises(ValueError):
                worker.preserve_installed_optimization({}, raw, self.manage)

    def test_generated_file_matches_all_values_and_restores_order(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'candidate.env'
            path.write_bytes(b'B=2\nA=1\n')
            worker.checked_generated_environment(path, b'# preserved\nA=1\nB=2\n')
            self.assertEqual(path.read_bytes(), b'# preserved\nA=1\nB=2\n')
            with self.assertRaises(AssertionError):
                worker.checked_generated_environment(path, b'A=1\nB=3\n')
            with self.assertRaises(ValueError):
                worker.checked_generated_environment(path, b'A=1\nA=1\n')

    def test_shop_uses_active_identity_and_rolls_back_to_old_client(self):
        key = self.manage.BLACK_DIAMOND_SECRET_SHOP_KEY
        config = {'black_diamond_secret_shop': 'auto', 'client_dir': 'unused-local-copy'}
        for sha in self.manage.BLACK_DIAMOND_SECRET_SHOP_CLIENTS:
            prepared = worker.installed_candidate_options(config, b'', self.manage, sha)
            self.assertEqual(self.manage.black_diamond_secret_shop_env(prepared, {}), 'on')
            restored = worker.installed_candidate_options(prepared, b'', self.manage, '1' * 64)
            self.assertEqual(self.manage.black_diamond_secret_shop_env(restored, {key: 'on'}), 'off')
        self.assertIsNone(self.manage.black_diamond_secret_shop_env({}, {}))
        with self.assertRaises(ValueError):
            self.manage.black_diamond_secret_shop_env({'client_exe_sha256': 'bad'}, {})
        with self.assertRaises(ValueError):
            self.manage.black_diamond_secret_shop_env({'black_diamond_secret_shop': 'off'}, {key: 'on'})
