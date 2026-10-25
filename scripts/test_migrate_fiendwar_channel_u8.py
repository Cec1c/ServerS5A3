"""Use the existing extracted channel table; mutations exercise refusal only."""
import contextlib
import hashlib
import io
import json
from pathlib import Path
import tempfile
import unittest

from migrate_fiendwar_channel_u8 import main, migrate


FIXTURE = Path(__file__).resolve().parents[1] / "go-server/internal/services/dnfbridge/testdata/channel_info.etc"


class FiendWarU8MigrationTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.original = FIXTURE.read_bytes()

    def test_real_table_changes_only_three_id_tokens(self):
        result = migrate(self.original)
        before, after = self.original.splitlines(keepends=True), result.splitlines(keepends=True)
        changed = [(a, b) for a, b in zip(before, after) if a != b]
        self.assertEqual(len(before), len(after))
        self.assertEqual(len(changed), 3)
        for (old, new), (a, b) in zip(((261, 244), (262, 245), (263, 246)), changed):
            self.assertEqual(a.replace(str(old).encode(), str(new).encode(), 1), b)

    def test_each_destination_collision(self):
        for target in (244, 245, 246):
            with self.subTest(target=target):
                data = self.original.replace(b"   261   `", f"   {target}   `".encode(), 1)
                with self.assertRaisesRegex(ValueError, "occupied"):
                    migrate(data)

    def test_each_source_missing(self):
        for source in (261, 262, 263):
            with self.subTest(source=source):
                data = self.original.replace(f"   {source}   `".encode(), b"   264   `", 1)
                with self.assertRaisesRegex(ValueError, "missing"):
                    migrate(data)

    def test_wrong_source_name_type_and_group(self):
        row = next(line for line in self.original.splitlines() if line.lstrip().startswith(b"261 "))
        for old, new in ((b"<chn_channel_info_048>", b"<chn_channel_info_047>"),
                         (b"   42   ", b"   45   "), (b"[fiendwar]", b"[raid]")):
            with self.subTest(field=old):
                self.assertIn(old, row)
                with self.assertRaisesRegex(ValueError, "name/type/group"):
                    migrate(self.original.replace(row, row.replace(old, new), 1))

    def test_duplicate_source_and_unrelated_id(self):
        for source in (261, 10):
            row = next(line for line in self.original.splitlines(keepends=True)
                       if line.lstrip().startswith(str(source).encode() + b" "))
            with self.subTest(source=source), self.assertRaisesRegex(ValueError, "duplicate"):
                migrate(self.original.replace(row, row + row, 1))

    def test_missing_duplicate_and_reversed_sections(self):
        for data in (self.original.replace(b"[/server]", b"", 1),
                     self.original + b"\n[server] 1\n[/server]\n",
                     b"[/server]\n" + self.original.replace(b"[/server]", b"", 1)):
            with self.subTest(), self.assertRaisesRegex(ValueError, "server section"):
                migrate(data)

    def test_malformed_unrelated_row_prevents_free_id_claim(self):
        with self.assertRaisesRegex(ValueError, "unrecognized"):
            migrate(self.original.replace(b"[/server]", b"244 malformed\n[/server]", 1))

    def test_repeated_migration_is_refused(self):
        with self.assertRaisesRegex(ValueError, "occupied"):
            migrate(migrate(self.original))

    def test_cli_receipt_and_refuse_overwrite_or_in_place(self):
        with tempfile.TemporaryDirectory() as directory:
            source, output = Path(directory) / "input.etc", Path(directory) / "output.etc"
            source.write_bytes(self.original)
            stdout = io.StringIO()
            with contextlib.redirect_stdout(stdout):
                main([str(source), str(output)])
            receipt = json.loads(stdout.getvalue())
            self.assertEqual(receipt["input_sha256"], hashlib.sha256(self.original).hexdigest())
            self.assertEqual(receipt["output_sha256"], hashlib.sha256(output.read_bytes()).hexdigest())
            self.assertEqual(output.read_bytes(), migrate(self.original))
            for target in (source, output):
                old = target.read_bytes()
                with contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit) as result:
                    main([str(source), str(target)])
                self.assertEqual(result.exception.code, 2)
                self.assertEqual(target.read_bytes(), old)

    def test_cli_invalid_input_creates_no_output(self):
        with tempfile.TemporaryDirectory() as directory:
            source, output = Path(directory) / "input.etc", Path(directory) / "output.etc"
            source.write_bytes(migrate(self.original))
            with contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit) as result:
                main([str(source), str(output)])
            self.assertEqual(result.exception.code, 2)
            self.assertFalse(output.exists())


if __name__ == "__main__":
    unittest.main()
