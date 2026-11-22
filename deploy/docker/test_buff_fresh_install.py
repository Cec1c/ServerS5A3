"""Fresh-install flow with isolated files and mocked Docker asset extraction."""
from argparse import Namespace
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import manage
import test_buff_swap_config as existing_tests
from test_channel_baseline import catalog


class FreshBuffConfiguration(unittest.TestCase):
    def args(self, pvf, client_dir=None, mode=None):
        return Namespace(host_ip="192.168.1.50", client_dir=client_dir,
                         pvf=str(pvf), channel_info=None, buff_group_sync=mode)

    def prepare(self, root, args):
        runtime = root / "runtime"

        def extract(*unused, **kwargs):
            return json.dumps(catalog())

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
            self.assertEqual(config["tag_buff_group_sync"], "auto")
            self.assertIn("DNFBRIDGE_BUFF_SWAP_REAPPLY=on\n", env)
            self.assertIn("DNFBRIDGE_MILEAGE_SHOP_ENABLED=on\n", env)
            self.assertIn("DNFBRIDGE_TAG_BUFF_GROUP_SYNC=off\n", env)
            self.assertIn("DNFBRIDGE_JOUST_EVENT_ENABLED=false\n", env)
            self.assertIn("DNFBRIDGE_DUNGEON_TOURNAMENT=vendor\n", env)
            self.assertIn("DNFBRIDGE_GUILD_RANKING_ENABLED=true\n", env)
            self.assertIn("DNFBRIDGE_GUILD_HONGBAO_ENABLED=true\n", env)
            self.assertIn("DNFBRIDGE_GUILD_ESSENCE_ENABLED=true\n", env)
            self.assertIn("DNFBRIDGE_DISJOINT_EPIC_DISPLAY=paired-v1\n", env)
            self.assertIn("DNFBRIDGE_TOWN_DISPLAY_ONLY=on\n", env)
            spec = json.loads((runtime / "compose.json").read_text())
            self.assertEqual(spec["services"]["server"]["env_file"], ["server.env"])
            self.assertEqual(spec["services"]["db"]["volumes"], ["database:/var/lib/mysql"])
            self.prepare(root, args)
            self.assertEqual((runtime / "instance.json").read_bytes(), original)

    def test_fresh_v216_enables_empire_buff_groups(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            pvf = root / "fixture.pvf"
            pvf.write_bytes(b"isolated mocked PVF")
            executable = root / "DNF.exe"
            executable.write_bytes(b"isolated client identity fixture")
            reviewed = "58e51fba3258a9499e77caedaea16a6d9f38904054e10edd91564e24894547c4"
            digest = manage.digest
            with patch.object(manage, "digest", side_effect=lambda path: reviewed if Path(path) == executable else digest(path)):
                runtime = self.prepare(root, self.args(pvf, str(root)))
                env = (runtime / "server.env").read_text()
                self.assertIn("DNFBRIDGE_TAG_BUFF_GROUP_SYNC=on\n", env)
                self.assertIn("DNFBRIDGE_MILEAGE_SHOP_ENABLED=on\n", env)
            config = json.loads((runtime / "instance.json").read_text())
            config["runtime_compatibility"]["DNFBRIDGE_MILEAGE_SHOP_ENABLED"] = "off"
            config["tag_buff_group_sync"] = "off"
            (runtime / "instance.json").write_text(json.dumps(config))
            self.prepare(root, self.args(pvf))
            env = (runtime / "server.env").read_text()
            self.assertIn("DNFBRIDGE_MILEAGE_SHOP_ENABLED=off\n", env)
            self.assertIn("DNFBRIDGE_TAG_BUFF_GROUP_SYNC=off\n", env)

    def test_existing_recommended_opt_in_keeps_explicit_values(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            pvf = root / "fixture.pvf"
            pvf.write_bytes(b"isolated mocked PVF")
            args = self.args(pvf)
            runtime = self.prepare(root, args)
            instance = runtime / "instance.json"
            saved = json.loads(instance.read_text())
            saved.pop("dungeon_tournament", None)
            saved["guild_hongbao_enabled"] = False
            saved["tag_buff_group_sync"] = "off"
            saved["runtime_compatibility"] = {"DNFBRIDGE_MILEAGE_SHOP_ENABLED": "off"}
            saved["buff_group_sync"] = "off"
            saved["client_exe_sha256"] = "58e51fba3258a9499e77caedaea16a6d9f38904054e10edd91564e24894547c4"
            instance.write_text(json.dumps(saved))
            before = instance.read_bytes()
            args.apply_recommended = True
            self.prepare(root, args)
            env = (runtime / "server.env").read_text()
            self.assertIn("DNFBRIDGE_DUNGEON_TOURNAMENT=vendor\n", env)
            self.assertIn("DNFBRIDGE_GUILD_HONGBAO_ENABLED=false\n", env)
            self.assertIn("DNFBRIDGE_BUFF_GROUP_SYNC=off\n", env)
            self.assertIn("DNFBRIDGE_TAG_BUFF_GROUP_SYNC=off\n", env)
            self.assertIn("DNFBRIDGE_MILEAGE_SHOP_ENABLED=off\n", env)
            backups = list(runtime.glob("instance.before-recommended-*.json"))
            self.assertEqual(len(backups), 1)
            self.assertEqual(backups[0].read_bytes(), before)
            self.prepare(root, args)
            self.assertEqual(list(runtime.glob("instance.before-recommended-*.json")), backups)

    def test_prepare_persists_and_clears_public_host(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            pvf = root / "fixture.pvf"
            pvf.write_bytes(b"isolated mocked PVF")
            args = self.args(pvf)
            args.public_host = "8.8.4.4"
            runtime = self.prepare(root, args)
            args.public_host = None
            self.prepare(root, args)
            instance = json.loads((runtime / "instance.json").read_text())
            self.assertEqual(instance["public_host"], "8.8.4.4")
            self.assertIn("DNFBRIDGE_SERVER_IP=8.8.4.4\n", (runtime / "server.env").read_text())
            args.public_host = ""
            self.prepare(root, args)
            self.assertIn("DNFBRIDGE_SERVER_IP=192.168.1.50\n", (runtime / "server.env").read_text())

    def test_missing_replacement_client_cannot_erase_saved_identity(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            pvf = root / "fixture.pvf"
            pvf.write_bytes(b"isolated mocked PVF")
            runtime = self.prepare(root, self.args(pvf))
            instance = runtime / "instance.json"
            saved = json.loads(instance.read_text())
            saved["client_exe_sha256"] = "58e51fba3258a9499e77caedaea16a6d9f38904054e10edd91564e24894547c4"
            instance.write_text(json.dumps(saved))
            self.prepare(root, self.args(pvf))
            before = instance.read_bytes()
            env = (runtime / "server.env").read_bytes()
            with self.assertRaisesRegex(ValueError, "DNF.exe"):
                self.prepare(root, self.args(pvf, str(root / "missing")))
            self.assertEqual(instance.read_bytes(), before)
            self.assertEqual((runtime / "server.env").read_bytes(), env)

    def test_reprepare_rejects_legacy_client_with_modern_switches(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            pvf = root / "fixture.pvf"
            pvf.write_bytes(b"isolated mocked PVF")
            runtime = self.prepare(root, self.args(pvf))
            old_instance = (runtime / "instance.json").read_bytes()
            old_environment = (runtime / "server.env").read_bytes()
            executable = root / "DNF.exe"
            executable.write_bytes(b"isolated legacy identity fixture")
            original_digest = manage.digest
            legacy = next(key for key, mode in manage.BUFF_GROUP_CLIENTS.items() if mode == "off")
            with patch.object(manage, "digest", side_effect=lambda path: legacy if Path(path) == executable else original_digest(path)):
                with self.assertRaisesRegex(ValueError, "legacy DNF.exe"):
                    self.prepare(root, self.args(pvf, str(root)))
            self.assertEqual((runtime / "instance.json").read_bytes(), old_instance)
            self.assertEqual((runtime / "server.env").read_bytes(), old_environment)

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
            env = (runtime / "server.env").read_text()
            self.assertIn("DNFBRIDGE_BUFF_GROUP_SYNC=off\n", env)
            self.assertNotIn("DNFBRIDGE_MILEAGE_SHOP_ENABLED=on\n", env)
            self.assertIn("DNFBRIDGE_TAG_BUFF_GROUP_SYNC=off\n", env)

    def test_existing_unconfigured_and_explicit_off_are_preserved(self):
        for settings in ({}, {"runtime_compatibility": {manage.BUFF_GROUP_SYNC_KEY: "off"}},
                         {"buff_group_sync": "off"}):
            with self.subTest(settings=settings), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                runtime = root / "runtime"
                runtime.mkdir()
                config = existing_tests.BuffSwapReapplyConfiguration().local_config() | settings
                config.update(channel_file="channel.override.etc", channel_source=str(manage.CHANNEL_TABLE))
                instance = runtime / "instance.json"
                instance.write_text(json.dumps(config, indent=2) + "\n")
                args = self.args(root / "unused")
                args.pvf = None
                assets = runtime / "data"
                assets.mkdir()
                (assets / "Script.pvf").write_bytes(b"isolated mocked PVF")
                (assets / "channel.override.etc").write_bytes(manage.CHANNEL_TABLE.read_bytes())
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
                with self.assertRaisesRegex(ValueError, "DNF.exe"):
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
