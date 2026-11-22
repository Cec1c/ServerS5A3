"""Optional redo/flush tuning planning, readback and exact rollback."""
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest


spec = importlib.util.spec_from_file_location(
    "database_tuning", Path(__file__).with_name("database_tuning.py"))
tuning = importlib.util.module_from_spec(spec)
spec.loader.exec_module(tuning)


class FakeHost:
    project = "fixture"

    def __init__(self):
        self.database = "fixture-db-1"
        self.game = "fixture-server-1"
        self.option = None
        self.variable_state = {
            "version": "11.8.9-MariaDB",
            "redoLogBytes": 96 * tuning.MIB,
            "flushNeighbors": 1,
            "flushAtCommit": 1,
            "poolBytes": 2 * tuning.GIB,
        }
        self.identity = {
            "id": "database",
            "image": "sha256:" + "a" * 64,
            "started": "2026-11-22T00:00:00Z",
            "restarts": 0,
            "volume": "database",
        }
        self.default_state = {
            "redoLogOptions": [],
            "flushNeighborsOptions": [],
        }
        self.free_bytes = 8 * tuning.GIB
        self.game_running = False
        self.fail_flush = False
        self.actions = []
        self.native_args = []

    def inspect(self, name):
        if name.endswith("-server-1"):
            return {"State": {"Running": self.game_running}}
        return {
            "Id": self.identity["id"],
            "Image": self.identity["image"],
            "RestartCount": self.identity["restarts"],
            "Config": {"Cmd": list(self.native_args)},
            "State": {
                "Running": True,
                "Paused": False,
                "StartedAt": self.identity["started"],
            },
            "Mounts": [{
                "Destination": "/var/lib/mysql",
                "Type": "volume",
                "Name": self.identity["volume"],
                "Source": str(Path.cwd()),
                "RW": True,
            }],
        }

    def data_mount(self):
        return {
            "Destination": "/var/lib/mysql",
            "Type": "volume",
            "Name": self.identity["volume"],
            "Source": str(Path.cwd()),
            "RW": True,
        }

    def variables_state(self):
        return dict(self.variable_state)

    def defaults_state(self):
        return {
            "redoLogOptions": list(self.default_state["redoLogOptions"]),
            "flushNeighborsOptions": list(self.default_state["flushNeighborsOptions"]),
        }

    def variables(self):
        return self.variables_state()

    def defaults(self):
        return self.defaults_state()

    def read_option(self):
        return self.option

    def _refresh_defaults(self):
        values = tuning.parse_option_bytes(self.option)
        self.default_state = {
            "redoLogOptions": ([values["redoLogBytes"]]
                               if "redoLogBytes" in values else []),
            "flushNeighborsOptions": ([values["flushNeighbors"]]
                                      if "flushNeighbors" in values else []),
        }

    def install_option(self, data):
        self.actions.append(("install", data))
        self.option = data
        self._refresh_defaults()

    def remove_option(self, expected):
        if self.option != expected:
            raise tuning.TuningError("fixture option differs")
        self.actions.append(("remove", expected))
        self.option = None
        self._refresh_defaults()

    def check_redo_space(self, size):
        self.actions.append(("space", size))
        if self.free_bytes < size + 512 * tuning.MIB:
            raise tuning.TuningError("fixture disk is too small")
        return self.free_bytes

    def disk_free(self):
        return self.free_bytes

    def game_stopped(self):
        if self.game_running:
            raise tuning.TuningError(
                "game must be stopped before database tuning is applied")

    def set_redo_log_size(self, size, allow_unverified_one_gib=False):
        tuning.checked_redo(size, allow_unverified_one_gib)
        self.check_redo_space(size)
        self.actions.append(("redo", size))
        self.variable_state["redoLogBytes"] = size

    def set_flush_neighbors(self, value):
        if self.fail_flush:
            self.actions.append(("flush-failed", value))
            raise tuning.TuningError("fixture flush failure")
        tuning.checked_flush_neighbors(value)
        self.actions.append(("flush", value))
        self.variable_state["flushNeighbors"] = value


