import gzip
import importlib.util
import io
from pathlib import Path
import tempfile
import unittest
from unittest.mock import Mock, patch

spec = importlib.util.spec_from_file_location("backup", Path(__file__).with_name("backup.py"))
backup = importlib.util.module_from_spec(spec)
spec.loader.exec_module(backup)


class BackupTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.runtime = Path(self.temp.name)
        self.argv = ["backup.py", "--runtime-dir", str(self.runtime), "--no-retention"]

    def test_complete_dump_is_published_and_failed_dump_stays_partial(self):
        process = Mock(stdout=io.BytesIO(b"-- MariaDB dump\nSELECT 1;"))
        process.wait.return_value = 0
        with patch("sys.argv", self.argv), patch.object(backup.subprocess, "Popen", return_value=process):
            backup.main()
        path, = (self.runtime / "backups").glob("*.gz")
        self.assertEqual(gzip.decompress(path.read_bytes()), b"-- MariaDB dump\nSELECT 1;")
        # A distinct temporary runtime models a failed later attempt.
        other = self.runtime / "failed"
        process.stdout = io.BytesIO(b"truncated"); process.wait.return_value = 1
        with patch("sys.argv", ["backup.py", "--runtime-dir", str(other), "--no-retention"]), patch.object(backup.subprocess, "Popen", return_value=process):
            with self.assertRaises(RuntimeError): backup.main()
        self.assertFalse(list((other / "backups").glob("*.gz")))
        self.assertEqual(len(list((other / "backups").glob("*.partial"))), 1)

    def test_concurrent_publication_cannot_overwrite_an_existing_recovery_point(self):
        process = Mock(stdout=io.BytesIO(b"-- MariaDB new dump"))
        process.wait.return_value = 0
        real_link = backup.os.link
        def collision(source, target):
            target.write_bytes(b"another complete recovery point")
            real_link(source, target)
        with patch("sys.argv", self.argv), patch.object(backup.subprocess, "Popen", return_value=process), patch.object(backup.os, "link", side_effect=collision):
            with self.assertRaises(FileExistsError): backup.main()
        path, = (self.runtime / "backups").glob("*.gz")
        self.assertEqual(path.read_bytes(), b"another complete recovery point")
        self.assertEqual(len(list((self.runtime / "backups").glob("*.partial"))), 1)


if __name__ == "__main__": unittest.main()
