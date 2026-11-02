"""Prepare rebuilds Linux without repeating source validation or PVF probes."""
import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import Mock, patch

spec = importlib.util.spec_from_file_location("build_only_update", Path(__file__).with_name("game_update.py"))
update = importlib.util.module_from_spec(spec)
spec.loader.exec_module(update)

class BuildOnlyTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.source = self.root / "source"
        self.source.mkdir()
        self.up = object.__new__(update.Update)
        self.up.stage = self.root / "stage"
        self.up.stage.mkdir()
        self.up.emergency = None
        self.up.profile = {"real_pvf_commands": [["unexpected-probe"]]}
        self.up.checkpoint = Mock()

    def invoke(self, states=None, failure=None):
        calls = []
        def run(argv, *args, **kwargs):
            calls.append(argv)
            if failure:
                raise subprocess.CalledProcessError(1, argv)
            return Mock(returncode=0)
        with patch.object(update, "source_state", side_effect=states or [dict(head="a", files={})]*2), \
             patch.object(update, "git", return_value="a"*40), \
             patch.object(update.subprocess, "run", side_effect=run):
            if hasattr(update, "cache"):
                with patch.object(update.cache, "timed_run", side_effect=run), \
                     patch.object(update.cache, "validation_checks", side_effect=AssertionError("duplicate validation")):
                    self.up.validate(self.source)
            else:
                self.up.validate(self.source)
        return calls

    def test_only_dependency_check_and_fresh_linux_server_build(self):
        calls = self.invoke()
        self.assertEqual(calls[:2], [["go", "version"], ["go", "mod", "verify"]])
        self.assertEqual(calls[-1][-3:], ["linux", "amd64", "server"])
        self.assertEqual(len(calls), 3)
        receipt = json.loads((self.up.stage / "validation.json").read_text())
        self.assertEqual(receipt["mode"], "linux-build-only")
        self.assertFalse(receipt["testsExecuted"])
        self.assertFalse(receipt["testEvidence"]["reused"])
        self.assertTrue(receipt["artifactsRebuilt"])

    def test_changed_source_rejects_receipt(self):
        with self.assertRaisesRegex(ValueError, "Source changed"):
            self.invoke(states=[{"head":"a","files":{}},{"head":"b","files":{}}])
        self.assertFalse((self.up.stage / "validation.json").exists())

    def test_dependency_failure_stops_without_success_receipt(self):
        with self.assertRaises(subprocess.CalledProcessError):
            self.invoke(failure=True)
        self.assertFalse((self.up.stage / "validation.json").exists())
