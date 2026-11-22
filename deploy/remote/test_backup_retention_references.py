"""Recovery references and bounded historical snapshot retirement."""
import gzip
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from datetime import datetime, timezone, timedelta


spec = importlib.util.spec_from_file_location(
    "retention", Path(__file__).with_name("backup_retention.py"))
retention = importlib.util.module_from_spec(spec)
spec.loader.exec_module(retention)


class RecoveryReferenceTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self._default_mounts = retention._default_container_mount_sources
        retention._default_container_mount_sources = lambda: []
        self.addCleanup(
            setattr, retention, "_default_container_mount_sources",
            self._default_mounts)
        self.runtime = Path(self.temp.name).resolve() / "runtime"
        self.backups = self.runtime / "backups"
        self.updates = self.runtime / "game-updates"
        self.backups.mkdir(parents=True)
        self.updates.mkdir(parents=True)
        self.active = self.backups / "active-backup"
        self.active.mkdir()
        (self.active / "config.json").write_text("active")
        self.active_path = self.runtime / "active-game-baseline.json"
        self.active_path.write_text(json.dumps({
            "sourceCommit": "active",
            "backup": str(self.active),
            "image": "sha256:" + "2" * 64,
            "gameHealthy": True,
        }))
        self.now = datetime(2026, 11, 22, 12, tzinfo=timezone.utc)

    def backup(self, name, content="fixture"):
        path = self.backups / name
        path.mkdir()
        (path / "config.json").write_text(content)
        return path

    def update(self, name, backup, before_image=None, base_commit=None,
               healthy=True):
        path = self.updates / name
        path.mkdir()
        (path / "payload.bin").write_bytes(name.encode())
        receipt = {
            "gameHealthy": healthy,
            "sourceCommit": name,
            "backup": str(backup),
        }
        if before_image is not None:
            receipt["beforeImage"] = before_image
        if base_commit is not None:
            receipt["baseCommit"] = base_commit
        (path / "game-deployment.json").write_text(json.dumps(receipt))
        return path

    def test_pending_receipt_protects_existing_recovery_material(self):
        pending = self.updates / "pending"
        pending.mkdir()
        dump = self.backups / "database-20261120T000000Z.sql.gz"
        with gzip.open(dump, "wb") as stream:
            stream.write(b"-- MariaDB dump\n")
        snapshot = self.backup("game-update-pending")
        (pending / "backup-receipt.json").write_text(json.dumps({
            "backup": str(snapshot),
            "databaseBackup": str(dump),
        }))
        proposal = retention.plan(
            self.runtime, self.now, include_game_updates=True,
            max_game_update_remove=10)
        self.assertIn(snapshot.name, proposal["protected"])
        self.assertIn(dump.name, proposal["protected"])
        self.assertNotIn("pending", [row["name"] for row in
                                     proposal["retiredGameUpdates"]])

    def test_missing_pending_dump_needs_independent_supersession_proof(self):
        pending = self.updates / "historical-aborted"
        pending.mkdir()
        before = "sha256:" + "1" * 64
        (pending / "backup-receipt.json").write_text(json.dumps({
            "backup": str(self.backups / "missing-backup"),
            "databaseBackup": str(self.backups / "database-20260901T000000Z.sql.gz"),
            "beforeImage": before,
            "baseCommit": "retired-base",
        }))
        with self.assertRaisesRegex(ValueError, "Pending recovery material"):
            retention.plan(self.runtime, self.now)
        (pending / "prepared-game.json").write_text(json.dumps({
            "beforeImage": before,
        }))
        healthy = self.update(
            "healthy", self.active, before_image=before,
            base_commit="retired-base")
        (healthy / "game-deployment.json").write_text(json.dumps({
            "gameHealthy": True,
            "sourceCommit": "healthy",
            "backup": str(self.active),
            "beforeImage": before,
            "baseCommit": "retired-base",
            "image": "sha256:" + "3" * 64,
        }))
        proposal = retention.plan(self.runtime, self.now)
        self.assertEqual(
            {row["field"] for row in proposal["missingSupersededPending"]},
            {"backup", "databaseBackup"})

    def test_transitive_reference_from_protected_staging_keeps_backup(self):
        keep = self.backup("keep-me")
        staging = self.updates / "aborted"
        staging.mkdir()
        (staging / "game-rollback.json").write_text(json.dumps({
            "backup": str(keep),
        }))
        proposal = retention.plan(self.runtime, self.now)
        self.assertIn(keep.name, proposal["protected"])

    def test_active_source_directory_protects_its_update_snapshot(self):
        backup = self.backup("active-source-backup")
        update = self.update("active-source", backup)
        active = json.loads(self.active_path.read_text())
        active["sourceDirectory"] = str(update / "source")
        self.active_path.write_text(json.dumps(active))
        proposal = retention.plan(
            self.runtime, self.now, keep_deployments=1,
            include_game_updates=True, max_game_update_remove=10,
            mount_runner=lambda: [])
        self.assertNotIn("active-source", [row["name"] for row in
                                           proposal["retiredGameUpdates"]])

    def test_completed_history_retires_in_bounded_batches(self):
        for index in range(5):
            backup = self.backup("history-backup-" + str(index))
            self.update("history-" + str(index), backup)
        incomplete = self.updates / "incomplete"
        incomplete.mkdir()
        (incomplete / "prepared-game.json").write_text("{}")
        first = retention.plan(
            self.runtime, self.now, keep_deployments=1,
            include_game_updates=True, max_game_update_remove=2)
        self.assertEqual(len(first["retiredGameUpdates"]), 2)
        self.assertNotIn("incomplete", [row["name"] for row in
                                        first["retiredGameUpdates"]])
        retention.apply(self.runtime, first)
        second = retention.plan(
            self.runtime, self.now, keep_deployments=1,
            include_game_updates=True, max_game_update_remove=2)
        self.assertEqual(len(second["retiredGameUpdates"]), 2)
        self.assertTrue(incomplete.exists())

    def test_new_mount_after_planning_blocks_removal(self):
        backup = self.backup("history-backup")
        update = self.update("history", backup)
        proposal = retention.plan(
            self.runtime, self.now, keep_deployments=1,
            include_game_updates=True, max_game_update_remove=10,
            mount_runner=lambda: [])
        self.assertIn("history", [row["name"] for row in
                                  proposal["retiredGameUpdates"]])
        with self.assertRaisesRegex(ValueError, "protected"):
            retention.apply(
                self.runtime, proposal,
                mount_runner=lambda: [str(update / "runtime")])

    def test_changed_history_blocks_batch_removal(self):
        backup = self.backup("history-backup")
        update = self.update("history", backup)
        proposal = retention.plan(
            self.runtime, self.now, keep_deployments=1,
            include_game_updates=True, max_game_update_remove=10,
            mount_runner=lambda: [])
        (update / "payload.bin").write_bytes(b"changed")
        with self.assertRaisesRegex(ValueError, "changed"):
            retention.apply(
                self.runtime, proposal, mount_runner=lambda: [])

    def test_unknown_mount_inventory_blocks_apply_and_preserves_files(self):
        backup = self.backup("history-backup")
        update = self.update("history", backup)
        proposal = retention.plan(
            self.runtime, self.now, keep_deployments=1,
            include_game_updates=True, max_game_update_remove=10,
            mount_runner=lambda: [])

        def unavailable():
            raise RuntimeError("container mount inventory is unavailable")

        with self.assertRaisesRegex(RuntimeError, "mount inventory"):
            retention.apply(self.runtime, proposal, mount_runner=unavailable)
        self.assertTrue(update.exists())
        self.assertTrue(backup.exists())

    def test_one_complete_mount_snapshot_is_used_per_plan_and_apply(self):
        backup = self.backup("history-backup")
        self.update("history", backup)
        calls = []

        def mounts():
            calls.append(True)
            return []

        proposal = retention.plan(
            self.runtime, self.now, keep_deployments=1,
            include_game_updates=True, max_game_update_remove=10,
            mount_runner=mounts)
        self.assertEqual(len(calls), 1)
        retention.apply(self.runtime, proposal, mount_runner=mounts)
        self.assertEqual(len(calls), 2)

    def test_missing_docker_is_not_an_empty_mount_snapshot(self):
        original = self._default_mounts
        retention._default_container_mount_sources = original
        real_which = retention.shutil.which
        retention.shutil.which = lambda _: None
        try:
            with self.assertRaisesRegex(RuntimeError, "mount inventory"):
                retention._default_container_mount_sources()
        finally:
            retention.shutil.which = real_which
            retention._default_container_mount_sources = lambda: []

    def test_explicit_fixture_mounts_work_without_docker(self):
        original = self._default_mounts
        retention._default_container_mount_sources = original
        real_which = retention.shutil.which
        retention.shutil.which = lambda _: None
        try:
            backup = self.backup("fixture-backup")
            self.update("fixture-update", backup)
            calls = []

            def mounts():
                calls.append(True)
                return []

            proposal = retention.plan(
                self.runtime, self.now, keep_deployments=1,
                include_game_updates=True, max_game_update_remove=10,
                mount_runner=mounts)
            self.assertEqual(calls, [True])
            self.assertTrue(proposal["policy"]["includeGameUpdates"])
        finally:
            retention.shutil.which = real_which
            retention._default_container_mount_sources = lambda: []


if __name__ == "__main__":
    unittest.main()
