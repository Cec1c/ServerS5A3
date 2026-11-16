import tempfile
from pathlib import Path
import unittest
from unittest.mock import patch
import manage
import test_disjoint_epic_display as display_fixture


class TownDisplayConfig(unittest.TestCase):
    def test_only_server_environment_changes_with_strict_modes(self):
        helper=display_fixture.DisjointEpicDisplayConfig();config=helper.config()|{'disjoint_epic_display':'paired-v1'}
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp);before=helper.generated(root,config)
            for mode in ('off','on'):
                after=helper.generated(root,config|{'town_display_only':mode})
                self.assertEqual({p for p in before if before[p]!=after[p]},{'server.env'})
                self.assertEqual(after['server.env'],before['server.env']+b'DNFBRIDGE_TOWN_DISPLAY_ONLY='+mode.encode()+b'\n')
            for invalid in (None,True,1,[],{},'ON',' on','on\nEXTRA=1'):
                with self.subTest(value=invalid),patch.object(manage,'RUNTIME',root),self.assertRaises(ValueError):
                    manage.generate(config|{'town_display_only':invalid},{'ports':[10006]})


if __name__=='__main__':unittest.main()
