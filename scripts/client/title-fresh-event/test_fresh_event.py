"""Native input-lifetime and producer regression, with explicit leaf fixtures."""
import gc
import os
from pathlib import Path
import struct
import unittest
from unittest.mock import patch

import build_fresh_event as build
import verify_recursive as old
from pe_support import Image, pe_checksum
from unicorn import UC_HOOK_CODE
from unicorn.x86_const import *


class FreshEventTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.before = Path(os.environ['TITLE_FRESH_RAW']).read_bytes()
        cls.after, cls.receipt = build.build(cls.before)
        cls.original = Image.from_bytes(cls.before)
        cls.candidate = Image.from_bytes(cls.after)
        cls.state = cls.receipt['eventStateVa']

    def tearDown(self):
        gc.collect()

    def machine(self):
        m = old.prepare_machine(self.candidate)
        m.mem_map(0, 0x1000)
        old.put_u32(m, 0, 0xffffffff)
        old.put_u32(m, build.RIGHT_TARGET, 0xffffffff)
        return m

    def rightup(self, m, target=12345, allowed=True):
        """Actual right-up prologue, lookup outcome and target-write hook.

        The global target lookup is a leaf adapter. Stop before event container
        creation; no event-dispatch or graphics lifetime is claimed here.
        """
        stops=[]
        def observe(cpu, address, size, ctx):
            if address == 0x385BDE0:
                cpu.reg_write(UC_X86_REG_EAX, int(allowed));old.resume_return(cpu)
            elif address in (0x382A49A,0x382A50F):
                stops.append(address);cpu.emu_stop()
        handle=m.hook_add(UC_HOOK_CODE,observe)
        sp=old.STACK+0x4000
        old.put_u32(m,sp,old.STOP);old.put_u32(m,sp+4,target)
        m.reg_write(UC_X86_REG_ESP,sp);m.reg_write(UC_X86_REG_EBP,old.STACK+0x4800)
        m.reg_write(UC_X86_REG_ECX,old.FAKE_WINDOW)
        try:m.emu_start(0x382A450,old.STOP,count=1000)
        finally:m.hook_del(handle)
        self.assertEqual(stops,[0x382A49A if allowed else 0x382A50F])
        return old.read_u32(m,self.state)

    def marker(self,m,**options):
        # The legacy runner owns a new machine per call and retains its code
        # observer. This test deliberately reuses one machine across clicks;
        # scope every observer to this call without resetting native state.
        active=set();original_add=m.hook_add;original_del=m.hook_del
        def add(*args,**kwargs):
            handle=original_add(*args,**kwargs);active.add(handle);return handle
        def remove(handle):
            original_del(handle);active.discard(handle)
        with patch.object(old,'prepare_machine',return_value=m), \
             patch.object(m,'hook_add',side_effect=add), \
             patch.object(m,'hook_del',side_effect=remove):
            try:return old.run_synthetic_recursive_case(self.candidate,**options)
            finally:
                for handle in tuple(active):remove(handle)

    def reset_prefix(self,m,site,enabled=1):
        original=build.EXPECTED_HOOKS[site]
        m.reg_write(UC_X86_REG_ESP,old.STACK+0x5000)
        m.reg_write(UC_X86_REG_ECX,old.FAKE_WINDOW)
        old.put_u32(m,0x5686AA8,old.FAKE_WINDOW)
        m.mem_write(old.FAKE_WINDOW+0xEE,bytes([enabled]))
        m.emu_start(site,site+len(original),count=100)
        self.assertEqual(m.reg_read(UC_X86_REG_EIP),site+len(original))
        self.assertEqual(old.read_u32(m,self.state),0xffffffff)

    def test_01_actual_rightup_then_recursive_route(self):
        baseline=old.run_recursive_route(self.original,case_label='baseline-actual-recursion')
        self.assertEqual(baseline['tape'],[0])
        m=self.machine();self.assertEqual(self.rightup(m),12345)
        with patch.object(old,'prepare_machine',return_value=m):
            fixed=old.run_recursive_route(self.candidate,case_label='fresh-rightup-actual-recursion')
        self.assertEqual(fixed['tape'],[2]);self.assertTrue(fixed['recursiveEntered'])
        self.assertEqual(old.read_u32(m,self.state),0xffffffff)

    def test_02_stale_global_without_new_event_rejected(self):
        m=self.machine();old.put_u32(m,build.RIGHT_TARGET,12345)
        self.assertEqual(self.marker(m)['tape'],[0])

    def test_03_rightup_one_shot_and_second_click(self):
        m=self.machine();self.rightup(m)
        self.assertEqual(self.marker(m)['tape'],[2])
        self.assertEqual(self.marker(m)['tape'],[0])
        self.rightup(m);self.assertEqual(self.marker(m)['tape'],[2])

    def test_04_different_current_target_not_promoted(self):
        m=self.machine();self.rightup(m);old.put_u32(m,build.RIGHT_TARGET,12346)
        self.assertEqual(self.marker(m)['tape'],[0]);self.assertEqual(old.read_u32(m,self.state),12345)

    def test_05_rejected_rightup_does_not_arm(self):
        m=self.machine();self.assertEqual(self.rightup(m,allowed=False),0xffffffff)
        old.put_u32(m,build.RIGHT_TARGET,12345);self.assertEqual(self.marker(m)['tape'],[0])

    def test_06_frame_entry_clears_even_when_global_target_is_stale(self):
        m=self.machine();self.rightup(m);self.reset_prefix(m,build.FRAME_SITE)
        self.assertEqual(old.read_u32(m,build.RIGHT_TARGET),12345)
        self.assertEqual(self.marker(m)['tape'],[0])

    def test_07_root_prefix_clears_before_both_enable_branches(self):
        for enabled in (0,1):
            with self.subTest(enabled=enabled):
                m=self.machine();self.rightup(m);self.reset_prefix(m,build.ROOT_STEP_SITE,enabled)
                self.assertEqual(old.read_u32(m,build.RIGHT_TARGET),12345)
                self.assertEqual(self.marker(m)['tape'],[0])

    def test_08_exact_frame_and_parameter_negatives(self):
        cases=[{'grand_return':old.rec.OUTER_PARENT_RETURN+1},{'inner_parent':0}]
        for label,template in [('inner',old.STRICT_INNER),('outer',old.STRICT_OUTER)]:
            for key,value in [('source',-1 if label=='inner' else 0),('slot',12),('count',4),('arg4',8),('arg5',8),('return',0xA5A50000)]:
                changed=dict(template,**{key:value});cases.append({label:changed})
        for options in cases:
            with self.subTest(options=options):
                m=self.machine();self.rightup(m)
                result=self.marker(m,**options)
                self.assertEqual(result['tape'],[0]);self.assertEqual(old.read_u32(m,self.state),12345)

    def test_09_legacy17_unchanged(self):
        for name,ret,parent,slot,count,arg4,expected,mode,null_frame in old.ORIGINAL_CASES:
            with self.subTest(name=name):
                result=old.run_legacy_case(self.candidate,ret,parent,slot,count,arg4,mode,null_frame,case_label=name)
                self.assertEqual(result['marker'],expected);self.assertEqual(result['tape'],[expected])

    def test_10_declared_bytes_checksum_and_rejection(self):
        cursor=0
        for lo,hi in self.receipt['inputChangedRanges']:
            self.assertEqual(self.before[cursor:lo],self.after[cursor:lo]);cursor=hi
        self.assertEqual(self.before[cursor:],self.after[cursor:len(self.before)])
        self.assertEqual(self.candidate.section('.titlert')['characteristics'],0xC0000040)
        checksum=struct.unpack_from('<I',self.after,self.candidate.optional+64)[0]
        self.assertEqual(checksum,pe_checksum(self.after,self.candidate.optional+64))
        for invalid in (self.after,self.before[:-1],b'X'+self.before[1:]):
            with self.assertRaises(ValueError):build.build(invalid)


if __name__=='__main__':unittest.main()
