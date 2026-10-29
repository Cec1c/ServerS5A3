"""Actual profile profession renderer red/green regression.

Catalog lookup, protected scalar IO, graphics and formatting are explicit
fixtures. The scene getter, level query, experience-table calculation, both
native bounds checks and renderer epilogue execute original machine code.
No game process or player files are modified.
"""
import os,struct,tempfile,unittest
from pathlib import Path
import patch_profile_render_v192 as patch
from test_patch_fatigue_count_v164 import windows_image
from test_guild_war_refresh_v173 import WarNative
from unicorn.x86_const import *
from collections import deque

class ProfileRenderer(WarNative):
    WINDOW,USER,ACTOR,VTABLE,TABLE,ROWS,GFX,GFXVT=range(0x20010000,0x20018000,0x1000)
    def __init__(self,data,actor='absent',experience=282,kind=3,count=31,table=True):
        super().__init__(data)
        self.recent=deque(maxlen=25)
        self.formatted=[];self.draws=[];self.exp=experience;self.kind=kind;self.count=count
        self.hooks.update({0x209bbe0:lambda:self.ret(value=50),0x37e9f40:lambda:self.ret(value=158),
            0x22000100:lambda:self.ret(4),0x22000200:lambda:self.ret(4),0x22000300:self.draw,
            0x22000400:lambda:self.ret(value=0 if actor=='invalid' else self.USER),
            0x22000500:lambda:self.ret(value=90),
            0x22000600:lambda:self.ret(),
            0x3a31aa0:lambda:self.ret(4,self.TABLE if table else 0),
            0x3b59ea0:lambda:self.ret(value=0x2000e200),
            0x39a6620:self.string_id,0x37de240:lambda:self.ret(value=0x2000b800),
            0x8ef130:self.format_row})
        for a in (0x37df550,0x37df5c0,0x37df630,0x37df6a0,0x37df7f0):self.hooks[a]=self.protected_read
        self.put(0x581a298,self.GFX);self.put(self.GFX,self.GFXVT)
        for at,fn in ((0x64,0x22000100),(0x60,0x22000200),(0x78,0x22000300),(0x68,0x22000600)):self.put(self.GFXVT+at,fn)
        self.put(self.WINDOW+0x1b8,self.USER)
        self.put(self.USER+0x30+0x370,kind)
        self.put(self.USER+0x30+0x3b4,90)
        self.put(self.USER+0x4f8,0 if actor=='absent' else self.ACTOR)
        self.put(self.ACTOR,self.VTABLE);self.put(self.VTABLE+0xd20,0x22000400);self.put(self.VTABLE+0x3c0,0x22000500)
        self.put(0x57b7310,0x2000e400)
        self.put(self.TABLE+4,self.ROWS,self.ROWS+12*count)
        for i in range(count):self.put(self.ROWS+12*i,100*(i+1),0,0x2000b800)
    def protected_read(self):
        assert self.arg()==self.USER+0x30+0x374+4
        self.put(self.arg(1),self.exp);self.ret(8)
    def hook(self,u,at,size,ctx):
        if hasattr(self,'recent'):self.recent.append((hex(at),hex(self.reg(UC_X86_REG_ESP))))
        super().hook(u,at,size,ctx)
    def string_id(self):self.messages.append(self.arg());self.ret(value=0x2000b800+self.arg()%0x100)
    def draw(self):self.draws.append([self.arg(i) for i in range(4)]);self.ret(16)
    def format_row(self):
        self.formatted.append([self.arg(i) for i in range(3,7)])
        self.u.mem_write(self.arg(),b'X\0\0\0');self.ret(value=1)
    def render(self):
        self.run(0x3497ef0,ecx=self.WINDOW)
        return dict(formatted=self.formatted,messages=self.messages,draws=len(self.draws))


class ProfileRenderTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        source=os.environ.get('DNF_PROFILE_V192_CLIENT')
        if not source: raise unittest.SkipTest('exact published V192 required')
        cls.source=Path(source);cls.before=cls.source.read_bytes()
        cls.after,cls.receipt=patch.build(cls.before)

    def render(self,data,**kw):
        lab=ProfileRenderer(data,**kw)
        return lab,lab.render()

    def test_missing_and_unusable_scene_actor_red_green(self):
        for actor in ('absent','invalid'):
            old=ProfileRenderer(self.before,actor=actor)
            with self.assertRaisesRegex(AssertionError,'checked iterator failed'):old.render()
            self.assertEqual(old.reg(UC_X86_REG_ESI),0xfffffffe)
            self.assertEqual(old.reg(UC_X86_REG_ECX),31)
            new,result=self.render(self.after,actor=actor)
            self.assertEqual(result['formatted'][0][1:],[3,282,300])
            self.assertEqual(result['draws'],2)

    def test_experience_thresholds_use_native_table(self):
        for exp,level in ((0,1),(99,1),(100,2),(282,3),(3099,31),(3100,31),(0xffffffff,31)):
            lab,r=self.render(self.after,experience=exp)
            self.assertEqual(r['formatted'][0][1:],[level,exp,level*100])

    def test_existing_scene_level_cap_preserved(self):
        for exp in (0,282,3100):
            _,a=self.render(self.before,actor='present',experience=exp)
            _,b=self.render(self.after,actor='present',experience=exp)
            self.assertEqual(a,b)
            self.assertEqual(b['formatted'][0][1],1)  # empty cap fixture: native minimum

    def test_missing_empty_and_malformed_resources_omit_only_value(self):
        for kw in ({'table':False},{'count':0}):
            _,r=self.render(self.after,**kw)
            self.assertEqual(r,dict(formatted=[],messages=[42020],draws=1))
        for begin,end in ((0,12),(0x20015000,0x2001500d),(0x2001500c,0x20015000)):
            lab=ProfileRenderer(self.after);lab.put(lab.TABLE+4,begin,end)
            self.assertEqual(lab.render(),dict(formatted=[],messages=[42020],draws=1))

    def test_zero_and_invalid_positive_level_never_index(self):
        for value in (0,32,0xfffffffe):
            lab=ProfileRenderer(self.after)
            lab.hooks[0x29728c0]=lambda v=value:lab.ret(value=v)
            self.assertEqual(lab.render(),dict(formatted=[],messages=[42020],draws=1))

    def test_no_profession_and_repeat_render_preserved(self):
        _,a=self.render(self.before,kind=0);_,b=self.render(self.after,kind=0)
        self.assertEqual(a,b);self.assertEqual(b['messages'],[42020,19017])
        lab=ProfileRenderer(self.after)
        original=bytes(lab.u.mem_read(lab.USER,0x900))
        for _ in range(12):lab.render()
        self.assertEqual(bytes(lab.u.mem_read(lab.USER,0x900)),original)
        self.assertEqual(len(lab.formatted),12)
        self.assertEqual(lab.read(lab.USER+0x4f8),0)

    def test_input_output_and_byte_preservation(self):
        pos=0
        for low,high in self.receipt['changedRanges']:
            self.assertEqual(self.before[pos:low],self.after[pos:low]);pos=high
        self.assertEqual(self.before[pos:],self.after[pos:len(self.before)])
        for value in (self.after,self.before[:-1]):
            with self.assertRaises(ValueError):patch.build(value)
        with self.assertRaises(ValueError):patch.write_candidate(self.source,self.source)
        with tempfile.TemporaryDirectory() as td:
            output=Path(td)/'DNF.exe';patch.write_candidate(self.source,output)
            self.assertEqual(output.read_bytes(),self.after)
            with self.assertRaises(ValueError):patch.write_candidate(self.source,output)
            if os.name=='nt':windows_image(output)

if __name__=='__main__':unittest.main()
