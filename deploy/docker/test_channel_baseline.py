from argparse import Namespace
import copy
import json
from pathlib import Path
import re
import tempfile
import unittest
from unittest.mock import patch

import manage


def catalog():
    return dict(source='external:channel.override.etc', sha256=manage.CHANNEL_TABLE_SHA256,
                channels=120, ports=sorted(10000 + int(i) for i in re.findall(
                    rb'(?m)^[ \t]*(\d+)[ \t]+`<chn_channel_info_[^`]+>`', manage.CHANNEL_TABLE.read_bytes())))


class DefaultChannelSelection(unittest.TestCase):
    def run_prepare(self, root, channel=None, result=None):
        pvf = root / 'input.pvf'
        pvf.write_bytes(b'isolated fixture; real PVF parsing is covered separately')
        args = Namespace(host_ip='192.168.1.50', client_dir=None, pvf=str(pvf),
                         channel_info=channel, buff_group_sync='off')
        with patch.object(manage, 'RUNTIME', root / 'runtime'), patch.object(manage, 'check_network'), \
                patch.object(manage.socket, 'socket'), patch.object(manage, 'run', return_value=json.dumps(result or catalog())) as run:
            manage.prepare(args)
        return run

    def test_fresh_default_selects_baseline_and_all_generated_ports(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            run = self.run_prepare(root)
            runtime = root / 'runtime'
            self.assertEqual((runtime / 'data/channel.override.etc').read_bytes(), manage.CHANNEL_TABLE.read_bytes())
            config = json.loads((runtime / 'instance.json').read_text())
            self.assertEqual(config['channel_file'], 'channel.override.etc')
            self.assertEqual(config['channel_source'], str(manage.CHANNEL_TABLE))
            self.assertIn('-channel', run.call_args.args)
            self.assertIn('/data/channel.override.etc', run.call_args.args)
            manifest = json.loads((runtime / 'data/manifest.json').read_text())
            self.assertEqual(manifest['sha256'], manage.CHANNEL_TABLE_SHA256)
            compose = json.loads((runtime / 'compose.json').read_text())
            ports = compose['services']['server']['ports']
            for port in (10244, 10245, 10246):
                self.assertIn(f'192.168.1.50:{port}:{port}/tcp', ports)
            self.assertFalse(any(f':{port}:' in value for port in (10261, 10262, 10263) for value in ports))
            self.assertIn('DNFBRIDGE_CHANNEL_INFO_PATH=data/dnf/channel.override.etc\n', (runtime / 'server.env').read_text())
            before = (runtime / 'instance.json').read_bytes()
            self.run_prepare(root)
            self.assertEqual((runtime / 'instance.json').read_bytes(), before)

    def test_explicit_old_table_is_rejected_before_staging(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            old = root / 'old.etc'
            old.write_bytes(manage.CHANNEL_TABLE.read_bytes().replace(b'   244   `', b'   261   `', 1))
            with self.assertRaisesRegex(ValueError, 'common 120-channel baseline'):
                self.run_prepare(root, str(old))
            self.assertFalse((root / 'runtime').exists())

    def test_existing_old_table_requires_explicit_selection_and_preserves_old(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.run_prepare(root)
            runtime = root / 'runtime'
            override = runtime / 'data/channel.override.etc'
            legacy = runtime / 'data/channel_info.etc'
            legacy.write_bytes(b'old runtime table, retained as migration evidence')
            config = json.loads((runtime / 'instance.json').read_text())
            config['channel_file'] = legacy.name
            (runtime / 'instance.json').write_text(json.dumps(config))
            before = (runtime / 'instance.json').read_bytes()
            with self.assertRaisesRegex(ValueError, 'Existing runtime channel table'):
                self.run_prepare(root)
            self.assertEqual((runtime / 'instance.json').read_bytes(), before)
            self.run_prepare(root, str(manage.CHANNEL_TABLE))
            self.assertEqual(legacy.read_bytes(), b'old runtime table, retained as migration evidence')
            self.assertEqual(override.read_bytes(), manage.CHANNEL_TABLE.read_bytes())

    def test_existing_different_override_is_never_overwritten(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.run_prepare(root)
            target = root / 'runtime/data/channel.override.etc'
            target.write_bytes(b'custom existing external table')
            with self.assertRaisesRegex(ValueError, 'Existing external channel table differs'):
                self.run_prepare(root, str(manage.CHANNEL_TABLE))
            self.assertEqual(target.read_bytes(), b'custom existing external table')

    def test_wrong_extractor_identity_or_ports_cannot_write_config(self):
        for key, value in (('sha256', '0' * 64), ('source', 'etc/channel_info.etc'),
                           ('channels', 119), ('ports', [10261] * 120)):
            with self.subTest(key=key), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                result = copy.deepcopy(catalog())
                result[key] = value
                with self.assertRaisesRegex(ValueError, 'Prepared channel catalog differs'):
                    self.run_prepare(root, result=result)
                self.assertFalse((root / 'runtime/instance.json').exists())
                self.assertFalse((root / 'runtime/server.env').exists())


if __name__ == '__main__':
    unittest.main()
