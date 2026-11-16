"""Disjoint epic display is strict, optional, and isolated to server.env."""
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import manage


class DisjointEpicDisplayConfig(unittest.TestCase):
    KEY = b"DNFBRIDGE_DISJOINT_EPIC_DISPLAY="
    RETAINED_KEYS = (
        b"DNFBRIDGE_CHANNEL_SCRIPT_ENCODING=gbk\n",
        b"DNFBRIDGE_GOLD_DIAGNOSTIC_CHARACTER=42\n",
        b"DNFBRIDGE_MAIN_QUEST_EXP_PERCENT=15\n",
        b"DNFBRIDGE_MAIN_QUEST_EXP_MULTIPLIER=10\n",
        b"DNFBRIDGE_DUNGEON_TOURNAMENT=vendor\n",
    )

    def config(self):
        return {
            "host_ip": "192.168.1.50",
            "server_ip": "10.254.90.2",
            "database_ip": "10.254.90.3",
            "subnet": "10.254.90.0/24",
            "database_password": "fixture",
            "root_password": "fixture",
            "admin_token": "ab" * 24,
            "body_codec": "plaintext",
            "client_body_codec": "plaintext",
            "joust_event_enabled": True,
            "channel_script_encoding": "gbk",
            "gold_diagnostic_character": 42,
            "main_quest_exp_percent": 15,
            "main_quest_exp_multiplier": 10,
            "dungeon_tournament": "vendor",
        }

    def generated(self, root, config):
        with patch.object(manage, "RUNTIME", root):
            manage.generate(config, {"ports": [10006]})
        return {
            path.relative_to(root).as_posix(): path.read_bytes()
            for path in root.rglob("*")
            if path.is_file()
        }

    def test_optional_display_only_appends_its_environment_key(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            config = self.config()
            before = self.generated(root, config)
            baseline_env = before["server.env"]
            self.assertNotIn(self.KEY, baseline_env)
            for retained in self.RETAINED_KEYS:
                self.assertIn(retained, baseline_env)

            for display in ("off", "paired-v1"):
                with self.subTest(display=display):
                    after = self.generated(
                        root, config | {"disjoint_epic_display": display}
                    )
                    self.assertEqual(set(after), set(before))
                    self.assertEqual(
                        {path for path in before if before[path] != after[path]},
                        {"server.env"},
                    )
                    display_line = self.KEY + display.encode("ascii") + b"\n"
                    self.assertEqual(
                        after["server.env"], baseline_env + display_line
                    )
                    for retained in self.RETAINED_KEYS:
                        self.assertIn(retained, after["server.env"])

            restored = self.generated(root, config)
            self.assertEqual(restored, before)
            self.assertNotIn(self.KEY, restored["server.env"])

    def test_invalid_display_rejects_before_writing_any_file(self):
        invalid = (
            None,
            True,
            False,
            0,
            1,
            1.0,
            [],
            {},
            "",
            "OFF",
            "paired_v1",
            " paired-v1",
            "paired-v1 ",
            "off\n",
            "paired-v1\nOTHER=1",
        )
        for value in invalid:
            with self.subTest(value=value), tempfile.TemporaryDirectory() as temp:
                root = Path(temp)
                with (
                    patch.object(manage, "RUNTIME", root),
                    self.assertRaisesRegex(ValueError, "disjoint_epic_display"),
                ):
                    manage.generate(
                        self.config() | {"disjoint_epic_display": value},
                        {"ports": [10006]},
                    )
                self.assertEqual(list(root.iterdir()), [])


if __name__ == "__main__":
    unittest.main()
