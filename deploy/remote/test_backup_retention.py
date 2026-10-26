import gzip
import importlib.util
import json
import os
from pathlib import Path
import tempfile
import unittest
from datetime import datetime, timezone, timedelta

spec = importlib.util.spec_from_file_location("retention", Path(__file__).with_name("backup_retention.py"))
retention = importlib.util.module_from_spec(spec)
spec.loader.exec_module(retention)


class RetentionTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.runtime = Path(self.temp.name).resolve() / "runtime"
        self.backups = self.runtime / "backups"
        self.backups.mkdir(parents=True)
        self.current = self.backups / "current-backup"
        self.current.mkdir()
        (self.current / "config.json").write_text("current")
        (self.runtime / "active-game-baseline.json").write_text(json.dumps({
            "sourceCommit": "current", "backup": str(self.current), "gameHealthy": True}))
        self.now = datetime(2026, 10, 16, 12, tzinfo=timezone.utc)

    def dump(self, when, valid=True):
        path = self.backups / when.strftime("database-%Y%m%dT%H%M%SZ.sql.gz")
        with gzip.open(path, "wb") as f:
            f.write(b"-- MariaDB dump\nCREATE TABLE t (id int);" if valid else b"broken")
        return path

    def test_retention_keeps_recent_daily_weekly_and_active_recovery_points(self):
        paths = [self.dump(self.now - timedelta(hours=n)) for n in range(70)]
        ancient = self.dump(self.now - timedelta(days=50))
        active = json.loads((self.runtime / "active-game-baseline.json").read_text())
        active["databaseBackup"] = str(ancient)
        (self.runtime / "active-game-baseline.json").write_text(json.dumps(active))
        proposal = retention.plan(self.runtime, self.now)
        self.assertGreater(len(proposal["remove"]), 50)
        for p in paths[:3] + [ancient]: self.assertIn(p.name, proposal["kept"])
        result = retention.apply(self.runtime, proposal)
        self.assertTrue(result["retainedDatabaseBackupsVerified"])
        self.assertTrue(ancient.exists())
        self.assertEqual(retention.plan(self.runtime, self.now)["remove"], [])

    def test_only_exact_duplicate_legacy_directories_are_removed(self):
        same = self.backups / "older-copy"
        same.mkdir(); (same / "config.json").write_text("current")
        unique = self.backups / "unknown-legacy"
        unique.mkdir(); (unique / "config.json").write_text("unique")
        proposal = retention.plan(self.runtime, self.now)
        self.assertEqual([p["name"] for p in proposal["remove"]], [same.name])
        retention.apply(self.runtime, proposal)
        self.assertTrue(self.current.exists()); self.assertTrue(unique.exists())

    def test_changed_file_and_changed_active_receipt_block_deletion(self):
        for n in range(7): self.dump(self.now - timedelta(minutes=n))
        proposal = retention.plan(self.runtime, self.now)
        victim = self.backups / proposal["remove"][0]["name"]
        victim.write_bytes(b"concurrent update")
        with self.assertRaises(ValueError): retention.apply(self.runtime, proposal)
        self.assertTrue(victim.exists())
        proposal = retention.plan(self.runtime, self.now)
        active = self.runtime / "active-game-baseline.json"
        active.write_text(active.read_text() + "\n")
        with self.assertRaises(ValueError): retention.apply(self.runtime, proposal)

    def test_corrupt_retained_database_stops_all_deletions(self):
        self.dump(self.now, valid=False)
        for n in range(1, 7): self.dump(self.now - timedelta(minutes=n))
        proposal = retention.plan(self.runtime, self.now)
        with self.assertRaises(ValueError): retention.apply(self.runtime, proposal)
        self.assertTrue(all((self.backups / row["name"]).exists() for row in proposal["remove"]))

    def test_outside_and_symlink_targets_are_rejected(self):
        for n in range(7): self.dump(self.now - timedelta(minutes=n))
        proposal = retention.plan(self.runtime, self.now)
        proposal["remove"][0]["name"] = "../active-game-baseline.json"
        with self.assertRaises(ValueError): retention.apply(self.runtime, proposal)
        link = self.backups / "link"
        try: link.symlink_to(self.current, target_is_directory=True)
        except OSError: return  # Windows without symlink privilege.
        proposal = retention.plan(self.runtime, self.now)
        self.assertIn("link", [p["name"] for p in proposal["skipped"]])


if __name__ == "__main__": unittest.main()
