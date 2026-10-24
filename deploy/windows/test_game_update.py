"""Regression tests for deployment boundaries; all mutations use temporary repos."""
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import subprocess
import tarfile
import tempfile
import unittest
from unittest.mock import Mock, patch

spec = importlib.util.spec_from_file_location("game_update", Path(__file__).with_name("game_update.py"))
update = importlib.util.module_from_spec(spec)
spec.loader.exec_module(update)
spec = importlib.util.spec_from_file_location("worker", Path(__file__).parents[1] / "remote/game_update.py")
worker = importlib.util.module_from_spec(spec)
spec.loader.exec_module(worker)


class UpdateTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.source = self.root / "source"
        self.source.mkdir()
        self.git("init", "-q")
        self.git("config", "user.name", "Test")
        self.git("config", "user.email", "test@example.invalid")
        self.git("config", "commit.gpgsign", "false")
        self.git("config", "core.autocrlf", "false")
        (self.source / "go-server").mkdir()
        (self.source / "go-server/main.go").write_bytes(b"old\n")
        (self.source / "README.md").write_bytes(b"line\n")
        self.commit()
        self.base = self.git("rev-parse", "HEAD")
        authority = patch.object(update, 'AUTHORITATIVE_URL', str(self.source))
        authority.start(); self.addCleanup(authority.stop)
        profile = {"ssh_alias": "example", "remote_root": "/srv/example", "compose_project": "example",
                   "runtime_base_image": "sha256:" + "a" * 64, "probe_command": ["probe"]}
        self.up = update.Update(profile, "test")
        self.up.stage = self.root / "receipt"

    def git(self, *args):
        return update.git(self.source, *args)

    def commit(self):
        self.git("add", ".")
        self.git("commit", "-qm", "test")

    def candidate(self):
        (self.source / "go-server/main.go").write_bytes(b"new\n")
        self.commit()
        return self.git("rev-parse", "HEAD")

    def snapshot(self):
        return {"active": {"sourceCommit": self.base}}

    def reply(self, obj):
        return Mock(stdout=json.dumps(obj).encode())

    def test_scope_dirty_divergence_and_infrastructure_guards(self):
        self.candidate()
        self.assertEqual(update.checked_changes(self.source, self.base, ["go-server/main.go"]), {"go-server/main.go"})
        with self.assertRaises(ValueError): update.checked_changes(self.source, self.base, [])
        (self.source / "README.md").write_text("dirty")
        with self.assertRaises(ValueError): update.checked_changes(self.source, self.base, ["go-server/main.go"])
        self.commit()
        future = self.git("rev-parse", "HEAD")
        self.git("checkout", "--detach", self.base)
        with self.assertRaises(subprocess.CalledProcessError): update.checked_changes(self.source, future, [])
        (self.source / "build.bat").write_text("echo changed")
        self.commit()
        with self.assertRaises(ValueError): update.checked_changes(self.source, self.base, ["build.bat"])

    def test_rename_scope_includes_removed_and_added_archive_members(self):
        self.git("config", "diff.renames", "true")
        self.git("mv", "go-server/main.go", "go-server/renamed.go")
        self.commit()
        paths = {"go-server/main.go", "go-server/renamed.go"}
        self.assertEqual(update.checked_changes(self.source, self.base, sorted(paths)), paths)
        with self.assertRaises(ValueError):
            update.checked_changes(self.source, self.base, ["go-server/renamed.go"])
        old = self.root / "before.tar.gz"
        contents = {"README.md": b"line\n", "go-server/main.go": b"old\n"}
        with tarfile.open(old, "w:gz") as archive:
            for name, data in contents.items():
                member = tarfile.TarInfo(name); member.size = len(data)
                archive.addfile(member, io.BytesIO(data))
        result = update.write_archive(self.source, old,
            {name: hashlib.sha256(data).hexdigest() for name, data in contents.items()}, paths,
            self.root / "after.tar.gz")
        self.assertEqual(set(result), {"README.md", "go-server/renamed.go"})
        self.assertEqual(result["go-server/renamed.go"], hashlib.sha256(b"old\n").hexdigest())

    def test_same_commit_is_verified_without_restart_and_dirty_noop_is_rejected(self):
        self.up.worker = Mock(return_value=self.reply(self.snapshot()))
        self.up.compatibility = Mock()
        self.up.probe = Mock()
        self.up.validate = Mock()
        self.up.deploy(self.source, [])
        self.assertEqual([c.args[0] for c in self.up.worker.call_args_list], ["snapshot"])
        self.up.validate.assert_not_called()
        self.assertFalse(json.loads((self.up.stage / "no-op.json").read_text())["restarted"])
        (self.source / "README.md").write_text("dirty")
        with self.assertRaises(ValueError): self.up.deploy(self.source, [])
        self.assertEqual(self.up.worker.call_count, 1)

    def test_same_source_cannot_silently_ignore_a_configuration_change(self):
        self.up.profile['dungeon_tournament'] = 'vendor'
        self.up.worker = Mock(return_value=self.reply(self.snapshot()))
        self.up.compatibility = Mock()
        with self.assertRaisesRegex(ValueError, 'require a reviewed source update'):
            self.up.deploy(self.source, [])
        self.up.compatibility.assert_not_called()

    def test_profile_is_pinned_at_construction(self):
        profile = dict(self.up.profile, dungeon_tournament='vendor')
        up = update.Update(profile, 'pinned')
        profile['dungeon_tournament'] = 'off'
        self.assertEqual(up.profile['dungeon_tournament'], 'vendor')

    def test_same_source_requires_exact_quest_multiplier_source_match(self):
        for source_value in (None, 1, 3):
            up = update.Update(dict(self.up.profile, main_quest_exp_multiplier=3), 'same-exp')
            up.stage = self.root / ('receipt-' + str(source_value))
            up.worker = Mock(return_value=self.reply(self.snapshot() | {'mainQuestExperience': {'sourceMultiplier': source_value}}))
            up.compatibility = Mock(); up.probe = Mock(); up.validate = Mock()
            if source_value == 3:
                up.deploy(self.source, [])
                self.assertTrue((up.stage / 'no-op.json').exists())
            else:
                with self.assertRaisesRegex(ValueError, 'require a reviewed source update'):
                    up.deploy(self.source, [])
                up.compatibility.assert_not_called()
            up.validate.assert_not_called()
            self.assertEqual([c.args[0] for c in up.worker.call_args_list], ['snapshot'])

    def test_manifest_freezes_requested_quest_multiplier_and_omits_unspecified(self):
        self.candidate()
        archive = self.root / 'old-source.tar.gz'
        files = {'go-server/main.go': b'old\n', 'README.md': b'line\n'}
        with tarfile.open(archive, 'w:gz') as tar:
            for name, data in files.items():
                member = tarfile.TarInfo(name); member.size = len(data)
                tar.addfile(member, io.BytesIO(data))
        (self.source / '.git/info/exclude').write_text('dist/\n')
        binary = self.source / 'dist/linux-amd64/DNF90Server'
        binary.parent.mkdir(parents=True); binary.write_bytes(b'fixture-binary')
        for value in (None, 1, 3, 100):
            profile = dict(self.up.profile)
            if value is not None: profile['main_quest_exp_multiplier'] = value
            up = update.Update(profile, 'package-exp')
            if value is not None: profile['main_quest_exp_multiplier'] = 2
            up.stage = self.root / ('package-' + str(value)); up.stage.mkdir()
            up.authority = update.AuthorityGate(up.stage / 'authority', up.stage, up.run_id)
            up.authority.check(self.source, 'package')
            up.ssh = Mock(return_value=Mock(stdout=archive.read_bytes()))
            snapshot = {'sourceManifest': {'files': {name: hashlib.sha256(data).hexdigest() for name, data in files.items()}},
                        'active': {'sourceCommit': self.base, 'sourceDirectory': '/example/run/source',
                                   'sourceArchiveSha256': update.digest(archive)}}
            up.package(self.source, snapshot, {'go-server/main.go'})
            manifest = json.loads((up.stage / 'game-source-manifest.json').read_text())
            bundle = up.stage / 'game-source.bundle'
            self.assertEqual(update.digest(bundle), manifest['sourceGitBundleSha256'])
            self.assertEqual(update.git(self.source, 'bundle', 'list-heads', str(bundle)).split()[0], manifest['sourceCommit'])
            if value is None: self.assertNotIn('mainQuestExpMultiplier', manifest)
            else: self.assertEqual(manifest['mainQuestExpMultiplier'], value)

    def test_same_source_requires_exact_quest_percent_source_match(self):
        for source_value in (None, 1, 3):
            up = update.Update(dict(self.up.profile, main_quest_exp_percent=3), 'same-percent')
            up.stage = self.root / ('receipt-' + str(source_value))
            up.worker = Mock(return_value=self.reply(self.snapshot() | {'mainQuestExperience': {'sourcePercent': source_value}}))
            up.compatibility = Mock(); up.probe = Mock(); up.validate = Mock()
            if source_value == 3:
                up.deploy(self.source, [])
                self.assertTrue((up.stage / 'no-op.json').exists())
            else:
                with self.assertRaisesRegex(ValueError, 'require a reviewed source update'):
                    up.deploy(self.source, [])
                up.compatibility.assert_not_called()
            up.validate.assert_not_called()
            self.assertEqual([c.args[0] for c in up.worker.call_args_list], ['snapshot'])

    def test_manifest_freezes_requested_quest_percent_and_omits_unspecified(self):
        self.candidate()
        archive = self.root / 'old-source.tar.gz'
        files = {'go-server/main.go': b'old\n', 'README.md': b'line\n'}
        with tarfile.open(archive, 'w:gz') as tar:
            for name, data in files.items():
                member = tarfile.TarInfo(name); member.size = len(data)
                tar.addfile(member, io.BytesIO(data))
        (self.source / '.git/info/exclude').write_text('dist/\n')
        binary = self.source / 'dist/linux-amd64/DNF90Server'
        binary.parent.mkdir(parents=True); binary.write_bytes(b'fixture-binary')
        for value in (None, 0, 15, 20, 100):
            profile = dict(self.up.profile)
            if value is not None: profile['main_quest_exp_percent'] = value
            up = update.Update(profile, 'package-percent')
            if value is not None: profile['main_quest_exp_percent'] = 2
            up.stage = self.root / ('package-' + str(value)); up.stage.mkdir()
            up.authority = update.AuthorityGate(up.stage / 'authority', up.stage, up.run_id)
            up.authority.check(self.source, 'package')
            up.ssh = Mock(return_value=Mock(stdout=archive.read_bytes()))
            snapshot = {'sourceManifest': {'files': {name: hashlib.sha256(data).hexdigest() for name, data in files.items()}},
                        'active': {'sourceCommit': self.base, 'sourceDirectory': '/example/run/source',
                                   'sourceArchiveSha256': update.digest(archive)}}
            up.package(self.source, snapshot, {'go-server/main.go'})
            manifest = json.loads((up.stage / 'game-source-manifest.json').read_text())
            if value is None: self.assertNotIn('mainQuestExpPercent', manifest)
            else: self.assertEqual(manifest['mainQuestExpPercent'], value)

    def test_candidate_moved_during_validation_blocks_upload(self):
        self.candidate()
        self.up.worker = Mock(return_value=self.reply(self.snapshot()))
        self.up.compatibility = Mock()
        self.up.probe = Mock()
        def move(_):
            (self.source / "go-server/main.go").write_bytes(b"another clean commit\n")
            self.commit()
        self.up.validate = move
        self.up.package = Mock()
        with self.assertRaisesRegex(ValueError, "changed during validation"):
            self.up.deploy(self.source, ["go-server/main.go"])
        self.up.package.assert_not_called()

    def test_archive_preserves_deployed_crlf_and_checks_every_member(self):
        self.candidate()
        old = self.root / "old.tar.gz"
        contents = {"README.md": b"line\r\n", "go-server/main.go": b"old\n"}
        with tarfile.open(old, "w:gz") as archive:
            for name, data in contents.items():
                member = tarfile.TarInfo(name); member.size = len(data)
                archive.addfile(member, io.BytesIO(data))
        hashes = {k: hashlib.sha256(v).hexdigest() for k, v in contents.items()}
        output = self.root / "new.tar.gz"
        result = update.write_archive(self.source, old, hashes, {"go-server/main.go"}, output)
        self.assertEqual(result["README.md"], hashes["README.md"])
        with tarfile.open(output) as archive:
            self.assertEqual(archive.extractfile("README.md").read(), b"line\r\n")
        hashes["README.md"] = "0" * 64
        with self.assertRaisesRegex(ValueError, "hash mismatch"):
            update.write_archive(self.source, old, hashes, {"go-server/main.go"}, output)

    def test_archive_rejects_traversal_links_and_duplicate_members(self):
        for names, link in [(["../escape"], False), (["safe", "safe"], False), (["safe"], True)]:
            stream = io.BytesIO()
            with tarfile.open(fileobj=stream, mode="w") as archive:
                for name in names:
                    member = tarfile.TarInfo(name)
                    if link: member.type = tarfile.SYMTYPE; member.linkname = "/etc/passwd"
                    archive.addfile(member)
            stream.seek(0)
            with tarfile.open(fileobj=stream) as archive:
                with self.assertRaises(ValueError): worker.archive_members(archive, {"safe": "hash"})

    def test_failed_post_probe_rolls_back_once_without_redeploy(self):
        self.up.stage.mkdir()
        self.up.worker = Mock(return_value=self.reply({"active": {"sourceCommit": "new"}}))
        self.up.compatibility = Mock()
        self.up.probe = Mock(side_effect=[RuntimeError("channel failed"), None])
        with self.assertRaisesRegex(RuntimeError, "channel failed"):
            self.up.switch_and_check("new", "old")
        self.assertEqual([c.args[0] for c in self.up.worker.call_args_list], ["deploy", "verify", "rollback"])

    def test_lost_ssh_response_reconciles_success_without_second_deploy(self):
        self.up.stage.mkdir()
        verified = {"active": {"sourceCommit": "new"}}
        self.up.worker = Mock(side_effect=[subprocess.CalledProcessError(255, "ssh"), self.reply(verified), self.reply(verified)])
        self.up.compatibility = Mock(); self.up.probe = Mock()
        self.up.ssh = Mock(return_value=self.reply(verified))
        self.up.switch_and_check("new", "old")
        self.assertEqual([c.args[0] for c in self.up.worker.call_args_list], ["deploy", "verify", "verify"])
        self.assertTrue((self.up.stage / "reconciled.json").exists())

    def test_cleanup_failure_does_not_roll_back_verified_deployment(self):
        self.candidate()
        self.up.worker = Mock(return_value=self.reply(self.snapshot()))
        self.up.compatibility = Mock(); self.up.probe = Mock(); self.up.validate = Mock()
        self.up.package = Mock(); self.up.upload = Mock(); self.up.switch_and_check = Mock()
        self.up.cleanup = Mock(side_effect=OSError("busy"))
        self.up.deploy(self.source, ["go-server/main.go"])
        self.up.switch_and_check.assert_called_once()
        self.assertTrue(json.loads((self.up.stage / "cleanup-deferred.json").read_text())["deploymentVerified"])
        self.assertNotIn("rollback", [c.args[0] for c in self.up.worker.call_args_list])

    def test_missing_latest_master_blocks_before_validate_package_upload(self):
        authority = self.root / 'actual-authority'
        self.git('clone', '-q', str(self.source), str(authority))
        self.candidate()
        (authority / 'README.md').write_text('new upstream')
        for key, value in [('user.name', 'Test'), ('user.email', 'test@example.invalid'), ('commit.gpgsign', 'false')]:
            update.git(authority, 'config', key, value)
        update.git(authority, 'add', '.')
        update.git(authority, 'commit', '-qm', 'upstream')
        self.up.worker = Mock(return_value=self.reply(self.snapshot()))
        for method in ('compatibility', 'probe', 'validate', 'package', 'upload', 'switch_and_check', 'cleanup'):
            setattr(self.up, method, Mock())
        with patch.object(update, 'AUTHORITATIVE_URL', str(authority)):
            with self.assertRaisesRegex(ValueError, 'does not contain'):
                self.up.deploy(self.source, ['go-server/main.go'])
        for method in ('validate', 'package', 'upload', 'switch_and_check'):
            getattr(self.up, method).assert_not_called()

    def test_recovery_requires_exact_bytes_and_honest_root_identity(self):
        files = update.source_state(self.source)['files']
        snapshot = {'active': {'sourceCommit': 'f' * 40, 'sourceArchiveSha256': 'a' * 64},
                    'sourceManifest': {'files': files}}
        self.candidate()
        receipt = update.checked_recovered_base(self.source, self.base, snapshot)
        self.assertFalse(receipt['gitAncestryRecovered'])
        self.assertEqual(receipt['originalDeployedSourceCommit'], 'f' * 40)
        snapshot['sourceManifest']['files']['README.md'] = '0' * 64
        with self.assertRaisesRegex(ValueError, 'every deployed source byte'):
            update.checked_recovered_base(self.source, self.base, snapshot)
        with self.assertRaisesRegex(ValueError, 'without fabricated parents'):
            update.checked_recovered_base(self.source, self.git('rev-parse', 'HEAD'), snapshot)

    def test_saved_proof_does_not_authorize_new_package_or_upload(self):
        self.up.stage.mkdir()
        (self.up.stage / 'authority-current.json').write_text('{"passed":true}')
        with self.assertRaisesRegex(ValueError, 'in-process'):
            self.up.package(self.source, self.snapshot(), set())
        with self.assertRaisesRegex(ValueError, 'in-process'):
            self.up.upload()


if __name__ == "__main__": unittest.main()
