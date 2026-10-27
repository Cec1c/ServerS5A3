"""Tests for the explicit Imperial Arena deployment preflight."""
import contextlib
import io
import json
from pathlib import Path
import tempfile
import unittest

from check_empire_channel import main, validate
from migrate_empire_channel import CHANNEL_ID, CHANNEL_PORT, migrate
from test_migrate_empire_channel import fixture


class EmpireChannelPreflightTests(unittest.TestCase):
    def test_accepts_only_exact_reviewed_candidate(self):
        base = fixture()
        candidate = migrate(base)
        result = validate(base, candidate)
        self.assertEqual(result["baseRows"], 118)
        self.assertEqual(result["candidateRows"], 119)
        self.assertEqual(result["channelId"], CHANNEL_ID)
        self.assertEqual(result["port"], CHANNEL_PORT)
        self.assertFalse(result["startupRequirement"])

    def test_rejects_unchanged_or_tampered_candidate(self):
        base = fixture()
        candidate = migrate(base)
        tampered = candidate.replace(b"`\xe5\xb8\x9d\xe5\x9b\xbd\xe7\xab\x9e\xe6\x8a\x80\xe5\x9c\xba`   39", b"`other`   39", 1)
        for invalid in (base, tampered):
            with self.subTest(), self.assertRaises(ValueError):
                validate(base, invalid)

    def test_cli_receipt(self):
        with tempfile.TemporaryDirectory() as directory:
            base_path = Path(directory) / "base.etc"
            candidate_path = Path(directory) / "candidate.etc"
            base_path.write_bytes(fixture())
            candidate_path.write_bytes(migrate(base_path.read_bytes()))
            stdout = io.StringIO()
            with contextlib.redirect_stdout(stdout):
                main([str(base_path), str(candidate_path)])
            receipt = json.loads(stdout.getvalue())
            self.assertEqual(receipt["candidateRows"], 119)
            self.assertFalse(receipt["startupRequirement"])


if __name__ == "__main__":
    unittest.main()
