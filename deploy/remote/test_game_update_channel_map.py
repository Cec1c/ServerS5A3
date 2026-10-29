"""The remote worker preserves all existing channel bytes and port identity."""
import unittest

import game_update


class ImperialMappingWorkerTests(unittest.TestCase):
    def test_only_two_area_ids_change(self):
        for newline in (b"\n", b"\r\n"):
            with self.subTest(newline=newline):
                old = (b"[dungeon]" + newline + b"`[tagtournament]`" + newline +
                       "`帝国竞技场`".encode() + newline + b"[/dungeon]" + newline +
                       b"[server] 1" + newline + b"187 `empire` 39 `[tagtournament]`" + newline + b"[/server]" + newline)
                candidate = old.replace(b"[/dungeon]" + newline, b"320" + newline + b"336" + newline + b"[/dungeon]" + newline)
                game_update.checked_empire_area_map_delta(old, candidate)
                with self.assertRaises(ValueError):
                    game_update.checked_empire_area_map_delta(old, candidate.replace(b"187", b"188"))
                with self.assertRaises(ValueError):
                    game_update.checked_empire_area_map_delta(candidate, candidate)

    def test_source_transition_and_rollback(self):
        spec = {"base_channel_source": "old", "channel_source": "new", "base_sha256": "a", "sha256": "b"}
        game_update.checked_runtime_channel_phase(spec, "old", "a", "prepare")
        game_update.checked_runtime_channel_phase(spec, "new", "b", "rollback")
        with self.assertRaises(ValueError):
            game_update.checked_runtime_channel_phase(spec, "old", "b", "deploy")
        old = {"server_image": "old", "channel_source": "external:approved-119-empire-arena-v1"}
        updated = game_update.target_instance(old, "new", None, channel_source="external:approved-119-empire-arena-pvfmap-v2")
        self.assertEqual(updated["channel_source"], "external:approved-119-empire-arena-pvfmap-v2")
        self.assertEqual(old["channel_source"], "external:approved-119-empire-arena-v1")


if __name__ == "__main__":
    unittest.main()
