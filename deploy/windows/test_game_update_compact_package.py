import gzip
import hashlib
import json
from pathlib import Path
import subprocess
import tarfile
import tempfile
import unittest

from compact_game_package import package, tool_path, SCHEMA


class CompactPackageTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(); self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.source = self.root/"source"; self.source.mkdir()
        self.git("init", "-q")
        self.git("config", "user.email", "test@example.invalid")
        self.git("config", "user.name", "Test")
        for name, data in {"deploy/docker/manage.py": "# runtime\n", "deploy/docker/test_manage.py": "# tests\n",
                           "go-server/example_test.go": "// tests\n", "Docs/readme.md": "notes\n",
                           "launcher/test.cs": "// client\n"}.items():
            path = self.source/name; path.parent.mkdir(parents=True, exist_ok=True); path.write_text(data)
        self.git("add", "."); self.git("commit", "-qm", "base")
        self.base = self.git("rev-parse", "HEAD")
        self.build = self.root/"build"; self.build.mkdir()
        self.binary = b"\x7fELF\x02\x01" + bytes(12) + b"\x3e\x00" + b"program"*100
        (self.build/"DNF90Server").write_bytes(self.binary)
        self.receipt = self.build/"validation.json"
        self.record = dict(sourceCommit=self.base, sourceTree=self.git("rev-parse", "HEAD^{tree}"),
            passed=True, artifactsRebuilt=True, mode="docker-linux-local", image="golang:test",
            serverSha256=hashlib.sha256(self.binary).hexdigest(), commands=[])
        self.receipt.write_text(json.dumps(self.record))
        self.resources = {k: "a"*64 for k in ("exeSha256", "pvfSha256", "channelSha256")}

    def git(self, *args):
        return subprocess.check_output(["git", *args], cwd=self.source, stderr=subprocess.PIPE).decode().strip()

    def pack(self, name="payload", **overrides):
        args = dict(source=self.source, build_receipt=self.receipt, base=self.base,
                    resources=self.resources, output=self.root/name)
        args.update(overrides)
        return package(**args)

    def test_round_trip_contains_only_program_tools_and_manifest(self):
        result = self.pack()
        output = self.root/"payload"
        self.assertEqual({p.name for p in output.iterdir()}, {"DNF90Server.gz", "deploy-tools.tar.gz", "manifest.json"})
        self.assertEqual(gzip.decompress((output/"DNF90Server.gz").read_bytes()), self.binary)
        with tarfile.open(output/"deploy-tools.tar.gz") as archive:
            self.assertEqual(archive.getnames(), ["deploy/docker/manage.py"])
            for member in archive:
                self.assertEqual(hashlib.sha256(archive.extractfile(member).read()).hexdigest(), result["deployTools"][member.name])
        self.assertEqual(result["schema"], SCHEMA)
        self.assertFalse(result["sourceArchiveIncluded"])
        for name, identity in result["transport"].items():
            self.assertEqual(hashlib.sha256((output/name).read_bytes()).hexdigest(), identity["sha256"])

    def test_modified_program_does_not_produce_a_complete_manifest(self):
        (self.build/"DNF90Server").write_bytes(self.binary + b"tamper")
        with self.assertRaisesRegex(ValueError, "program differs"): self.pack()
        self.assertFalse((self.root/"payload/manifest.json").exists())

    def test_old_or_failed_build_receipt_is_rejected(self):
        for key, value in [("sourceCommit", "d"*40), ("sourceTree", "d"*40), ("passed", False), ("artifactsRebuilt", False)]:
            self.receipt.write_text(json.dumps(dict(self.record, **{key: value})))
            with self.assertRaisesRegex(ValueError, "exact source"): self.pack()

    def test_dirty_tree_is_rejected(self):
        (self.source/"deploy/docker/manage.py").write_text("changed")
        with self.assertRaisesRegex(ValueError, "clean"): self.pack()

    def test_unrelated_base_is_rejected(self):
        with self.assertRaises(subprocess.CalledProcessError): self.pack(base="d"*40)

    def test_paths_resources_and_existing_output_are_bounded(self):
        for name in ["../secrets", "deploy/../private.py", "deploy/docker/test_manage.py", "deploy/config.json", "/deploy/a.py"]:
            with self.assertRaises(ValueError): tool_path(self.source, name)
        with self.assertRaises(ValueError): self.pack(resources=dict(self.resources, token="secret"))
        self.pack()
        with self.assertRaises(FileExistsError): self.pack()


if __name__ == "__main__": unittest.main()
