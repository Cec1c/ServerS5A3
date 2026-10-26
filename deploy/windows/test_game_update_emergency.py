import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('recovery_controller', Path(__file__).with_name('game_update.py'))
update = importlib.util.module_from_spec(spec)
spec.loader.exec_module(update)


class ValidationReuseTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.source = self.root / 'source'
        self.source.mkdir()
        (self.source / 'go-server').mkdir()
        (self.source / 'go-server/example.go').write_bytes(b'validated-source')
        self.files = {'go-server/example.go': update.digest(self.source / 'go-server/example.go')}
        self.log = self.root / 'check.log'
        self.log.write_bytes(b'passed')
        self.results = self.root / 'validation.json'
        self.results.write_text(json.dumps([dict(stage=stage, exitCode=0, log=str(self.log))
            for stage in ['list', 'domain', 'bridge', 'control', 'vet', 'windows', 'linux']]))
        self.binary = self.root / 'release-server'
        self.binary.write_bytes(b'\x7fELFfixture')
        profile = dict(ssh_alias='example', remote_root='/srv/example', compose_project='example',
                       runtime_base_image='sha256:' + 'a' * 64, probe_command=['probe'])
        self.up = update.Update(profile, 'test')
        self.up.stage = self.root / 'receipt'
        self.up.stage.mkdir()
        self.up.emergency = dict(files=self.files, validatedSource=str(self.source),
            validationResults=str(self.results), candidateCommit='b' * 40, evidenceSha256='c' * 64,
            evidenceFiles=[dict(path=str(p), sha256=update.digest(p)) for p in [self.log, self.results]],
            linuxArtifact=dict(path=str(self.binary), sha256=update.digest(self.binary)))

    def validate(self, metadata='-tags=timetzdata\nGOOS=linux\nGOARCH=amd64'):
        with patch.object(update, 'source_state', return_value={'files': self.files}), \
             patch.object(update.subprocess, 'check_output', return_value=metadata):
            self.up.validate(self.source)

    def test_identical_source_evidence_and_release_artifact_reuse(self):
        self.validate()
        self.assertEqual((self.source / 'dist/linux-amd64/DNF90Server').read_bytes(), self.binary.read_bytes())
        self.assertTrue(json.loads((self.up.stage / 'validation.json').read_text())['reusedValidation'])

    def test_missing_or_changed_validation_evidence_is_rejected(self):
        self.up.emergency['evidenceFiles'] = [self.up.emergency['evidenceFiles'][0]]
        with self.assertRaisesRegex(ValueError, 'results are not pinned'):
            self.validate()
        self.log.write_bytes(b'changed')
        with self.assertRaisesRegex(ValueError, 'evidence changed'):
            self.validate()

    def test_changed_source_or_missing_timezone_build_tag_is_rejected(self):
        with self.assertRaisesRegex(ValueError, 'release build settings'):
            self.validate('GOOS=linux\nGOARCH=amd64')
        (self.source / 'go-server/example.go').write_bytes(b'not-validated')
        with self.assertRaisesRegex(ValueError, 'previously validated bytes'):
            self.validate()


if __name__ == '__main__':
    unittest.main()
