"""Focused checks for channel script encoding deployment controls."""

import importlib.util
from pathlib import Path
import unittest


SPEC = importlib.util.spec_from_file_location(
    "game_update_channel_script_encoding", Path(__file__).with_name("game_update.py")
)
worker = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(worker)

CHANNEL_ENCODING_KEY = "DNFBRIDGE_CHANNEL_SCRIPT_ENCODING"
TOURNAMENT_KEY = "DNFBRIDGE_DUNGEON_TOURNAMENT"
MULTIPLIER_KEY = "DNFBRIDGE_MAIN_QUEST_EXP_MULTIPLIER"
PERCENT_KEY = "DNFBRIDGE_MAIN_QUEST_EXP_PERCENT"


class ChannelScriptEncodingManifest(unittest.TestCase):
    def test_optional_value_accepts_only_exact_supported_names(self):
        self.assertIsNone(worker.channel_script_encoding({"unrelated": "preserved"}))
        for encoding in ("utf8", "gbk"):
            with self.subTest(encoding=encoding):
                self.assertEqual(
                    worker.channel_script_encoding(
                        {"channelScriptEncoding": encoding}
                    ),
                    encoding,
                )

        for value in (
            None,
            "",
            "UTF8",
            "UTF-8",
            "gb18030",
            "utf8\n",
            True,
            False,
            1,
            b"utf8",
            [],
            {},
        ):
            with self.subTest(value=repr(value)):
                with self.assertRaises(ValueError):
                    worker.channel_script_encoding(
                        {"channelScriptEncoding": value}
                    )


class TargetChannelScriptEncoding(unittest.TestCase):
    def test_target_instance_sets_encoding_and_default_preserves_existing(self):
        instance = {
            "server_image": "image:old",
            "channel_script_encoding": "gbk",
            TOURNAMENT_KEY.lower(): "off",
            "unrelated": {"kept": True},
        }
        before = dict(instance)

        unchanged = worker.target_instance(instance, "image:next", None)
        self.assertEqual(
            unchanged, dict(instance, server_image="image:next")
        )
        self.assertEqual(instance, before)
        self.assertIsNot(unchanged, instance)

        updated = worker.target_instance(
            instance, "image:next", None, encoding="utf8"
        )
        self.assertEqual(updated["channel_script_encoding"], "utf8")
        self.assertEqual(updated["server_image"], "image:next")
        self.assertEqual(updated["unrelated"], {"kept": True})
        self.assertEqual(instance, before)

        with self.assertRaises(ValueError):
            worker.target_instance(instance, "image:next", None, encoding="UTF8")

    def test_target_environment_default_and_explicit_value_are_isolated(self):
        environment = {
            TOURNAMENT_KEY: "off",
            MULTIPLIER_KEY: "3",
            PERCENT_KEY: "15",
            CHANNEL_ENCODING_KEY: "gbk",
            "UNRELATED": "kept",
        }
        before = dict(environment)

        unchanged = worker.target_environment(environment, None)
        self.assertEqual(unchanged, environment)
        self.assertEqual(unchanged[CHANNEL_ENCODING_KEY], "gbk")
        self.assertEqual(environment, before)
        self.assertIsNot(unchanged, environment)

        updated = worker.target_environment(environment, None, encoding="utf8")
        self.assertEqual(
            updated,
            dict(environment, **{CHANNEL_ENCODING_KEY: "utf8"}),
        )
        self.assertEqual(environment, before)

        with self.assertRaises(ValueError):
            worker.target_environment(environment, None, encoding="latin1")

    def test_target_environment_appends_encoding_after_managed_keys(self):
        updated = worker.target_environment(
            {"UNRELATED": "kept"}, "vendor", 3, 15, encoding="gbk"
        )
        self.assertEqual(
            updated,
            {
                "UNRELATED": "kept",
                TOURNAMENT_KEY: "vendor",
                MULTIPLIER_KEY: "3",
                PERCENT_KEY: "15",
                CHANNEL_ENCODING_KEY: "gbk",
            },
        )
        self.assertEqual(
            list(updated),
            [
                "UNRELATED",
                TOURNAMENT_KEY,
                MULTIPLIER_KEY,
                PERCENT_KEY,
                CHANNEL_ENCODING_KEY,
            ],
        )

    def test_env_bytes_default_does_not_touch_existing_data(self):
        before = (
            b"RAW=\xff\r\n"
            + CHANNEL_ENCODING_KEY.encode()
            + b"=gbk\r\nTAIL-WITHOUT-NEWLINE"
        )
        self.assertEqual(worker.target_env_bytes(before, None), before)

        managed = (
            b"HEAD=\xff\r\n"
            + TOURNAMENT_KEY.encode()
            + b"=off\r\n"
            + CHANNEL_ENCODING_KEY.encode()
            + b"=gbk\r\n"
            + MULTIPLIER_KEY.encode()
            + b"=3\r\n"
            + b"TAIL=\x00\r\n"
        )
        self.assertEqual(
            worker.target_env_bytes(managed, "vendor"),
            managed.replace(
                TOURNAMENT_KEY.encode() + b"=off\r\n",
                TOURNAMENT_KEY.encode() + b"=vendor\n",
            ),
        )

    def test_env_bytes_appends_encoding_after_managed_keys(self):
        before = (
            TOURNAMENT_KEY.encode()
            + b"=off\n"
            + MULTIPLIER_KEY.encode()
            + b"=3\n"
            + PERCENT_KEY.encode()
            + b"=15\n"
        )
        self.assertEqual(
            worker.target_env_bytes(before, None, encoding="gbk"),
            before + CHANNEL_ENCODING_KEY.encode() + b"=gbk\n",
        )

    def test_env_bytes_updates_existing_key_and_preserves_other_bytes(self):
        before = (
            b"HEAD=\xff\r\n"
            + TOURNAMENT_KEY.encode()
            + b"=off\r\n"
            + CHANNEL_ENCODING_KEY.encode()
            + b"=gbk\r\n"
            + b"TAIL=\x00\r\n"
        )
        after = worker.target_env_bytes(before, None, encoding="utf8")
        self.assertEqual(
            after,
            before.replace(
                CHANNEL_ENCODING_KEY.encode() + b"=gbk\r\n",
                CHANNEL_ENCODING_KEY.encode() + b"=utf8\n",
            ),
        )

    def test_env_bytes_rejects_ambiguous_file_when_encoding_changes(self):
        duplicate = (
            CHANNEL_ENCODING_KEY.encode()
            + b"=utf8\n"
            + CHANNEL_ENCODING_KEY.encode()
            + b"=gbk\n"
        )
        without_newline = b"UNRELATED=kept"
        for before in (duplicate, without_newline):
            with self.subTest(before=before):
                with self.assertRaises(ValueError):
                    worker.target_env_bytes(before, None, encoding="gbk")

    def test_env_bytes_rejects_invalid_encoding(self):
        with self.assertRaises(ValueError):
            worker.target_env_bytes(b"OTHER=kept\n", None, encoding="UTF8")


