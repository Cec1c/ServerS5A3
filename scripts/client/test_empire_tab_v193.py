"""Actual Empire Tab empty/partial/full participant-list regression.

Current-user/world lookup and UI visibility/opening are adapters. The real
entry/epilogue, participant lookup and checked iterators execute native code.
Two-participant switching is checked at the unchanged continuation, not
represented as full battle/GUI acceptance.
"""
import gc,os,struct,tempfile,unittest
from pathlib import Path
import patch_empire_tab_v193 as patch
from test_guild_war_refresh_v173 import WarNative
from test_patch_fatigue_count_v164 import windows_image
from unicorn.x86_const import *

class EmpireTab(WarNative):
    TEAM,PROXY,HEAD,USER=0x20010000,0x20010100,0x20010200,0x20010300
    def __init__(self,data,count=0,visible=False):
        super().__init__(data);self.opens=[];self.ready=[];self.seen=[];self.count=count;self.visible=visible
        self.hooks.update({0x296dd80:lambda:self.ret(value=self.USER),0x3c1bea0:lambda:self.ret(value=1001),
            0x32b4af0:lambda:self.ret(value=self.SCENE),0xb62330:lambda:self.ret(value=0),
            0x32b55e0:self.window_visible,0x296a410:lambda:self.ret(value=self.TEAM),
            0x3810650:lambda:self.ret(value=self.SCENE),0x29f0850:lambda:self.ret(value=0),
            0x36c4f30:lambda:self.ret(value=0),0x32bea50:self.open_team,0x145f90e:self.valid_continuation})
        self.put(0x56a3758,self.USER);self.put(0x56a3798,self.MANAGER)
        self.put(self.TEAM+0x14,self.PROXY,self.HEAD,count);self.put(self.PROXY,self.TEAM+0x14)
        nodes=[0x20011000+i*0x800 for i in range(count)]
        self.put(self.HEAD,nodes[0] if nodes else self.HEAD,nodes[-1] if nodes else self.HEAD)
        for i,node in enumerate(nodes):
            self.put(node,nodes[i+1] if i+1<len(nodes) else self.HEAD,nodes[i-1] if i else self.HEAD,i)
    def hook(self,u,at,size,ctx):
        self.seen.append(at);super().hook(u,at,size,ctx)
    def window_visible(self):self.ret(4,int(self.visible and self.arg()==0xda))
    def open_team(self):
        self.opens.append([self.arg(i) for i in range(3)]);self.ret(12)
    def valid_continuation(self):
        frame=self.reg(UC_X86_REG_EBP)
        self.ready.append(dict(first=self.read(frame-0x1c),second=self.read(frame-0x20),firstSelector=self.reg(UC_X86_REG_EDI),secondSelector=self.reg(UC_X86_REG_EAX)))
        self.u.reg_write(UC_X86_REG_EIP,0x145fb34)
    def tab(self):
        self.opens.clear();self.ready.clear();self.seen.clear();self.run(0x145f730,ecx=self.TEAM)
        return dict(opened=self.opens,ready=self.ready,participantGetterExecuted=0x1456e80 in self.seen,checkedDereferenceExecuted=0x2921b40 in self.seen)

class EmpireTabTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        source=os.environ.get('DNF_EMPIRE_TAB_V193_CLIENT')
        if not source:raise unittest.SkipTest('exact published V193 required')
        cls.source=Path(source);cls.before=cls.source.read_bytes()
        cls.after,cls.receipt=patch.build(cls.before)

    def test_empty_roster_real_entry_red_green(self):
        old=EmpireTab(self.before,0)
        with self.assertRaisesRegex(AssertionError,'checked iterator failed'):old.tab()
        self.assertIn(0x145f8cf,old.seen)
        self.assertEqual(old.read(old.reg(UC_X86_REG_EBP)-0x1c),0)
        self.assertEqual(old.read(old.reg(UC_X86_REG_EBP)-0x20),0)
        fixed=EmpireTab(self.after,0);result=fixed.tab()
        self.assertEqual(result['opened'],[[0xda,0,0]])
        self.assertEqual(result['ready'],[])
        self.assertTrue(result['participantGetterExecuted'])
        self.assertFalse(result['checkedDereferenceExecuted'])

    def test_partial_team_uses_same_setup_window(self):
        for data in (self.before,self.after):
            lab=EmpireTab(data,1);result=lab.tab()
            self.assertEqual(result['opened'],[[0xda,0,0]])
            self.assertEqual(result['ready'],[])

    def test_complete_team_preserves_native_continuation(self):
        old=EmpireTab(self.before,2);new=EmpireTab(self.after,2)
        self.assertEqual(old.tab(),new.tab())
        self.assertEqual(new.ready[0]['firstSelector'],0)
        self.assertEqual(new.ready[0]['secondSelector'],1)

    def test_visible_window_and_repeated_unconfigured_entry(self):
        visible=EmpireTab(self.after,0,True)
        self.assertEqual(visible.tab()['opened'],[])
        lab=EmpireTab(self.after,0);original=bytes(lab.u.mem_read(lab.TEAM,0x1800))
        for _ in range(30):self.assertEqual(lab.tab()['opened'],[[0xda,0,0]])
        self.assertEqual(bytes(lab.u.mem_read(lab.TEAM,0x1800)),original)

    def test_either_missing_lookup_opens_setup(self):
        for missing in (0,1):
            lab=EmpireTab(self.after,2)
            def lookup():
                index=lab.arg();lab.ret(4,0 if index==missing else 0x20011008+index*0x800)
            lab.hooks[0x1456e80]=lookup
            result=lab.tab();self.assertEqual(result['opened'],[[0xda,0,0]])
            self.assertFalse(result['checkedDereferenceExecuted'])

    def test_exact_input_and_all_undeclared_bytes_preserved(self):
        pos=0
        for lo,hi in self.receipt['changedRanges']:
            self.assertEqual(self.before[pos:lo],self.after[pos:lo]);pos=hi
        self.assertEqual(self.before[pos:],self.after[pos:len(self.before)])
        for data in (self.after,self.before[:-1]):
            with self.assertRaises(ValueError):patch.build(data)
        with self.assertRaises(ValueError):patch.write_candidate(self.source,self.source)
        with tempfile.TemporaryDirectory() as td:
            out=Path(td)/'DNF.exe';patch.write_candidate(self.source,out)
            self.assertEqual(out.read_bytes(),self.after)
            with self.assertRaises(ValueError):patch.write_candidate(self.source,out)
            if os.name=='nt':windows_image(out)

if __name__=='__main__':unittest.main()
