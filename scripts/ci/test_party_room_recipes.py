"""Guard the source prerequisites of the public party room recipes."""
from pathlib import Path
import subprocess
import sys
import unittest


RECIPES = Path(__file__).resolve().parents[1] / "client"


class PartyRoomRecipeTests(unittest.TestCase):
    def test_recovery_entry_imports_its_full_public_builder_chain(self):
        builder = RECIPES / "party-room-recovery" / "build_candidate.py"
        result = subprocess.run(
            [sys.executable, "-B", str(builder), "--help"],
            capture_output=True, text=True, timeout=15, check=False,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("--input", result.stdout)

    def test_lifecycle_verifier_is_shipped_with_the_builder(self):
        verifier = RECIPES / "party-room-lifecycle-registration" / "verify_candidate.py"
        compile(verifier.read_bytes(), str(verifier), "exec")


if __name__ == "__main__":
    unittest.main()
