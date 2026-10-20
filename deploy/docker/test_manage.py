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

    def public_config(self):
        return {"profile": "docker-remote-public-v1", "host_ip": "10.1.0.2", "advertised_ip": "8.8.8.8",
                "server_ip": "10.254.90.2", "database_ip": "10.254.90.3", "subnet": "10.254.90.0/24",
                "database_password": "abc", "root_password": "def", "admin_token": "123",
                "body_codec": "plaintext", "client_body_codec": "plaintext", "joust_event_enabled": True,
                "player_strict": True, "player_proxy_secret": "proxy", "player_admin_secret": "admin"}

    def test_public_nat_separates_bind_and_advertisement_without_exposing_management(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(manage, "RUNTIME", Path(directory)):
            manage.generate(self.public_config(), {"ports": [10006, 10508]})
            spec = json.loads((Path(directory) / "compose.json").read_text())
            self.assertNotIn("ports", spec["services"]["db"])
            self.assertEqual(spec["services"]["server"]["ports"], [
                "127.0.0.1:7001:7001/tcp", "127.0.0.1:17002:17002/tcp", "127.0.0.1:18111:18111/tcp",
                "10.1.0.2:10006:10006/tcp", "10.1.0.2:10508:10508/tcp", "10.1.0.2:30000-30063:30000-30063/udp"])
            env = (Path(directory) / "server.env").read_text()
            self.assertIn("DNFBRIDGE_SERVER_IP=8.8.8.8\n", env)
            self.assertIn("DNFBRIDGE_GAME_LISTEN_HOST=10.254.90.2\n", env)

    def test_public_profile_requires_explicit_opt_in_and_authentication(self):
        for changes in ({"profile": "docker-local-token-v1"}, {"player_strict": False},
                        {"player_proxy_secret": ""}, {"player_admin_secret": ""},
                        {"host_ip": "0.0.0.0"}, {"host_ip": "8.8.8.8"},
                        {"advertised_ip": "10.1.0.2"}, {"advertised_ip": "224.0.0.1"}):
            with self.subTest(changes=changes), self.assertRaises(ValueError):
                manage.player_endpoints(self.public_config() | changes, {"ports": [10006]})

    def test_gm_is_disabled_by_default(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(manage, "RUNTIME", Path(directory)):
            manage.generate(self.public_config(), {"ports": [10006]})
            self.assertNotIn("DNFBRIDGE_GM_", (Path(directory) / "server.env").read_text())
            spec = json.loads((Path(directory) / "compose.json").read_text())
            self.assertFalse(any(":18112:" in p for p in spec["services"]["server"]["ports"]))

    def test_gm_is_loopback_only_and_keeps_password_out_of_compose(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(manage, "RUNTIME", Path(directory)):
            secret = "ab" * 24
            config = self.public_config() | {"gm_enabled": True, "gm_password": secret, "gm_port": 18115,
                                              "player_admin_secret": "cd" * 24}
            manage.generate(config, {"ports": [10006], "pvf_sha256": "a" * 64})
            raw = (Path(directory) / "compose.json").read_text()
            spec = json.loads(raw)
            self.assertNotIn(secret, raw)
            self.assertNotIn("ports", spec["services"]["db"])
            self.assertNotIn("127.0.0.1:18115:18115/tcp", spec["services"]["server"]["ports"])
            self.assertEqual(spec["services"]["gm"]["ports"], ["127.0.0.1:18115:18115/tcp"])
            self.assertNotIn("server", spec["services"]["gm"]["depends_on"])
            self.assertEqual(spec["services"]["gm"]["environment"]["GOMEMLIMIT"], "768MiB")
            self.assertNotEqual(spec["services"]["server"]["image"], spec["services"]["gm"]["image"])
            env = (Path(directory) / "server.env").read_text()
            self.assertNotIn("DNFBRIDGE_GM_", env)
            self.assertNotIn(secret, env)
            gm = json.loads((Path(directory) / "gm/config.json").read_text())
            self.assertEqual(gm["listen"], "10.254.90.4:18115")
            self.assertEqual(gm["password"], secret)
            self.assertEqual(gm["pvfSha256"], "a" * 64)

    def test_independent_gm_rejects_wrong_asset_or_colliding_address(self):
        good = self.public_config() | {"gm_enabled": True, "gm_password": "ab" * 24,
                                      "player_admin_secret": "cd" * 24}
        for changes in ({"gm_ip": "10.254.90.2"}, {"gm_ip": "10.254.90.3"},
                        {"gm_ip": "10.254.90.1"}, {"gm_ip": "8.8.8.8"},
                        {"player_admin_secret": "short"}):
            with tempfile.TemporaryDirectory() as directory, patch.object(manage, "RUNTIME", Path(directory)):
                with self.assertRaises(ValueError):
                    manage.generate(good | changes, {"ports": [10006], "pvf_sha256": "a" * 64})
                self.assertEqual(list(Path(directory).iterdir()), [])
        with tempfile.TemporaryDirectory() as directory, patch.object(manage, "RUNTIME", Path(directory)):
            with self.assertRaises(ValueError):
                manage.generate(good, {"ports": [10006]})

    def test_gm_rejects_bad_secrets_and_port_collisions_before_writing(self):
        good = self.public_config() | {"gm_enabled": True, "gm_password": "ab" * 24}
        invalid = [{"gm_port": p} for p in (True, "18112", 443, 65536, 10000, 10006, 7001, 17002, 18111, 3306, 30000)]
        invalid += [{"gm_password": p} for p in (None, "", "abc", "ab" * 24 + "\nINJECT=1")]
        invalid += [{"gm_enabled": "true"}]
        for changes in invalid:
            with self.subTest(changes=changes), tempfile.TemporaryDirectory() as directory, patch.object(manage, "RUNTIME", Path(directory)):
                with self.assertRaises(ValueError):
                    manage.generate(good | changes, {"ports": [10006]})
                self.assertEqual(list(Path(directory).iterdir()), [])

    def test_bootstrap_cannot_shadow_channel_or_management_ports(self):
        for port in (10006, 7001, 17002, 18111, 3306, 443, 65536, "10000"):
            with self.subTest(port=port), self.assertRaises(ValueError):
                manage.player_endpoints(self.public_config() | {"bootstrap_port": port}, {"ports": [10006]})

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