class DatabaseTuningTests(unittest.TestCase):
    def setUp(self):
        self.host = FakeHost()
        self.stage = Path(tempfile.mkdtemp()) / "stage"
        self.addCleanup(self._cleanup)

    def _cleanup(self):
        import shutil
        shutil.rmtree(self.stage.parent, ignore_errors=True)

    def test_absent_optional_fields_carry_current_values_without_mutation(self):
        proposal = tuning.plan(self.host)
        self.assertTrue(proposal["carried"])
        self.assertEqual(proposal["changes"], {})
        result = tuning.apply(self.host, proposal, self.stage)
        self.assertTrue(result["carried"])
        self.assertFalse(self.stage.exists())
        self.assertEqual(self.host.actions, [])

    def test_approved_redo_and_flush_apply_and_read_back(self):
        proposal = tuning.plan(self.host, 256 * tuning.MIB, 0)
        self.assertEqual(proposal["changes"], {
            "redoLogBytes": 256 * tuning.MIB,
            "flushNeighbors": 0,
        })
        result = tuning.apply(self.host, proposal, self.stage)
        self.assertTrue(result["durabilityPreserved"])
        self.assertEqual(self.host.variable_state["redoLogBytes"], 256 * tuning.MIB)
        self.assertEqual(self.host.variable_state["flushNeighbors"], 0)
        self.assertEqual(self.host.option, tuning.option_bytes({
            "redoLogBytes": 256 * tuning.MIB,
            "flushNeighbors": 0,
        }))
        self.assertIn(("redo", 256 * tuning.MIB), self.host.actions)
        self.assertIn(("flush", 0), self.host.actions)

    def test_same_values_persist_startup_file_without_runtime_writes(self):
        proposal = tuning.plan(self.host, 96 * tuning.MIB, 1)
        self.assertEqual(proposal["changes"], {})
        self.assertFalse(proposal["carried"])
        tuning.apply(self.host, proposal, self.stage)
        self.assertFalse([row for row in self.host.actions
                          if row[0] in ("redo", "flush")])
        self.assertIsNotNone(self.host.option)
        self.assertEqual(self.host.variable_state["redoLogBytes"], 96 * tuning.MIB)
        self.assertEqual(self.host.variable_state["flushNeighbors"], 1)

    def test_one_gib_redo_needs_explicit_flag_and_disk_space(self):
        with self.assertRaisesRegex(ValueError, "not qualified"):
            tuning.plan(self.host, tuning.GIB)
        self.host.free_bytes = tuning.GIB + 256 * tuning.MIB
        with self.assertRaisesRegex(tuning.TuningError, "disk"):
            tuning.plan(self.host, tuning.GIB, allow_unverified_one_gib=True)
        self.host.free_bytes = 4 * tuning.GIB
        proposal = tuning.plan(
            self.host, tuning.GIB, allow_unverified_one_gib=True)
        self.assertEqual(proposal["qualification"], "disk-space-check-only")
        self.assertEqual(proposal["changes"]["redoLogBytes"], tuning.GIB)

    def test_foreign_startup_source_is_not_overwritten(self):
        self.host.default_state["redoLogOptions"] = [256 * tuning.MIB]
        with self.assertRaisesRegex(tuning.TuningError, "outside"):
            tuning.plan(self.host, 512 * tuning.MIB)

    def test_command_line_override_of_owned_options_is_refused(self):
        for argument in ("--innodb-log-file-size=256M",
                         "--loose-innodb_flush_neighbors=0",
                         "--skip-innodb-flush-neighbors"):
            with self.subTest(argument=argument):
                self.host.native_args = [argument]
                with self.assertRaisesRegex(tuning.TuningError, "overrides"):
                    tuning.plan(self.host)

    def test_rollback_restores_runtime_and_option_file_exactly(self):
        self.host.install_option(tuning.option_bytes({
            "redoLogBytes": 96 * tuning.MIB,
            "flushNeighbors": 1,
        }))
        original = tuning.snapshot(self.host)
        proposal = tuning.plan(self.host, 256 * tuning.MIB, 0)
        tuning.apply(self.host, proposal, self.stage)
        self.assertNotEqual(tuning.snapshot(self.host), original)
        result = tuning.rollback(self.host, self.stage)
        self.assertTrue(result["originalDatabaseTuningRestored"])
        self.assertEqual(tuning.snapshot(self.host), original)
        self.assertTrue((self.stage / "rollback-complete.json").is_file())

    def test_rollback_removes_new_owned_file_when_baseline_had_none(self):
        original = tuning.snapshot(self.host)
        proposal = tuning.plan(self.host, 96 * tuning.MIB, 1)
        tuning.apply(self.host, proposal, self.stage)
        self.assertIsNotNone(self.host.option)
        tuning.rollback(self.host, self.stage)
        self.assertEqual(tuning.snapshot(self.host), original)
        self.assertIsNone(self.host.option)

    def test_concurrent_apply_is_refused_by_lock(self):
        proposal = tuning.plan(self.host, 256 * tuning.MIB)
        lock_path = self.stage.parent / "tuning.lock"
        with tuning.FileLock(lock_path):
            with self.assertRaisesRegex(tuning.TuningError, "another"):
                tuning.apply(
                    self.host, proposal, self.stage, lock_path=lock_path)
        self.assertFalse(self.stage.exists())
        self.assertEqual(self.host.variable_state["redoLogBytes"],
                         96 * tuning.MIB)

    def test_completed_rollback_is_returned_without_replay(self):
        self.host.install_option(tuning.option_bytes({
            "redoLogBytes": 96 * tuning.MIB,
            "flushNeighbors": 1,
        }))
        proposal = tuning.plan(self.host, 256 * tuning.MIB, 0)
        tuning.apply(self.host, proposal, self.stage)
        first = tuning.rollback(self.host, self.stage)
        original = tuning.snapshot(self.host)
        second = tuning.rollback(self.host, self.stage)
        self.assertEqual(first, second)
        self.assertEqual(tuning.snapshot(self.host), original)
        self.assertTrue((self.stage / "rollback-complete.json").is_file())

    def test_completed_rollback_detects_later_drift(self):
        self.host.install_option(tuning.option_bytes({
            "redoLogBytes": 96 * tuning.MIB,
            "flushNeighbors": 1,
        }))
        proposal = tuning.plan(self.host, 256 * tuning.MIB, 0)
        tuning.apply(self.host, proposal, self.stage)
        tuning.rollback(self.host, self.stage)
        self.host.variable_state["redoLogBytes"] = 512 * tuning.MIB
        with self.assertRaisesRegex(tuning.TuningError, "changed after"):
            tuning.rollback(self.host, self.stage)

    def test_started_rollback_is_not_replayed(self):
        self.host.install_option(tuning.option_bytes({
            "redoLogBytes": 96 * tuning.MIB,
            "flushNeighbors": 1,
        }))
        proposal = tuning.plan(self.host, 256 * tuning.MIB, 0)
        tuning.apply(self.host, proposal, self.stage)
        target = tuning.snapshot(self.host)
        (self.stage / "rollback-started.json").write_text(
            json.dumps({"step": "rollback-started"}))
        with self.assertRaisesRegex(tuning.TuningError, "already started"):
            tuning.rollback(self.host, self.stage)
        self.assertEqual(tuning.snapshot(self.host), target)

    def test_failure_after_redo_is_diagnosed_and_rollback_restores(self):
        self.host.install_option(tuning.option_bytes({
            "redoLogBytes": 96 * tuning.MIB,
            "flushNeighbors": 1,
        }))
        original = tuning.snapshot(self.host)
        proposal = tuning.plan(self.host, 256 * tuning.MIB, 0)
        self.host.fail_flush = True
        with self.assertRaisesRegex(tuning.TuningError, "fixture flush"):
            tuning.apply(self.host, proposal, self.stage)
        diagnostics = json.loads((self.stage / "failure.json").read_text())
        self.assertEqual(diagnostics["stage"], "flush-started")
        self.assertEqual(diagnostics["error"], "TuningError")
        self.host.fail_flush = False
        tuning.rollback(self.host, self.stage)
        self.assertEqual(tuning.snapshot(self.host), original)

    def test_apply_requires_stopped_game(self):
        self.host.game_running = True
        proposal = tuning.plan(self.host, 256 * tuning.MIB)
        with self.assertRaisesRegex(tuning.TuningError, "game must be stopped"):
            tuning.apply(self.host, proposal, self.stage)
        self.assertFalse(self.stage.exists())
        self.assertEqual(self.host.variable_state["redoLogBytes"], 96 * tuning.MIB)

    def test_rollback_refuses_foreign_runtime_change(self):
        self.host.install_option(tuning.option_bytes({
            "redoLogBytes": 96 * tuning.MIB,
            "flushNeighbors": 1,
        }))
        proposal = tuning.plan(self.host, 256 * tuning.MIB)
        tuning.apply(self.host, proposal, self.stage)
        self.host.variable_state["redoLogBytes"] = 512 * tuning.MIB
        with self.assertRaisesRegex(tuning.TuningError, "outside"):
            tuning.rollback(self.host, self.stage)

    def test_owned_option_parser_rejects_unexpected_keys(self):
        with self.assertRaisesRegex(ValueError, "unexpected"):
            tuning.parse_option_bytes(
                b"[mariadbd]\ninnodb_buffer_pool_size=1\n")
        with self.assertRaisesRegex(ValueError, "repeats"):
            tuning.parse_option_bytes(
                b"[mariadbd]\ninnodb_flush_neighbors=0\n"
                b"innodb_flush_neighbors=1\n")


if __name__ == "__main__":
    unittest.main()
