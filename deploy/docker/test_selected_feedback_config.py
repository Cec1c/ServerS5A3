import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import manage
import test_manage

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('selected_feedback_worker', ROOT / 'deploy/remote/game_update.py')
worker = importlib.util.module_from_spec(spec)
spec.loader.exec_module(worker)


class SelectedFeedbackConfiguration(unittest.TestCase):
    def config(self):
        return test_manage.DeploymentBoundaries().gm_config()

    def test_512_ports_environment_and_runtime_projection_match(self):
        config = self.config() | {'party_udp_relay_port_count': 512}
        with tempfile.TemporaryDirectory() as directory, patch.object(manage, 'RUNTIME', Path(directory)):
            manage.generate(config, {'ports': [10006]})
            compose = json.loads((Path(directory) / 'compose.json').read_text())
            self.assertIn('DNFBRIDGE_PARTY_UDP_RELAY_PORT_COUNT=512\n', (Path(directory) / 'server.env').read_text())
            ports = compose['services']['server']['ports']
            self.assertEqual(ports, worker.expected_server_ports(config['host_ip'], [10006], 512))
            self.assertTrue(ports[-1].endswith(':30000-30511:30000-30511/udp'))
        for invalid in (True, None, '512', 512.0, 63, 1025):
            with self.subTest(value=invalid), tempfile.TemporaryDirectory() as directory, patch.object(manage, 'RUNTIME', Path(directory)):
                with self.assertRaises(ValueError):
                    manage.generate(self.config() | {'party_udp_relay_port_count': invalid}, {'ports': [10006]})
                self.assertFalse((Path(directory) / 'server.env').exists())

    def test_old_clients_keep_legacy_tag_protocol(self):
        old = '28e04d1d1c148c841318d533ed8ccb3e426ba5a5f24328b3c913b81303b12a84'
        with patch.object(manage, 'reviewed_client_sha256', return_value=old):
            self.assertEqual(manage.selected_client_protocol_env({'tag_buff_group_sync': 'auto'}, 'on')['DNFBRIDGE_TAG_BUFF_GROUP_SYNC'], 'off')
            with self.assertRaises(ValueError):
                manage.selected_client_protocol_env({'tag_buff_group_sync': 'on'}, 'on')
            with self.assertRaises(ValueError):
                manage.selected_client_protocol_env({'wedding_gender_policy': 'deny_male_male'}, 'on')

    def test_exact_cumulative_client_enables_both_selected_options(self):
        client = '58e51fba3258a9499e77caedaea16a6d9f38904054e10edd91564e24894547c4'
        with patch.object(manage, 'reviewed_client_sha256', return_value=client):
            config = {'tag_buff_group_sync': 'on', 'wedding_gender_policy': 'deny_male_male'}
            self.assertEqual(manage.selected_client_protocol_env(config, 'on'), {
                'DNFBRIDGE_TAG_BUFF_GROUP_SYNC': 'on', 'DNFBRIDGE_WEDDING_GENDER_POLICY': 'deny_male_male'})
            with self.assertRaises(ValueError):
                manage.selected_client_protocol_env(config, 'off')


if __name__ == '__main__':
    unittest.main()
