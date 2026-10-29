"""Channel script encoding is strict, optional, and isolated to server.env."""
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import manage


class ChannelScriptEncodingConfig(unittest.TestCase):
    KEY = b"DNFBRIDGE_CHANNEL_SCRIPT_ENCODING="
    RETAINED_KEYS = (
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

    def test_optional_encoding_only_appends_its_environment_key(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            config = self.config()
            before = self.generated(root, config)
            baseline_env = before["server.env"]
            self.assertNotIn(self.KEY, baseline_env)
            for retained in self.RETAINED_KEYS:
                self.assertIn(retained, baseline_env)

            for encoding in ("utf8", "gbk"):
                with self.subTest(encoding=encoding):
                    after = self.generated(
                        root, config | {"channel_script_encoding": encoding}
                    )
                    self.assertEqual(set(after), set(before))
                    self.assertEqual(
                        {path for path in before if before[path] != after[path]},
                        {"server.env"},
                    )
                    encoding_line = self.KEY + encoding.encode("ascii") + b"\n"
                    self.assertEqual(
                        after["server.env"], baseline_env + encoding_line
                    )
                    for retained in self.RETAINED_KEYS:
                        self.assertIn(retained, after["server.env"])

            restored = self.generated(root, config)
            self.assertEqual(restored, before)
            self.assertNotIn(self.KEY, restored["server.env"])

    def test_invalid_encoding_rejects_before_writing_any_file(self):
        invalid = (
            None,
            True,
            False,
            0,
            1,
            [],
            {},
            ["utf8"],
            {"encoding": "utf8"},
            "UTF8",
            "",
            "\n",
            "utf8\n",
            "gbk\n",
            "utf8\nOTHER=1",
        )
        for value in invalid:
            with self.subTest(value=value), tempfile.TemporaryDirectory() as temp:
                root = Path(temp)
                with (
                    patch.object(manage, "RUNTIME", root),
                    self.assertRaises(ValueError),
                ):
                    manage.generate(
                        self.config() | {"channel_script_encoding": value},
                        {"ports": [10006]},
                    )
                self.assertEqual(list(root.iterdir()), [])


if __name__ == "__main__":
    unittest.main()
