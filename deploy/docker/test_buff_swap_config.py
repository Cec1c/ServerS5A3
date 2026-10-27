import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

import manage


class BuffSwapReapplyConfiguration(unittest.TestCase):
    def local_config(self):
        return {"host_ip": "192.168.1.50", "server_ip": "10.253.90.2",
                "database_ip": "10.253.90.3", "subnet": "10.253.90.0/24",
                "database_password": "abc", "root_password": "def", "admin_token": "abc",
                "body_codec": "plaintext", "client_body_codec": "plaintext",
                "joust_event_enabled": True}

    def generated_env(self, directory, config):
        manage.generate(config, {"ports": [10006]})
        return (Path(directory) / "server.env").read_text()

    def test_explicit_on_and_off_are_rendered(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(manage, "RUNTIME", Path(directory)):
            for value in ("on", "off"):
                with self.subTest(value=value):
                    env = self.generated_env(directory, self.local_config() | {"buff_swap_reapply": value})
                    self.assertIn("DNFBRIDGE_BUFF_SWAP_REAPPLY=" + value + "\n", env)

    def test_default_does_not_inject_env(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(manage, "RUNTIME", Path(directory)):
            env = self.generated_env(directory, self.local_config())
            self.assertNotIn("DNFBRIDGE_BUFF_SWAP_REAPPLY=", env)

    def test_invalid_values_are_rejected(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(manage, "RUNTIME", Path(directory)):
            for value in ("", "ON", "true", "1", True, 1):
                with self.subTest(value=value), self.assertRaisesRegex(ValueError, "buff_swap_reapply"):
                    self.generated_env(directory, self.local_config() | {"buff_swap_reapply": value})


class RuntimeCompatibilityConfiguration(unittest.TestCase):
    def local_config(self):
        return BuffSwapReapplyConfiguration().local_config()

    def compatibility_settings(self):
        return {
            "DNFBRIDGE_MYSQL_INSERT_BATCH": "200",
            "DNFBRIDGE_INVENTORY_SAVE_SCOPE": "slot",
            "DNFBRIDGE_EQUIPMENT_SAVE_SCOPE": "collection",
            "DNFBRIDGE_OWNER_CONTEXT_USE_CLIENT_CHANNEL": "on",
            "DNFBRIDGE_PARTY_SELECTOR_MEMBER_SELF_PREPARE": "on",
            "DNFBRIDGE_PARTY_DUNGEON_ALL_MEMBER_USER_STATE": "all",
            "DNFBRIDGE_DUNGEON_DEATH_ROOM_PROOF": "room_proven",
            "DNFBRIDGE_HELL_BLOCKED_SLOTS": "on",
            "DNFBRIDGE_DUNGEON_ENTRY_SELECTION_MODE": "allow",
            "DNFBRIDGE_PEER_USERINFO_IDENTITY_CARRIER_MODE0_GUARD": "on",
            "DNFBRIDGE_TOWN_NPC_EVENT_CATALOG": "on",
            "DNFBRIDGE_PARTY_FORMATION_ORDER": "vendor_wire",
            "DNFBRIDGE_PARTY_PEER_ENDPOINTS_AFTER_ROSTER": "repeat",
            "DNFBRIDGE_PARTY_PEER_ENDPOINT_REBROADCAST": "on",
            "DNFBRIDGE_LOGIN_AREA_GATES": "strict",
            "DNFBRIDGE_HOTKEY_SEEDED_LAYOUT": "off",
            "DNFBRIDGE_SKILL_SWITCH_CANDIDATE_PAIRS": "off",
            "DNFBRIDGE_LOGIN_SKILL_SWITCH_LOADOUT_AFTER_COMMIT": "before_transition_only",
            "DNFBRIDGE_PEER_SKILL_PAGE_TOWN": "off",
            "DNFBRIDGE_SKILL_SWITCH_DOWNLINK_SHAPE": "off",
            "DNFBRIDGE_SKILL_SWITCH_ANSWER_AT_SCENE_READY": "off",
        }

    def generated_env(self, directory, config):
        manage.generate(config, {"ports": [10006]})
        return (Path(directory) / "server.env").read_text()

    def test_all_runtime_compatibility_settings_are_rendered(self):
        compatibility = self.compatibility_settings()
        with tempfile.TemporaryDirectory() as directory, patch.object(manage, "RUNTIME", Path(directory)):
            env = self.generated_env(directory, self.local_config() | {"runtime_compatibility": compatibility})
            for key, value in compatibility.items():
                self.assertIn(key + "=" + value + "\n", env)

    def test_default_empty_runtime_compatibility_injects_nothing(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(manage, "RUNTIME", Path(directory)):
            for config in (self.local_config(), self.local_config() | {"runtime_compatibility": {}}):
                env = self.generated_env(directory, config)
                self.assertNotIn("DNFBRIDGE_MYSQL_INSERT_BATCH=", env)
                self.assertNotIn("DNFBRIDGE_INVENTORY_SAVE_SCOPE=", env)

    def test_sensitive_and_unknown_keys_are_rejected(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(manage, "RUNTIME", Path(directory)):
            for key in ("DNFBRIDGE_PLAYER_ADMIN_SECRET", "DNFBRIDGE_MYSQL_DSN", "DNFBRIDGE_UNKNOWN"):
                with self.subTest(key=key), self.assertRaisesRegex(ValueError, "runtime_compatibility"):
                    self.generated_env(directory, self.local_config() | {"runtime_compatibility": {key: "on"}})

    def test_type_and_value_errors_are_rejected(self):
        invalid = (
            [],
            None,
            "on",
            {"DNFBRIDGE_HELL_BLOCKED_SLOTS": True},
            {"DNFBRIDGE_HELL_BLOCKED_SLOTS": "off"},
            {"DNFBRIDGE_MYSQL_INSERT_BATCH": 200},
            {"DNFBRIDGE_MYSQL_INSERT_BATCH": "0"},
            {"DNFBRIDGE_MYSQL_INSERT_BATCH": "1001"},
            {"DNFBRIDGE_MYSQL_INSERT_BATCH": "abc"},
        )
        with tempfile.TemporaryDirectory() as directory, patch.object(manage, "RUNTIME", Path(directory)):
            for compatibility in invalid:
                with self.subTest(compatibility=compatibility), self.assertRaises(ValueError):
                    self.generated_env(directory, self.local_config() | {"runtime_compatibility": compatibility})

    def test_scope_alternatives_are_allowed(self):
        compatibility = {"DNFBRIDGE_INVENTORY_SAVE_SCOPE": "collection",
                          "DNFBRIDGE_EQUIPMENT_SAVE_SCOPE": "entry"}
        with tempfile.TemporaryDirectory() as directory, patch.object(manage, "RUNTIME", Path(directory)):
            env = self.generated_env(directory, self.local_config() | {"runtime_compatibility": compatibility})
            self.assertIn("DNFBRIDGE_INVENTORY_SAVE_SCOPE=collection\n", env)
            self.assertIn("DNFBRIDGE_EQUIPMENT_SAVE_SCOPE=entry\n", env)


if __name__ == "__main__":
    unittest.main()
