import ipaddress
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import manage


class DeploymentBoundaries(unittest.TestCase):
    def test_guild_solo_creation_requires_explicit_boolean(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(manage, "RUNTIME", Path(directory)):
            for value in [None, False, True]:
                config = {"host_ip": "192.168.1.50", "server_ip": "10.253.90.2", "database_ip": "10.253.90.3", "subnet": "10.253.90.0/24", "database_password": "abc", "root_password": "def", "admin_token": "abc", "body_codec": "plaintext", "client_body_codec": "plaintext", "joust_event_enabled": True}
                if value is not None:
                    config["guild_solo_create"] = value
                manage.generate(config, {"ports": [10006]})
                self.assertIn("DNFBRIDGE_GUILD_SOLO_CREATE=" + str(value is True).lower(), (Path(directory) / "server.env").read_text())
            for value in ["false", "true", 0, 1]:
                with self.assertRaises(ValueError):
                    manage.generate({"host_ip": "192.168.1.50", "server_ip": "10.253.90.2", "database_ip": "10.253.90.3", "subnet": "10.253.90.0/24", "database_password": "abc", "root_password": "def", "admin_token": "abc", "body_codec": "plaintext", "client_body_codec": "plaintext", "joust_event_enabled": True} | {"guild_solo_create": value}, {"ports": [10006]})

    def test_advertised_region_rejects_invalid_values_before_writing(self):
        for value in (-1, 250, True, "1", 1.5):
            with self.subTest(value=value), tempfile.TemporaryDirectory() as directory:
                with patch.object(manage, "RUNTIME", Path(directory)), self.assertRaisesRegex(ValueError, "channel_advertise_server"):
                    manage.generate({"channel_advertise_server": value}, {})
                self.assertEqual(list(Path(directory).iterdir()), [])

    def test_only_lan_addresses_are_accepted(self):
        for value in ("0.0.0.0", "127.0.0.1", "8.8.8.8", "169.254.1.1", "::1"):
            with self.subTest(value=value), self.assertRaises(ValueError):
                manage.private_ipv4(value)
        self.assertEqual(manage.private_ipv4("192.168.0.101"), "192.168.0.101")

    def test_secrets_and_database_are_not_published(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(manage, "RUNTIME", Path(directory)):
            config = {"host_ip": "192.168.1.50", "server_ip": "10.253.90.2", "database_ip": "10.253.90.3",
                      "subnet": "10.253.90.0/24", "database_password": "DB_PRIVATE", "root_password": "ROOT_PRIVATE",
                      "admin_token": "ADMIN_PRIVATE", "body_codec": "plaintext", "client_body_codec": "plaintext",
                      "joust_event_enabled": True}
            manage.generate(config, {"ports": [10011, 10019]})
            text = (Path(directory) / "compose.json").read_text()
            spec = json.loads(text)
            self.assertNotIn("PRIVATE", text)
            self.assertNotIn("ports", spec["services"]["db"])
            ports = spec["services"]["server"]["ports"]
            self.assertEqual(ports[:3], [f"127.0.0.1:{p}:{p}/tcp" for p in (7001, 17002, 18111)])
            self.assertEqual(ports[3:5], ["192.168.1.50:10011:10011/tcp", "192.168.1.50:10019:10019/tcp"])
            for port in ports:
                self.assertFalse(ipaddress.ip_address(port.split(":")[0]).is_unspecified)
            self.assertIn("DNFBRIDGE_PARTY_UDP_RELAY_MODE=embedded", (Path(directory) / "server.env").read_text())
            self.assertIn("DNFBRIDGE_CHANNEL_ADVERTISE_SERVER_INDEX=0\n", (Path(directory) / "server.env").read_text())
            config["channel_advertise_server"] = 1
            manage.generate(config, {"ports": [10011, 10019]})
            self.assertIn("DNFBRIDGE_CHANNEL_ADVERTISE_SERVER_INDEX=1\n", (Path(directory) / "server.env").read_text())
            self.assertEqual(spec["volumes"], {"database": {}})

    def test_overlapping_network_fails_before_deployment(self):
        config = {"subnet": "10.253.90.0/24", "server_ip": "10.253.90.2", "database_ip": "10.253.90.3"}
        other = [{"Name": "other-project", "IPAM": {"Config": [{"Subnet": "10.253.0.0/16"}]}}]
        with patch.object(manage, "run", side_effect=["abc\n", json.dumps(other)]), self.assertRaisesRegex(ValueError, "overlaps"):
            manage.check_network(config)

    def test_remote_profile_keeps_database_private_and_survives_container_exit(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(manage, "RUNTIME", Path(directory)):
            config = {"host_ip": "10.90.0.1", "server_ip": "10.254.90.2", "database_ip": "10.254.90.3",
                      "subnet": "10.254.90.0/24", "database_password": "abc", "root_password": "def",
                      "admin_token": "123", "body_codec": "plaintext", "client_body_codec": "plaintext",
                      "joust_event_enabled": True, "go_memory_limit": "2500MiB", "server_memory_limit": "3g",
                      "database_memory_limit": "384m", "restart": "unless-stopped"}
            manage.generate(config, {"ports": [10006, 10508]})
            services = json.loads((Path(directory) / "compose.json").read_text())["services"]
            self.assertNotIn("ports", services["db"])
            self.assertEqual(services["db"]["mem_limit"], "384m")
            self.assertEqual(services["server"]["mem_limit"], "3g")
            self.assertEqual(services["server"]["restart"], "unless-stopped")
            self.assertEqual(services["server"]["ports"][3], "10.90.0.1:10006:10006/tcp")
            self.assertIn("GOMEMLIMIT=2500MiB", (Path(directory) / "server.env").read_text())


if __name__ == "__main__":
    unittest.main()
