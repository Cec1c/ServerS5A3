"""Only the reviewed 119-to-119 Imperial mapping profile is accepted."""
import hashlib
from pathlib import Path
import tempfile
import unittest

import game_update


def profile():
    return {"runtime_channel_update": {
        "source": "C:/candidate/channel.override.etc",
        "target": "runtime/docker/data/channel.override.etc",
        "container_path": "/runtime/data/dnf/channel.override.etc",
        "channel_file": "channel.override.etc",
        "base_channel_source": "external:approved-119-empire-arena-v1",
        "channel_source": "external:approved-119-empire-arena-pvfmap-v2",
        "base_sha256": "61adb6c78d69aca40f58dc79fa3ecd39570b32e82854305944c46faf332eafa1",
        "sha256": "d701a4d9619b761f91fdd9516fdf8d45a383daba264d947ffd319b8d94ff2748",
        "pvf_sha256": "94a2549598816198115340f84c0be2d31603ddeea58f70f27afe6f73b25e3df7",
        "base_channels": 119, "channels": 119, "channel_id": 187,
        "port": 10187, "channel_type": 39, "group": "tagtournament", "name": "帝国竞技场",
    }}


class ImperialMappingProfileTests(unittest.TestCase):
    def test_reviewed_transition_only(self):
        spec = profile()["runtime_channel_update"]
        self.assertEqual(game_update.checked_runtime_channel_profile(profile()), spec)
        for field, wrong in (("base_channels", 118), ("channels", 118),
                             ("base_channel_source", "external:approved-118-channel-table-u8-r131"),
                             ("channel_source", "external:approved-119-empire-arena-v1"),
                             ("port", 10188)):
            with self.subTest(field=field), self.assertRaises(ValueError):
                changed = profile()
                changed["runtime_channel_update"][field] = wrong
                game_update.checked_runtime_channel_profile(changed)

    def test_asset_gate_binds_candidate_live_source_and_pvf(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "candidate.etc"
            path.write_bytes(b"reviewed candidate")
            selected = profile()
            spec = selected["runtime_channel_update"]
            spec["source"] = str(path)
            spec["sha256"] = hashlib.sha256(path.read_bytes()).hexdigest()
            dummy = type("Driver", (), {"profile": selected})()
            snapshot = {"channelSha256": spec["base_sha256"], "pvfSha256": spec["pvf_sha256"],
                        "channel": {"channelSource": spec["base_channel_source"]}}
            result = game_update.Update.checked_channel_asset(dummy, snapshot)
            self.assertFalse(result["already_applied"])
            self.assertEqual(result["manifest"]["sha256"], spec["sha256"])
            snapshot["channel"]["channelSource"] = "wrong"
            with self.assertRaises(ValueError):
                game_update.Update.checked_channel_asset(dummy, snapshot)
            snapshot["channel"]["channelSource"] = spec["base_channel_source"]
            path.write_bytes(b"changed candidate")
            with self.assertRaises(ValueError):
                game_update.Update.checked_channel_asset(dummy, snapshot)


if __name__ == "__main__":
    unittest.main()
