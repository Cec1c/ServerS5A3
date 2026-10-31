"""119-to-119 Imperial area mapping update contract."""
import unittest

from check_empire_channel_update import validate_update
from migrate_empire_channel import migrate
from test_migrate_empire_channel import fixture


class EmpireChannelUpdateTests(unittest.TestCase):
    def test_only_adds_dungeon_mappings_to_existing_directory(self):
        for newline in (b"\n", b"\r\n"):
            with self.subTest(newline=newline):
                candidate = migrate(fixture(newline))
                current = candidate.replace(b"320" + newline + b"336" + newline, b"", 1)
                receipt = validate_update(current, candidate)
                self.assertEqual(receipt["channels"], 119)
                self.assertEqual(receipt["dungeonIds"], (320, 336))

    def test_rejects_other_changes_and_repeat_application(self):
        candidate = migrate(fixture())
        current = candidate.replace(b"320\n336\n", b"", 1)
        for old, new in ((b"`channel_1`", b"`changed_1`"),
                         (b"187   `", b"188   `"),
                         (b"320\n336\n", b"320\n337\n")):
            with self.subTest(old=old), self.assertRaises(ValueError):
                validate_update(current, candidate.replace(old, new, 1))
        with self.assertRaises(ValueError):
            validate_update(candidate, candidate)


if __name__ == "__main__":
    unittest.main()
