"""The optional multiplier must not change unrelated generated configuration."""
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import manage


class MainQuestExperienceConfig(unittest.TestCase):
    def config(self):
        return {'host_ip': '192.168.1.50', 'server_ip': '10.254.90.2', 'database_ip': '10.254.90.3',
                'subnet': '10.254.90.0/24', 'database_password': 'fixture', 'root_password': 'fixture',
                'admin_token': 'ab' * 24, 'body_codec': 'plaintext', 'client_body_codec': 'plaintext',
                'joust_event_enabled': True}

    def generated(self, root, config):
        with patch.object(manage, 'RUNTIME', root): manage.generate(config, {'ports': [10006]})
        return {p.relative_to(root).as_posix(): p.read_bytes() for p in root.rglob('*') if p.is_file()}

    def test_optional_multiplier_changes_only_its_environment_key(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp); config = self.config(); before = self.generated(root, config)
            key = b'DNFBRIDGE_MAIN_QUEST_EXP_MULTIPLIER='
            self.assertNotIn(key, before['server.env'])
            for multiplier in (1, 3, 100):
                with self.subTest(multiplier=multiplier):
                    after = self.generated(root, config | {'main_quest_exp_multiplier': multiplier})
                    self.assertEqual({p for p in before if before[p] != after[p]}, {'server.env'})
                    self.assertEqual(after['server.env'], before['server.env'] + key + str(multiplier).encode() + b'\n')
            self.assertEqual(self.generated(root, config), before)

    def test_invalid_multiplier_rejects_before_writing(self):
        with tempfile.TemporaryDirectory() as temp, patch.object(manage, 'RUNTIME', Path(temp)):
            for multiplier in (None, 0, -1, 101, True, False, 1.0, 3.5, '3', '3\nOTHER=1', [], {}):
                with self.subTest(multiplier=multiplier), self.assertRaisesRegex(ValueError, 'main_quest_exp_multiplier'):
                    manage.generate(self.config() | {'main_quest_exp_multiplier': multiplier}, {'ports': [10006]})
            self.assertEqual(list(Path(temp).iterdir()), [])


if __name__ == '__main__': unittest.main()
