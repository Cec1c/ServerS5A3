import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
from local_verify import commands, docker_command, execute


class LocalVerificationTests(unittest.TestCase):
    def test_scope_is_required_and_bridge_is_explicit(self):
        for args in [dict(packages=[]), dict(packages=["./internal/services/dnfbridge"]),
                     dict(packages=["./internal/modules/dnf/inventory"], full=True),
                     dict(packages=["../../elsewhere"]), dict(packages=[], build_only=True, run="TestX")]:
            with self.assertRaises(ValueError): commands(**args)

    def test_targeted_cache_and_opt_in_freshness(self):
        scope = ["./internal/services/dnfbridge"]
        normal = dict(commands(scope, run="TestAuction"))
        fresh = dict(commands(scope, run="TestAuction", race=True, uncached=True))
        self.assertNotIn("-count=1", normal["targeted"])
        self.assertIn("-run TestAuction", normal["targeted"])
        self.assertIn("-race", fresh["targeted"])
        self.assertIn("-count=1", fresh["targeted"])
        self.assertIn("CGO_ENABLED=0 go build", normal["build"])
        self.assertEqual(set(dict(commands([], build_only=True))), {"modules", "build"})

    def test_container_uses_readonly_tree_and_separate_cache_volumes(self):
        with patch("local_verify.git", return_value="/git-input"):
            argv = docker_command(Path("/src"), Path("/output"), "golang:1.26.5", "dnf-local", "go test")
        self.assertIn(f"type=bind,source={Path('/src')},target=/source,readonly", argv)
        self.assertIn("type=volume,source=dnf-local-gocache,target=/root/.cache/go-build", argv)
        self.assertEqual(argv[-3:], ["sh", "-c", "go test"])
        self.assertFalse(any("wsl" in x.lower() or "kali" in x.lower() for x in argv))

    def test_zero_matches_cannot_be_attested(self):
        with tempfile.TemporaryDirectory() as tmp:
            output = Path(tmp)/"run"
            with patch("local_verify.source_identity", return_value=dict(head="a"*40, tree="b"*40, clean=True)), \
                 patch("local_verify.docker_command", return_value=["unused"]), \
                 patch("local_verify.subprocess.Popen") as popen:
                popen.return_value.stdout = iter([json.dumps(dict(Action="pass", Package="example"))+"\n"])
                popen.return_value.wait.return_value = 0
                with self.assertRaisesRegex(ValueError, "no tests matched"):
                    execute(Path(tmp), output, [("targeted", "go test")], "go", "dnf-local")
            self.assertFalse(json.loads((output/"validation.json").read_text())["passed"])

    def test_source_change_during_build_invalidates_receipt(self):
        with tempfile.TemporaryDirectory() as tmp:
            output = Path(tmp)/"run"
            states = [dict(head="a"*40, tree="b"*40, clean=True), dict(head="a"*40, tree="b"*40, clean=False)]
            with patch("local_verify.source_identity", side_effect=states), \
                 patch("local_verify.docker_command", return_value=["unused"]), \
                 patch("local_verify.subprocess.Popen") as popen:
                popen.return_value.stdout = iter([]); popen.return_value.wait.return_value = 0
                with self.assertRaisesRegex(ValueError, "source changed"):
                    execute(Path(tmp), output, [("build", "go build")], "go", "dnf-local")
            result = json.loads((output/"validation.json").read_text())
            self.assertFalse(result["passed"])
            self.assertFalse(result["testsExecuted"])


if __name__ == "__main__": unittest.main()
