"""Real recursive title serialization and preserved legacy boundary behavior."""
import os
from pathlib import Path
import unittest
import patch_title_guild_v197 as patch
import title_equipment_v197_fixture as native


class TitleEquipmentV197(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        name=os.environ.get('DNF_CLIENT_V196')
        if not name:raise unittest.SkipTest('requires exact released V196')
        cls.before=Path(name).read_bytes();cls.after,cls.receipt=patch.build(cls.before)

    def test_full_recursive_request_changes_only_manual_marker(self):
        old=native.generic_context(native.Image(self.before))
        new=native.generic_context(native.Image(self.after))
        self.assertEqual(old['opcode'],19);self.assertEqual(new['opcode'],19)
        a,b=bytes.fromhex(old['body']),bytes.fromhex(new['body'])
        self.assertEqual(len(a),28);self.assertEqual(len(b),28)
        self.assertEqual(a[-2:],b'\0\0');self.assertEqual(b[-2:],b'\2\0')
        self.assertEqual(a[:-2],b[:-2])
        self.assertEqual(new['markerArgs'],[41,13,2,0,1])
        self.assertEqual(new['producers'][0]['args'],[41,13,1,6,0])
        self.assertEqual(new['producers'][1]['args'],[41,13,2,3,1])
        self.assertEqual(new['resolverGroups'],[0,1,2,3,4])

    def test_recursive_ancestry_and_shape_reject_mismatches(self):
        def change(depth,offset,value):
            def apply(machine,bp):
                for _ in range(depth):bp=machine.read(bp)
                machine.put(bp+offset,value)
            return apply
        cases=[(0,4,0x25FA396),(0,8,42),(0,12,12),(0,16,3),(0,24,0),
               (1,4,0x249C881),(1,8,42),(1,12,12),(1,16,2),(1,20,0),(1,24,1),
               (2,4,0x24A6B10),(3,4,0x32C76FE)]
        for depth,offset,value in cases:
            with self.subTest(depth=depth,offset=offset,value=value):
                native.Full.mutation=staticmethod(change(depth,offset,value))
                try:
                    a=native.generic_context(native.Image(self.before))
                    b=native.generic_context(native.Image(self.after))
                    self.assertEqual(a['marker'],0);self.assertEqual(b['marker'],0)
                    self.assertEqual(a['body'],b['body'])
                finally:native.Full.mutation=None

    def test_existing_direct_title_and_unrelated_boundaries_preserved(self):
        cases=[{},dict(slot=12),dict(parent=0x24A6B10),dict(grand=0x32C7702),
               dict(parent_outside_stack=True),dict(parent_unaligned=True),dict(qty=2),dict(slot=26),
               dict(ret=0x24A288E,arg4=0),dict(ret=0x24A288E,arg4=0,slot=12),
               dict(ret=0x24A288E,arg4=0,qty=2),dict(ret=0x24A288E,arg4=2),
               dict(ret=0x12ACB30,parent=0x12AE59F,grand=0x12AE7F5,source=3,arg4=2)]
        for values in cases:
            with self.subTest(values=values):
                a=native.boundary_case(native.Image(self.before),**values)
                b=native.boundary_case(native.Image(self.after),**values)
                self.assertEqual(a['tape'],b['tape'])


if __name__=='__main__':unittest.main()
