import unittest
from verification_scope import classify

class ScopeTests(unittest.TestCase):
    def test_notes_do_not_build(self):
        self.assertEqual(classify(["Docs/x.md", "changelog.d/x.fix.md"]), dict(game=False, tooling=False, container=False))
    def test_game_change_requires_linux_checks(self):
        self.assertTrue(classify(["go-server/internal/example.go"])["game"])
    def test_controller_change_does_not_repeat_game(self):
        self.assertEqual(classify(["deploy/windows/game_update.py"]), dict(game=False, tooling=True, container=False))
    def test_ci_change_checks_policy(self):
        self.assertEqual(classify([".github/workflows/verify.yml"]), dict(game=True, tooling=True, container=False))
    def test_container_and_manual(self):
        self.assertTrue(classify(["deploy/docker/Dockerfile"])["container"])
        self.assertTrue(all(classify([], full=True).values()))
