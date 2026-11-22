"""Verify deploy/templates/docker-instance.recommended.json merges cleanly with manage.py."""
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import manage


class RecommendedInstanceTemplateTests(unittest.TestCase):
    def test_recommended_template_renders_expected_runtime_environment(self):
        template_path = manage.ROOT / "deploy" / "templates" / "docker-instance.recommended.json"
        preset = json.loads(template_path.read_text(encoding="utf-8"))
        base = {
            "profile": "docker-local-token-v1",
            "host_ip": "192.168.1.50",
            "subnet": "10.253.90.0/24",
            "server_ip": "10.253.90.2",
            "database_ip": "10.253.90.3",
            "database_password": "ab" * 24,
            "root_password": "cd" * 24,
            "admin_token": "ef" * 32,
            "joust_event_enabled": True,
            "client_dir": "",
            "body_codec": "plaintext",
            "client_body_codec": "plaintext",
            "default_group_login_only": True,
            "client_version": "official-24.9",
            "buff_group_sync": "on",
        }
        self.assertEqual(preset["tag_buff_group_sync"], "auto")
        merged = base | preset
        merged["runtime_compatibility"] = base.get("runtime_compatibility", {}) | preset.get("runtime_compatibility", {})
        with tempfile.TemporaryDirectory() as directory, patch.object(manage, "RUNTIME", Path(directory)):
            manage.generate(merged, {"ports": [10006]})
            env = (Path(directory) / "server.env").read_text(encoding="utf-8")
        for expected in (
            "DNFBRIDGE_DUNGEON_TOURNAMENT=vendor\n",
            "DNFBRIDGE_JOUST_EVENT_ENABLED=false\n",
            "DNFBRIDGE_MILEAGE_SHOP_ENABLED=on\n",
            "DNFBRIDGE_BUFF_GROUP_SYNC=on\n",
            "DNFBRIDGE_BUFF_SWAP_REAPPLY=on\n",
            "DNFBRIDGE_DISJOINT_EPIC_DISPLAY=paired-v1\n",
            "DNFBRIDGE_TOWN_DISPLAY_ONLY=on\n",
            "DNFBRIDGE_EXPERIMENTAL_PEAK=on\n",
            "DNFBRIDGE_HELL_BLOCKED_SLOTS=on\n",
            "DNFBRIDGE_DUNGEON_ENTRY_SELECTION_MODE=allow\n",
            "DNFBRIDGE_GUILD_RANKING_ENABLED=true\n",
            "DNFBRIDGE_GUILD_HONGBAO_ENABLED=true\n",
            "DNFBRIDGE_MYSQL_INSERT_BATCH=200\n",
            "DNFBRIDGE_INVENTORY_SAVE_SCOPE=slot\n",
            "DNFBRIDGE_EQUIPMENT_SAVE_SCOPE=entry\n",
        ):
            self.assertIn(expected, env)

    def test_v215_formal_client_sha_is_recognized_for_buff_group_sync(self):
        v215_sha = "28e04d1d1c148c841318d533ed8ccb3e426ba5a5f24328b3c913b81303b12a84"
        self.assertEqual(manage.BUFF_GROUP_CLIENTS.get(v215_sha), "on")


if __name__ == "__main__":
    unittest.main()
