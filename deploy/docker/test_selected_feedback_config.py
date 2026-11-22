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

    def test_public_host_updates_advertisements_and_preserves_private_bindings(self):
        config = self.config() | {"public_host": "8.8.4.4"}
        catalog = {"ports": [10006]}
        with tempfile.TemporaryDirectory() as directory, patch.object(manage, "RUNTIME", Path(directory)):
            manage.generate(config, catalog)
            runtime = Path(directory)
            self.assertIn('public_addr = "8.8.4.4:7001"', (runtime / "server/configs/dnfbridge.toml").read_text())
            self.assertIn("DNFBRIDGE_SERVER_IP=8.8.4.4\n", (runtime / "server.env").read_text())
            spec = json.loads((runtime / "compose.json").read_text())
            endpoint = worker.player_endpoint_projection(config, catalog, manage)
            self.assertEqual(endpoint["advertised"], "8.8.4.4")
            self.assertEqual(endpoint["bind"], config["host_ip"])
            self.assertEqual(spec["services"]["server"]["ports"], endpoint["serverPorts"])
            self.assertIn("127.0.0.1:17002:17002/tcp", endpoint["serverPorts"])
            self.assertNotIn("ports", spec["services"]["db"])

    def test_public_host_rejects_incompatible_values_before_writing(self):
        for host in ("192.168.1.80", "100.64.1.1", "203.0.113.8", "2001:db8::1", "example.com", 123, "224.0.0.1", "255.255.255.255"):
            with self.subTest(host=host), tempfile.TemporaryDirectory() as directory, patch.object(manage, "RUNTIME", Path(directory)):
                with self.assertRaisesRegex(ValueError, "public_host"):
                    manage.generate(self.config() | {"public_host": host}, {"ports": [10006]})
                self.assertEqual(list(Path(directory).iterdir()), [])

    def test_empire_uses_active_client_identity_instead_of_local_checkout(self):
        ready = "21a9ed0b0c86e8e3a95f7b6c84270025c91219d3df3e9143bdd6629264c9166e"
        old = "28e04d1d1c148c841318d533ed8ccb3e426ba5a5f24328b3c913b81303b12a84"
        config = {"tag_buff_group_sync": "auto", "client_dir": ""}
        key = "DNFBRIDGE_TAG_BUFF_GROUP_SYNC"
        self.assertEqual(manage.selected_client_protocol_env(config | {"client_exe_sha256": ready}, "on")[key], "on")
        candidate = worker.installed_candidate_options(config, b"", manage, old)
        self.assertEqual(candidate["client_exe_sha256"], old)
        self.assertEqual(manage.selected_client_protocol_env(candidate, "on")[key], "off")
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory)
            (path / "DNF.exe").write_bytes(b"stale checkout fixture")
            with patch.object(manage, "digest", return_value=old):
                paired = config | {"client_exe_sha256": ready, "client_dir": directory}
                self.assertEqual(manage.selected_client_protocol_env(paired, "on")[key], "on")
            with patch.object(manage, "digest", return_value=ready):
                stale = candidate | {"client_dir": directory}
                self.assertEqual(manage.selected_client_protocol_env(stale, "on")[key], "off")

    def test_auto_ordinary_buff_prefers_active_legacy_identity(self):
        old = next(sha for sha, mode in manage.BUFF_GROUP_CLIENTS.items() if mode == "off")
        config = {"buff_group_sync": "auto", "client_dir": ""}
        candidate = worker.installed_candidate_options(config, b"", manage, old)
        self.assertEqual(candidate["client_exe_sha256"], old)
        self.assertEqual(manage.buff_group_sync_env(candidate, {}), "off")

    def test_old_clients_keep_legacy_tag_protocol(self):
        old = '28e04d1d1c148c841318d533ed8ccb3e426ba5a5f24328b3c913b81303b12a84'
        with patch.object(manage, 'client_executable_sha256', return_value=old):
            self.assertEqual(manage.selected_client_protocol_env({'tag_buff_group_sync': 'auto'}, 'on')['DNFBRIDGE_TAG_BUFF_GROUP_SYNC'], 'off')
            with self.assertRaises(ValueError):
                manage.selected_client_protocol_env({'tag_buff_group_sync': 'on'}, 'on')
            with self.assertRaises(ValueError):
                manage.selected_client_protocol_env({'wedding_gender_policy': 'deny_male_male'}, 'on')

    def test_exact_cumulative_client_enables_both_selected_options(self):
        client = '58e51fba3258a9499e77caedaea16a6d9f38904054e10edd91564e24894547c4'
        with patch.object(manage, 'client_executable_sha256', return_value=client):
            config = {'tag_buff_group_sync': 'on', 'wedding_gender_policy': 'deny_male_male'}
            self.assertEqual(manage.selected_client_protocol_env(config, 'on'), {
                'DNFBRIDGE_TAG_BUFF_GROUP_SYNC': 'on', 'DNFBRIDGE_WEDDING_GENDER_POLICY': 'deny_male_male'})
            with self.assertRaises(ValueError):
                manage.selected_client_protocol_env(config, 'off')


if __name__ == '__main__':
    unittest.main()
