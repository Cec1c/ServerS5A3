"""Focused tests for the full Imperial Arena channel-directory migration."""
from pathlib import Path
import contextlib
import io
import json
import tempfile
import unittest

from migrate_empire_channel import CHANNEL_DUNGEON_IDS, CHANNEL_ID, main, migrate


def fixture(newline: bytes = b"\n") -> bytes:
    rows = []
    for channel_id in range(1, 119):
        rows.append(f"   {channel_id}   `channel_{channel_id}`   11   `[granfloris]`   5   0   0   ``".encode() + newline)
    return (b"[dungeon]" + newline + b"`[granfloris]`" + newline + b"[/dungeon]" + newline +
            b"[server] 1" + newline + b"".join(rows) + b"[/server]" + newline)


class EmpireChannelMigrationTests(unittest.TestCase):
    def test_preserves_every_existing_byte_and_appends_one_row(self):
        for newline in (b"\n", b"\r\n"):
            with self.subTest(newline=newline):
                source = fixture(newline)
                output = migrate(source)
                area = (b"[dungeon]" + newline + b"`[tagtournament]`" + newline +
                        "`帝国竞技场`".encode() + newline + b"320" + newline + b"336" + newline +
                        b"[/dungeon]" + newline + newline)
                row = f"   {CHANNEL_ID}   `帝国竞技场`   39   `[tagtournament]`   0.0   0.0   0.0   0.0   0.0   0.0   0.0   0.0   0.0   0.0   0.0   ``".encode() + newline
                self.assertEqual(output.replace(area, b"", 1).replace(row, b"", 1), source)
                self.assertEqual(output.count(b"`[tagtournament]`"), 2)

    def test_refuses_occupied_or_existing_empire_rows(self):
        source = fixture()
        occupied = source.replace(b"   118   `channel_118`", b"   187   `channel_118`", 1)
        tagged = source.replace(b"`[granfloris]`", b"`[tagtournament]`", 1)
        typed = source.replace(b"   118   `channel_118`   11", b"   118   `channel_118`   39", 1)
        for invalid in (occupied, tagged, typed):
            with self.subTest(), self.assertRaises(ValueError):
                migrate(invalid)

    def test_refuses_wrong_row_count_duplicates_and_packed_rows(self):
        source = fixture()
        duplicate = source.replace(b"   118   `channel_118`", b"   117   `channel_118`", 1)
        packed = source.replace(b"   118   `channel_118`   11", b"   118 `channel_118` 11 119 `other` 11", 1)
        for invalid in (source.replace(b"   1   `channel_1`   11   `[granfloris]`   5   0   0   ``\n", b"", 1),
                        duplicate, packed):
            with self.subTest(), self.assertRaises(ValueError):
                migrate(invalid)

    def test_cli_receipt_and_refuses_overwrite(self):
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory) / "input.etc"
            output = Path(directory) / "output.etc"
            source.write_bytes(fixture())
            stdout = io.StringIO()
            with contextlib.redirect_stdout(stdout):
                main([str(source), str(output)])
            receipt = json.loads(stdout.getvalue())
            self.assertEqual(receipt["base_rows"], 118)
            self.assertEqual(receipt["output_rows"], 119)
            self.assertEqual(receipt["dungeon_ids"], list(CHANNEL_DUNGEON_IDS))
            self.assertEqual(output.read_bytes(), migrate(source.read_bytes()))
            old = output.read_bytes()
            with contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit) as result:
                main([str(source), str(output)])
            self.assertEqual(result.exception.code, 2)
            self.assertEqual(output.read_bytes(), old)


if __name__ == "__main__":
    unittest.main()
