"""Native V179 move/remove senders, ACKs, deltas and practice scoreboard guard.

STL, dialog text/resources and scene object effects are adapted. Original request
construction, record updates, action dispatch and scoreboard predicate execute
in Unicorn without starting a game process.
"""
import os
import struct
import tempfile
import unittest
from pathlib import Path

from unicorn import UC_HOOK_CODE
from unicorn.x86_const import *
from test_guild_placement_v177 import PlacementLab, ProjectionLab
from test_patch_fatigue_count_v164 import native, windows_image
import patch_guild_controls_v179 as patch

class ControlsLab(PlacementLab):
    def __init__(self,data):
        super().__init__(data)
        self.ROW=self.TEMP+0x200
        self.OBJECT=self.TEMP+0x400
        self.put(self.ROW+0x20,7)
        self.put(self.OBJECT,self.TEMP+0x600)
        self.put(self.TEMP+0x600+0x4c,self.STOP+0x200)
        self.hooks.update({
            0x1721170:lambda:self.ret(8,self.ROW),
            0x1223ea0:lambda:self.ret(value=self.ROW-4),
            0x22152e0:lambda:self.ret(4,self.reg(UC_X86_REG_ECX)),
            0x1078430:lambda:self.ret(),
            self.STOP+0x200:lambda:self.ret(value=71021),
        })
    def move(self):
        self.geometry([(750,210,500,160),(290,370,900,450)])
        self.footprint(3,3,[1]*9)
        self.put(self.CONTROL+0x9c,1)
        self.put(self.CONTROL+0xa4,self.OBJECT,self.OBJECT)
        self.send_book()
        return self.sent
    def remove(self):
        dialog=self.CONTROL+0x1b0
        self.put(dialog+0x2c,7,71021,902,276,0)
        text=self.TEMP+0x800
        self.u.mem_write(text,b'Y\0\0\0')
        self.put(self.CONTROL,self.TEMP+0x900)
        self.put(self.TEMP+0x900+0x7c,self.STOP+0x220)
        self.hooks.update({
            0x29baca0:self.dialog_ref,
            0x37de240:lambda:self.ret(value=text),
            0x391ba20:lambda:self.ret(value=self.TEMP+0xa00),
            0x889480:lambda:self.ret(16,0),
            self.STOP+0x220:lambda:self.ret(),
        })
        self.run(0x1193e50,(0,13,0,0),ecx=dialog)
        return self.sent
    def dialog_ref(self):
        out=self.arg();self.put(out,self.TEMP+0xa00,0);self.ret(8,out)

class ControlsTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.source=Path(os.environ['GUILD_CONTROLS_EXE'])
        cls.before=cls.source.read_bytes()
        cls.after,cls.receipt=patch.build(cls.before)

    def test_native_senders(self):
        for action,op in [('move',839),('remove',838)]:
            for data,fixed in [(self.before,False),(self.after,True)]:
                lab=ControlsLab(data)
                sent=getattr(lab,action)()
                self.assertEqual(len(sent),1)
                self.assertEqual(sent[0]['opcode'],op)
                body=bytes.fromhex(sent[0]['body'])
                self.assertEqual(len(body),33)
                self.assertEqual(struct.unpack_from('<5I',body,13),(7,71021,902,276,0))
                if fixed:self.assertEqual(body[:13],bytes(13))

    def test_ack_dispatch(self):
        for op,entry in [(838,0x1066a60),(839,0x10675c0)]:
            for data,fixed in [(self.before,False),(self.after,True)]:
                for body in (b'\1',b'\0\3'):
                    lab=ControlsLab(data);lab.put(0x2000c000,entry,0);calls=[]
                    def observe(_u,at,size,ctx):
                        if at==entry:calls.append([lab.arg(),lab.arg(1)&255,lab.arg(2)&255])
                    lab.u.hook_add(UC_HOOK_CODE,observe)
                    wire=bytearray(16)+body;wire[0]=1
                    struct.pack_into('<H',wire,1,op);struct.pack_into('<I',wire,3,len(wire))
                    lab.u.mem_write(lab.PACKET,bytes(wire))
                    lab.run(0x25239a0,(0,lab.PACKET,0),ecx=lab.NETWORK)
                    self.assertEqual(calls,[[op,body[0],body[1] if len(body)>1 else 0]] if fixed else [])
                    if fixed:self.assertEqual(lab.read(0x57f9ad4),0)

    def test_native_scene_move_remove(self):
        folder=Path(os.environ['GUILD_PLACEMENT_NATIVE_DIR'])
        lab=ProjectionLab(self.after)
        removed=[]
        lab.hooks[0x107b660]=lambda:(removed.append((lab.arg(),lab.arg(1))),lab.ret(8,1))
        lab.hooks[0x1079f10]=lambda:lab.ret(16)
        record=struct.pack('<IIIBHhhI',1,71021,29,0,0,902,276,lab.now+180)
        lab.snapshot(record)
        move=(folder/'move-777.bin').read_bytes()
        lab.now=struct.unpack_from('<I',move,35)[0]-180
        lab.packet(0x1068760,move);lab.run(0x106ecb0)
        self.assertEqual(lab.created[-1]['position'],[938,276,0])
        self.assertEqual(lab.created[-1]['key'],1)
        self.assertEqual(removed[-1],(1,1))
        count=len(lab.created)
        lab.packet(0x1068760,(folder/'remove-777.bin').read_bytes());lab.run(0x106ecb0)
        self.assertEqual(len(lab.created),count)
        self.assertEqual(removed[-1],(1,1))
        # Native erase clears identity and sets area=-1; it retains the timer.
        self.assertEqual(bytes(lab.u.mem_read(lab.DATA+5,12)),bytes(12))
        self.assertEqual(lab.u.mem_read(lab.DATA+17,1),b'\xff')

    def test_practice_scoreboard_has_no_opponent(self):
        lab=ProjectionLab(self.after);strings=[];messages=[]
        lab.hooks.update({
            0x10728c0:lambda:lab.ret(value=lab.MANAGER),
            0x39a6620:lambda:(strings.append(lab.arg()),lab.ret(value=lab.TEMP)),
            0x32bea50:lambda:(messages.append(lab.arg()),lab.ret(12)),
        })
        lab.put(lab.DATA+0x2e41,0)
        self.assertEqual(lab.run(0xef7bd0,(0,0),ecx=lab.CONTROL)&255,0)
        self.assertEqual(strings,[100000005])
        self.assertEqual(messages,[726])
        lab.put(lab.DATA+0x2e41,123)
        self.assertEqual(lab.run(0x10663c0)&255,1)

    def test_exact_gate_delta(self):
        for cls in (0,1,2,255):
            old=native(self.before,(cls,),range(65536));new=native(self.after,(cls,),range(65536))
            changed=[i for i,(a,b) in enumerate(zip(old,new)) if a!=b]
            self.assertEqual(changed,[838,839] if cls==1 else [])
            for i in changed:self.assertEqual((old[i],new[i]),(0,1))

    def test_boundary_and_image(self):
        end=0
        for lo,hi in self.receipt['changedRanges']:
            self.assertEqual(self.before[end:lo],self.after[end:lo]);end=hi
        self.assertEqual(self.before[end:],self.after[end:])
        for invalid in (self.after,self.before[:-1],b'X'+self.before[1:]):
            with self.assertRaises(ValueError):patch.build(invalid)
        with tempfile.TemporaryDirectory() as tmp:
            dest=Path(tmp)/'DNF.exe';patch.write_candidate(self.source,dest)
            self.assertEqual(dest.read_bytes(),self.after)
            with self.assertRaises(ValueError):patch.write_candidate(self.source,dest)
            if os.name=='nt':windows_image(dest)

if __name__=='__main__':unittest.main()
