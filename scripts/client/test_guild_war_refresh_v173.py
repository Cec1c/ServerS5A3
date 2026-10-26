"""Bounded original V173 ACK dispatcher, 1297 receiver and scene-entry checks.

CRT, transport decoding, callback-map lookup and UI/scene effects are adapters.
Status/error readers, callback argument production, receiver copy/decisions,
upgrade selection and scene-entry decisions execute native machine code.
"""
import json
import os
from pathlib import Path
import struct
import tempfile
import unittest

from unicorn import Uc, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE
from unicorn.x86_const import *
import patch_guild_war_refresh_v173 as patch
from test_patch_fatigue_count_v164 import native, windows_image


class WarNative:
    STACK, STOP, PACKET = 0x21018000, 0x22000000, 0x21020000
    GUILD, DATA, MANAGER, SCENE = 0x20001000, 0x20002000, 0x565F950, 0x20009000
    NETWORK = 0x23000000

    def __init__(self, data):
        self.u = Uc(UC_ARCH_X86, UC_MODE_32)
        opt, sections = patch.pe.headers(data)
        self.u.mem_map(patch.pe.IMAGE_BASE, struct.unpack_from('<I',data,opt+56)[0])
        for va,size,raw in sections:self.u.mem_write(va,data[raw:raw+size])
        for at,size in ((0,4096),(0x20000000,0x20000),(0x21000000,0x40000),(self.STOP,4096),(self.NETWORK,0x100000)):
            self.u.mem_map(at,size)
        self.town,self.area,self.scene,self.guild_present = 8,3,self.SCENE,True
        self.effects,self.ui,self.messages,self.ack_args = [],[],[],[]
        self.put(0x566385C,1)
        self.put(self.MANAGER+0x420,3)
        self.put(self.NETWORK+4,1)
        self.put(0x2000C000,0x1067EF0,0)
        self.hooks = {
            0x3E734F0:self.memset,0x3E730D0:self.memcpy,
            0x3E72CE4:lambda:self.ret(),0x3E70882:lambda:self.ret(),
            0x2986E10:self.guild_lookup,0x22841B0:lambda:self.ret(value=self.DATA),
            0x10728C0:lambda:self.ret(value=self.MANAGER),
            0x1FA2790:lambda:self.ret(value=0x2000A000),
            0x1302400:lambda:self.ret(value=self.scene),
            0x36FEEB0:lambda:self.ret(value=self.town),0x36FEEC0:lambda:self.ret(value=self.area),
            0x10723C0:self.config,0xEDF210:lambda:self.ret(),
            0x2047360:lambda:self.ret(4,0x2000A000),
            0x2046EA0:lambda:self.ret(value=self.SCENE),0x20470A0:lambda:self.ret(value=self.SCENE),
            0x1068D10:lambda:self.effect('base',8),0x106A630:lambda:self.effect('structure',8),
            0x10763D0:lambda:self.effect('guardian',4),
            0x32B55E0:lambda:self.ret(4,1),0x32B5C90:lambda:self.ret(8,0x2000B000),
            0x3E73CB6:lambda:self.ret(value=self.arg()),
            0xF094B0:lambda:self.ui_effect('war'),0xEE6520:lambda:self.ui_effect('guardian'),
            0xEE33A0:lambda:self.ui_effect('upgrade_id',4),0xEE33E0:lambda:self.ui_effect('level'),
            0xEE3430:lambda:self.ui_effect('skill',4),0xE93A90:lambda:self.ui_effect('global'),
            0x39A6620:self.message,0x32BEA50:lambda:self.ret(12),
            0x8DF100:self.empty_iterator,0xA8F6C0:self.empty_iterator,
            0x3E738B1:lambda:(_ for _ in ()).throw(AssertionError('checked iterator failed')),
            # Plain transport fixture supplies an already decoded header/body.
            0x37E7DA0:lambda:self.ret(),0x374E980:lambda:self.ret(),
            0x376EC10:self.decoded_packet,0x2523820:lambda:self.ret(8,1),
            0x374EA10:lambda:self.ret(value=self.NETWORK),0x374E9D0:lambda:self.ret(value=0),
            0x374FC40:lambda:self.ret(4),0x37501A0:lambda:self.ret(8),
            0x8BAB80:lambda:self.ret(value=0),0x769AD0:lambda:self.ret(),
            0x2524210:lambda:self.ret(4,self.NETWORK),0x2523FF0:lambda:self.ret(value=0x2000C000),
            0x37DE240:lambda:self.ret(value=0x2000B800),
            0x45FEA0:self.string_init,0x90D380:lambda:self.ret(4),
            0x37E0840:self.format_string,0x1331A10:lambda:self.ret(value=self.reg(UC_X86_REG_ECX)),
            0x45F8C0:lambda:self.ret(),0x37DEAF0:lambda:self.ret(),0x37DD7C0:lambda:self.ret(),
            0x3876E50:lambda:self.ret(),0x3876E60:lambda:self.ret(),
            0x37BD990:lambda:self.ret(4),0x37C2C50:lambda:self.ret(),
        }
        self.u.hook_add(UC_HOOK_CODE,self.hook)

    def put(self,at,*values):self.u.mem_write(at,struct.pack('<'+'I'*len(values),*[v&0xffffffff for v in values]))
    def read(self,at):return struct.unpack('<I',self.u.mem_read(at,4))[0]
    def reg(self,r):return self.u.reg_read(r)
    def arg(self,i=0):return self.read(self.reg(UC_X86_REG_ESP)+4+4*i)
    def ret(self,pop=0,value=None):
        sp=self.reg(UC_X86_REG_ESP);dest=self.read(sp)
        if value is not None:self.u.reg_write(UC_X86_REG_EAX,value)
        self.u.reg_write(UC_X86_REG_ESP,sp+4+pop);self.u.reg_write(UC_X86_REG_EIP,dest)
    def hook(self,_u,at,_size,_ctx):
        if at==0x1067EF0:self.ack_args.append([self.arg(),self.arg(1)&255,self.arg(2)&255,self.arg(3)])
        if at in self.hooks:self.hooks[at]()
    def memset(self):self.u.mem_write(self.arg(),bytes([self.arg(1)&255])*self.arg(2));self.ret(value=self.arg())
    def memcpy(self):self.u.mem_write(self.arg(),bytes(self.u.mem_read(self.arg(1),self.arg(2))));self.ret(value=self.arg())
    def guild_lookup(self):assert self.arg()==1;self.ret(4,self.GUILD if self.guild_present else 0)
    def config(self):out=self.arg();self.put(out+0x334,3);self.ret(4,out)
    def effect(self,name,pop):self.effects.append((name,[self.arg(i) for i in range(pop//4)]));self.ret(pop)
    def ui_effect(self,name,pop=0):self.ui.append(name);self.ret(pop)
    def message(self):self.messages.append(self.arg());self.ret(value=0x2000B800)
    def empty_iterator(self):
        out=self.arg();self.put(out,0x2000E000,0,0);self.put(0x2000E000,0x2000E100);self.ret(4,out)
    def decoded_packet(self):self.put(self.arg(1),self.PACKET);self.ret(8,0)
    def string_init(self):
        at=self.reg(UC_X86_REG_ECX);self.u.mem_write(at,bytes(32));self.put(at+24,7);self.ret(4,at)
    def format_string(self):
        at=self.arg();self.u.mem_write(at,bytes(32));self.put(at+24,7);self.ret(value=at)
    def run(self,va,args=(),ecx=None,pop=None):
        self.put(self.STACK,self.STOP,*args)
        self.u.reg_write(UC_X86_REG_ESP,self.STACK);self.u.reg_write(UC_X86_REG_EFLAGS,2)
        keep={UC_X86_REG_EBX:0x12345670,UC_X86_REG_ESI:0x12345671,UC_X86_REG_EDI:0x12345672,UC_X86_REG_EBP:0x12345673}
        for r,v in keep.items():self.u.reg_write(r,v)
        self.u.reg_write(UC_X86_REG_ECX,self.MANAGER if ecx is None else ecx)
        seh=self.read(0)
        self.u.emu_start(va,self.STOP,count=100000)
        assert self.reg(UC_X86_REG_EIP)==self.STOP,hex(self.reg(UC_X86_REG_EIP))
        assert self.reg(UC_X86_REG_ESP)==self.STACK+4+(len(args)*4 if pop is None else pop),'stack ABI'
        assert all(self.reg(r)==v for r,v in keep.items()),('callee-saved ABI',{r:hex(self.reg(r)) for r in keep})
        assert self.read(0)==seh,'SEH chain'
        return self.reg(UC_X86_REG_EAX)
    def default(self):self.run(0x3C1B2D0,ecx=self.PACKET);return bytes(self.u.mem_read(self.PACKET,0x895))
    def selected(self):
        body=bytearray(self.default());struct.pack_into('<I',body,0x44,1)
        body[0x7F8:0x80A]=struct.pack('<IB5BII',1,1,1,1,1,1,1,0,0)
        return body
    def receive(self,body):
        assert len(body)==0x895
        self.u.mem_write(self.PACKET,bytes(body));self.put(0x57F9AD0,self.PACKET,len(body),0,0)
        self.effects.clear();self.ui.clear();self.run(0x1077450)
        assert self.read(0x57F9AD0)==self.PACKET+0x895 and self.read(0x57F9AD4)==0
        if self.guild_present:assert bytes(self.u.mem_read(self.DATA+0x2E05,0x895))==body
        return [x[0] for x in self.effects]
    def enter(self,selected=True):
        self.effects.clear();self.run(0x1077BB0,(self.SCENE,8,3))
        return [x[0] for x in self.effects]
    def ack(self,body):
        wire=bytearray(16)+body;wire[0]=1
        struct.pack_into('<H',wire,1,1350);struct.pack_into('<I',wire,3,len(wire))
        self.u.mem_write(self.PACKET,bytes(wire));self.messages.clear();self.ack_args.clear()
        self.run(0x25239A0,(0,self.PACKET,0),ecx=self.NETWORK)
        assert len(self.ack_args)==1,self.ack_args
        assert self.read(0x57F9AD4)==0,'ACK body not consumed exactly'
        return self.ack_args[0],list(self.messages)


class GuildWarV173Tests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        name=os.environ.get('DNF_GUILD_WAR_V173_CLIENT')
        if not name:raise unittest.SkipTest('set DNF_GUILD_WAR_V173_CLIENT to exact V173')
        cls.source=Path(name);cls.before=cls.source.read_bytes();cls.after,cls.receipt=patch.build(cls.before)
        cls.original=WarNative(cls.before);cls.fixed=WarNative(cls.after)

    def test_01_repeated_and_currency_packets_red_green(self):
        for lab,expected in ((self.original,['base','structure','guardian']),(self.fixed,[])):
            body=lab.selected();lab.receive(lab.default())
            self.assertEqual(lab.receive(body),['base','structure','guardian'])
            for label,at in (('identical',None),('peer_ap',0x17),('gp',0x1B),('guardian_ap',0x802),('guardian_gp',0x806)):
                with self.subTest(patched=lab is self.fixed,change=label):
                    if at is not None:struct.pack_into('<I',body,at,123)
                    self.assertEqual(lab.receive(body),expected)
                    self.assertEqual(lab.ui,['war','global','guardian'])

    def test_02_structural_changes_and_upgrade_notifications(self):
        lab=self.fixed;body=lab.selected();lab.receive(body)
        for name,offset,value in (('select',0x44,2),('building',0x50,1),('level',0x7FC,11),('skill',0x7FD,2),('unknown_structure',0x874,1)):
            with self.subTest(change=name):
                body[offset]=value
                self.assertEqual(lab.receive(body),['base','structure','guardian'])
                if name=='level':self.assertIn('level',lab.ui)
                if name=='skill':self.assertIn('skill',lab.ui)
                self.assertEqual(lab.receive(body),[])

    def test_03_scene_entry_is_independent_and_repeats_on_reentry(self):
        for lab in (self.original,self.fixed):
            body=lab.selected();lab.receive(body)
            for _ in range(2):self.assertEqual(lab.enter(),['base','structure','guardian'])
            lab.receive(lab.default())
            self.assertEqual(lab.enter(False),['base','structure'])

    def test_04_other_scenes_preserve_original_effects(self):
        for town,area,scene,expected in ((1,3,1,['guardian']),(8,2,1,['structure','guardian']),(8,3,0,['guardian'])):
            for lab in (self.original,self.fixed):
                with self.subTest(patched=lab is self.fixed,town=town,area=area,scene=scene):
                    lab.town,lab.area,lab.scene=town,area,lab.SCENE if scene else 0
                    body=lab.selected();lab.receive(body)
                    self.assertEqual(lab.receive(body),expected)
                    lab.town,lab.area,lab.scene=8,3,lab.SCENE

    def test_05_actual_ack_dispatch_success_and_errors(self):
        message_ids={3:100000882,22:100000878,100:19140,96:48491,223:100000878}
        for lab in (self.original,self.fixed):
            for body in (b'\x01',b'\x00\x03',b'\x00\x16',b'\x00\x64',b'\x00\x60',b'\x00\xdf'):
                with self.subTest(patched=lab is self.fixed,body=body.hex()):
                    args,messages=lab.ack(body)
                    self.assertEqual(args[:3],[1350,body[0],body[1] if len(body)>1 else 0])
                    self.assertEqual(messages,[] if body[0] else [message_ids[body[1]]])

    def test_06_go_production_packets(self):
        folder=os.environ.get('GUILD_ESSENCE_NATIVE_STAGE_DIR')
        if not folder:self.skipTest('set GUILD_ESSENCE_NATIVE_STAGE_DIR to Go exported packets')
        files=sorted(Path(folder).glob('*-1350.bin'));self.assertGreater(len(files),0)
        cases=[]
        for file in files:
            body=file.read_bytes();args,messages=self.fixed.ack(body)
            self.assertEqual(args[1],body[0]);self.assertEqual(len(messages),0 if body[0] else 1)
            snapshot=file.with_name(file.name.replace('-1350.bin','-1297.bin'))
            if snapshot.exists():self.fixed.receive(snapshot.read_bytes())
            cases.append({'file':file.name,'body':body.hex(),'args':args,'messageIDs':messages})
        output=os.environ.get('GUILD_WAR_NATIVE_RESULT')
        if output:Path(output).write_text(json.dumps(cases,indent=2),encoding='utf-8')

    def test_07_exact_patch_and_original_gate_matrix(self):
        cursor=0
        for lo,hi in self.receipt['changedRanges']:
            self.assertEqual(self.before[cursor:lo],self.after[cursor:lo]);cursor=hi
        self.assertEqual(self.before[cursor:],self.after[cursor:len(self.before)])
        opt,_=patch.pe.headers(self.after)
        self.assertEqual(struct.unpack_from('<I',self.after,opt+64)[0],patch.pe.checksum(self.after,opt+64))
        for cls in (0,1,2,255):self.assertEqual(native(self.before,(cls,),range(65536)),native(self.after,(cls,),range(65536)))

    def test_08_independent_output_and_windows_mapping(self):
        for invalid in (self.after,self.before[:-1],b'X'+self.before[1:]):
            with self.assertRaises(ValueError):patch.build(invalid)
        with self.assertRaises(ValueError):patch.write_candidate(self.source,self.source)
        with tempfile.TemporaryDirectory() as folder:
            path=Path(folder)/'DNF.exe'
            receipt=patch.write_candidate(self.source,path)
            self.assertEqual(path.read_bytes(),self.after)
            self.assertEqual(receipt['outputSha256'],patch.pe.sha(self.after))
            with self.assertRaises(ValueError):patch.write_candidate(self.source,path)
            if os.name=='nt':windows_image(path)

    def test_09_absent_guild_and_all_progress_slots(self):
        lab=self.fixed;lab.town,lab.area,lab.scene=8,3,lab.SCENE
        body=lab.selected();lab.receive(body)
        for row in range(5):
            for offset in (10,14):
                with self.subTest(row=row,offset=offset):
                    struct.pack_into('<I',body,0x7F8+18*row+offset,987)
                    self.assertEqual(lab.receive(body),[])
        before=bytes(lab.u.mem_read(lab.DATA+0x2E05,0x895))
        lab.guild_present=False
        self.assertEqual(lab.receive(lab.default()),[])
        self.assertEqual(bytes(lab.u.mem_read(lab.DATA+0x2E05,0x895)),before)
        lab.guild_present=True


if __name__=='__main__':unittest.main()