class ChannelScriptEncodingSnapshot(unittest.TestCase):
    def test_snapshot_defaults_to_utf8_or_accepts_an_unset_runtime_value(self):
        cases = (
            ({}, {}, {}, None, None),
            (
                {},
                {CHANNEL_ENCODING_KEY: "utf8"},
                {CHANNEL_ENCODING_KEY: "utf8"},
                "utf8",
                "utf8",
            ),
        )
        for instance, container, process, container_value, process_value in cases:
            with self.subTest(container=container, process=process):
                self.assertEqual(
                    worker.channel_script_encoding_snapshot(
                        instance, container, process
                    ),
                    {
                        "sourceEncoding": None,
                        "effectiveEncoding": "utf8",
                        "containerValue": container_value,
                        "processValue": process_value,
                    },
                )

    def test_snapshot_explicit_encoding_requires_three_way_equality(self):
        for encoding in ("utf8", "gbk"):
            with self.subTest(encoding=encoding):
                self.assertEqual(
                    worker.channel_script_encoding_snapshot(
                        {"channel_script_encoding": encoding},
                        {CHANNEL_ENCODING_KEY: encoding},
                        {CHANNEL_ENCODING_KEY: encoding, "UNRELATED": "ignored"},
                    ),
                    {
                        "sourceEncoding": encoding,
                        "effectiveEncoding": encoding,
                        "containerValue": encoding,
                        "processValue": encoding,
                    },
                )

    def test_snapshot_rejects_runtime_disagreement_and_unsupported_defaults(self):
        cases = (
            ({}, {CHANNEL_ENCODING_KEY: "gbk"}, {CHANNEL_ENCODING_KEY: "gbk"}),
            ({}, {}, {CHANNEL_ENCODING_KEY: "utf8"}),
            (
                {"channel_script_encoding": "gbk"},
                {CHANNEL_ENCODING_KEY: "gbk"},
                {CHANNEL_ENCODING_KEY: "utf8"},
            ),
            (
                {"channel_script_encoding": "gbk"},
                {CHANNEL_ENCODING_KEY: "utf8"},
                {CHANNEL_ENCODING_KEY: "utf8"},
            ),
            ({"channel_script_encoding": "utf8"}, {}, {}),
        )
        for instance, container, process in cases:
            with self.subTest(instance=instance, container=container, process=process):
                with self.assertRaises((AssertionError, ValueError)):
                    worker.channel_script_encoding_snapshot(
                        instance, container, process
                    )

    def test_snapshot_rejects_invalid_source_value(self):
        for value in (None, "UTF8", "gb18030", "", 1, True):
            with self.subTest(value=repr(value)):
                with self.assertRaises(ValueError):
                    worker.channel_script_encoding_snapshot(
                        {"channel_script_encoding": value}, {}, {}
                    )


if __name__ == "__main__":
    unittest.main()
