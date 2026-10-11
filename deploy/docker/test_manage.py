import ipaddress
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import manage


class DeploymentBoundaries(unittest.TestCase):
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
            self.assertEqual(spec["volumes"], {"database": {}})

    def test_overlapping_network_fails_before_deployment(self):
        config = {"subnet": "10.253.90.0/24", "server_ip": "10.253.90.2", "database_ip": "10.253.90.3"}
        other = [{"Name": "other-project", "IPAM": {"Config": [{"Subnet": "10.253.0.0/16"}]}}]
        with patch.object(manage, "run", side_effect=["abc\n", json.dumps(other)]), self.assertRaisesRegex(ValueError, "overlaps"):
            manage.check_network(config)


if __name__ == "__main__":
    unittest.main()
