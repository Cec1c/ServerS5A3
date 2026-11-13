"""Fresh-install flow with isolated files and mocked Docker asset extraction."""
from argparse import Namespace
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import manage
import test_buff_swap_config as existing_tests


class FreshBuffConfiguration(unittest.TestCase):
    def args(self, pvf, client_dir=None, mode=None):
        return Namespace(host_ip="192.168.1.50", client_dir=client_dir,
                         pvf=str(pvf), channel_info=None, buff_group_sync=mode)

    def prepare(self, root, args):
        runtime = root / "runtime"

        def extract(*unused, **kwargs):
            (runtime / "data/channel_info.etc").write_text("isolated mocked asset output\n")
            return json.dumps({"channels": 1, "ports": [10006]})

        with patch.object(manage, "RUNTIME", runtime), patch.object(manage, "check_network"), \
                patch.object(manage.socket, "socket"), patch.object(manage, "run", side_effect=extract):
            manage.prepare(args)
        return runtime

    def test_fresh_server_persists_on_and_reprepare_preserves_it(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            pvf = root / "fixture.pvf"
            pvf.write_bytes(b"isolated fixture; Docker parser is mocked")
            args = self.args(pvf)
            runtime = self.prepare(root, args)
            original = (runtime / "instance.json").read_bytes()
            config = json.loads(original)
            self.assertEqual(config["client_version"], "official-24.9")
            self.assertEqual(config["buff_group_sync"], "on")
            env = (runtime / "server.env").read_text()
            self.assertIn("DNFBRIDGE_BUFF_GROUP_SYNC=on\n", env)
            spec = json.loads((runtime / "compose.json").read_text())
            self.assertEqual(spec["services"]["server"]["env_file"], ["server.env"])
            self.assertEqual(spec["services"]["db"]["volumes"], ["database:/var/lib/mysql"])
            self.prepare(root, args)
            self.assertEqual((runtime / "instance.json").read_bytes(), original)

    def test_known_client_pairs_select_the_matching_protocol(self):
        with tempfile.TemporaryDirectory() as directory:
            executable = Path(directory) / "DNF.exe"
            executable.write_bytes(b"isolated identity fixture")
            config = {"buff_group_sync": "auto", "client_dir": directory}
            for digest, expected in manage.BUFF_GROUP_CLIENTS.items():
                with self.subTest(digest=digest), patch.object(manage, "digest", return_value=digest):
                    self.assertEqual(manage.buff_group_sync_env(config, {}), expected)

    def test_fresh_legacy_client_persists_off(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            pvf = root / "fixture.pvf"
            pvf.write_bytes(b"isolated mocked PVF")
            client = root / "client"
            client.mkdir()
            exe = client / "DNF.exe"
            exe.write_bytes(b"isolated legacy identity fixture")
            digest = manage.digest
            legacy = next(key for key, mode in manage.BUFF_GROUP_CLIENTS.items() if mode == "off")
            with patch.object(manage, "digest", side_effect=lambda path: legacy if Path(path) == exe else digest(path)):
                runtime = self.prepare(root, self.args(pvf, str(client)))
            self.assertEqual(json.loads((runtime / "instance.json").read_text())["buff_group_sync"], "off")
            self.assertIn("DNFBRIDGE_BUFF_GROUP_SYNC=off\n", (runtime / "server.env").read_text())

    def test_existing_unconfigured_and_explicit_off_are_preserved(self):
        for settings in ({}, {"runtime_compatibility": {manage.BUFF_GROUP_SYNC_KEY: "off"}},
                         {"buff_group_sync": "off"}):
            with self.subTest(settings=settings), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                runtime = root / "runtime"
                runtime.mkdir()
                config = existing_tests.BuffSwapReapplyConfiguration().local_config() | settings
                instance = runtime / "instance.json"
                instance.write_text(json.dumps(config, indent=2) + "\n")
                args = self.args(root / "unused")
                args.pvf = None
                assets = runtime / "data"
                assets.mkdir()
                (assets / "Script.pvf").write_bytes(b"isolated mocked PVF")
                self.prepare(root, args)
                saved = json.loads(instance.read_text())
                self.assertEqual(saved, config)
                env = (runtime / "server.env").read_text()
                if settings:
                    self.assertIn("DNFBRIDGE_BUFF_GROUP_SYNC=off\n", env)
                else:
                    self.assertNotIn(manage.BUFF_GROUP_SYNC_KEY + "=", env)

    def test_auto_unknown_or_missing_client_stops_before_writing(self):
        for present in (False, True):
            with self.subTest(present=present), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                if present:
                    (root / "DNF.exe").write_bytes(b"unknown isolated client")
                args = self.args(root / "unused.pvf", str(root))
                with self.assertRaisesRegex(ValueError, "buff_group_sync auto"):
                    self.prepare(root, args)
                self.assertFalse((root / "runtime").exists())

    def test_explicit_legacy_choice_does_not_require_client_detection(self):
        self.assertEqual(manage.buff_group_sync_env({"buff_group_sync": "off", "client_dir": "missing"}, {}), "off")
        self.assertEqual(manage.buff_group_sync_env({"buff_group_sync": "auto"}, {manage.BUFF_GROUP_SYNC_KEY: "off"}), "off")

    def test_invalid_conflicting_or_non_current_on_are_rejected(self):
        for mode in (None, True, False, "", "ON", "true", 1):
            with self.subTest(mode=mode), self.assertRaises(ValueError):
                manage.buff_group_sync_env({"buff_group_sync": mode}, {})
        with self.assertRaisesRegex(ValueError, "conflicts"):
            manage.buff_group_sync_env({"buff_group_sync": "on"}, {manage.BUFF_GROUP_SYNC_KEY: "off"})
        with self.assertRaisesRegex(ValueError, "official-24.9"):
            manage.buff_group_sync_env({"buff_group_sync": "on", "client_version": "legacy"}, {})


if __name__ == "__main__":
    unittest.main()
