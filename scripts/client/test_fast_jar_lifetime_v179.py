"""V179 bounded inventory lifetime regression for consecutive lottery opens.

The original CMD27 continuation, NOTI13 clear and NOTI14 existing-stack branch
execute machine code. Allocator, virtual item accessors, encrypted integers and
UI effects are adapters. No game executable is launched or patched.
"""
import hashlib
import os
from pathlib import Path
import struct
import unittest

from unicorn import UcError
from unicorn.x86_const import *
from test_guild_war_refresh_v173 import WarNative

SHA = '72254abd5214bc24f8bcb76a16184e07105dde33f4e762b4c71c2e264bc7789c'


class FastJarNative(WarNative):
    ITEM, VT, TEMPLATE = 0x24000000, 0x20010000, 0x20010800
    CONTRACT, BAG, SLOTS, FRAME = 0x20011000, 0x20012000, 0x20013000, 0x21010000
    DELETE, GET_TEMPLATE, SAME, COUNT, SET = (0x23010000 + 16*i for i in range(5))

    def __init__(self, data):
        super().__init__(data)
        self.u.mem_map(self.ITEM, 4096)
        self.put(self.ITEM, self.VT)
        self.put(self.VT+0xC, self.DELETE)
        self.put(self.VT+0x28, self.GET_TEMPLATE)
        self.put(self.VT+0x5C, self.SAME)
        self.put(self.VT+0x48, self.COUNT)
        self.put(self.VT+0x40, self.SET)
        self.put(self.VT+0x44, self.SET)
        self.put(self.VT+0x50, 0x23010110)
        self.put(self.TEMPLATE+0x18, 900)
        self.put(self.CONTRACT+8, self.ITEM, 1, 20)
        self.put(0x568BDF4, self.CONTRACT)
        self.put(0x56A379C, self.BAG)
        self.put(0x568C0FC, 0x2001E000)
        self.put(self.BAG+0x38, 0)
        self.put(self.BAG+0x48, 80)
        self.put(self.BAG+0x58, self.SLOTS, self.SLOTS+80*4)
        self.put(self.SLOTS+75*4, self.ITEM)
        self.opened, self.deleted, self.count = [], [], 21
        self.hooks.update({
            self.DELETE:self.destroy,
            self.GET_TEMPLATE:lambda:self.ret(value=self.TEMPLATE),
            self.SAME:lambda:self.ret(4, int(self.arg()==900)),
            self.COUNT:lambda:self.ret(value=self.count), self.SET:self.set_count,
            0x23010110:self.consume_count, 0x25EE980:self.remove_slot,
            0x3E4B210:lambda:self.ret(), 0x32BEA50:self.open_window,
            0x2326360:lambda:self.ret(4,3),
            0x1CC3D80:lambda:self.ret(20), 0x20A3150:lambda:self.ret(4),
            0x20A2250:lambda:self.ret(),
            0x2142F90:lambda:self.ret(), 0x25EDB70:lambda:self.ret(value=0),
            0x1C93190:lambda:self.ret(), 0x1C92CC0:lambda:self.ret(8),
            0x25EABC0:lambda:self.ret(), 0x1C6D840:lambda:self.ret(),
            0x25EB680:lambda:self.ret(4,self.read(self.SLOTS+self.arg()*4)),
            0x1D444F0:lambda:self.ret(4), 0x1819820:lambda:self.ret(),
            0x127A480:lambda:self.ret(4), 0x1277C40:lambda:self.ret(),
            0x116ACD0:lambda:self.ret(value=0),
        })
        for va in (0x37DF550,0x37DF5C0,0x37DF630,0x37DF6A0,0x37DF7F0):
            self.hooks[va] = self.read_integer
        for va in (0x37DF130,0x37DF1B0,0x37DF230,0x37DF2B0,0x37DF010):
            self.hooks[va] = self.write_integer

    def read_integer(self):
        self.put(self.arg(1), self.read(self.reg(UC_X86_REG_ECX)+4)); self.ret(8)

    def write_integer(self):
        self.put(self.reg(UC_X86_REG_ECX)+4,self.read(self.arg())); self.ret(8)

    def destroy(self):
        assert self.arg()==1
        at=self.reg(UC_X86_REG_ECX)
        self.deleted.append(at)
        self.u.mem_unmap(at,4096)
        self.ret(4)

    def set_count(self):
        self.count=self.arg(); self.ret(8)

    def consume_count(self):
        self.count-=self.arg(); self.ret(4)

    def remove_slot(self):
        slot=self.SLOTS+self.arg()*4
        at=self.read(slot)
        if at:
            self.deleted.append(at)
            self.u.mem_unmap(at,4096)
            self.put(slot,0)
        self.ret(4)

    def consume_source(self):
        self.put(self.FRAME-0x464,75)
        self.fragment(0x1FE217B,0x1FE21FA,{UC_X86_REG_EBX:0,UC_X86_REG_ECX:75})

    def open_window(self):
        self.opened.append((self.arg(),self.arg(1)))
        self.ret(12)

    def fragment(self,start,end,registers=None):
        self.u.reg_write(UC_X86_REG_ESP,self.STACK)
        self.u.reg_write(UC_X86_REG_EBP,self.FRAME)
        self.u.reg_write(UC_X86_REG_EFLAGS,2)
        for r,v in (registers or {}).items():self.u.reg_write(r,v)
        self.u.emu_start(start,end,count=10000)
        assert self.reg(UC_X86_REG_EIP)==end,hex(self.reg(UC_X86_REG_EIP))
        assert self.reg(UC_X86_REG_ESP)==self.STACK,'fragment stack balance'

    def settle_and_continue(self):
        # Reward virtual template is null: the native branch skips auto-selling
        # but still executes the real active-contract and remaining-count gates.
        reward,vt=0x20015000,0x20016000
        self.put(reward,vt);self.put(vt+0x28,0x23010100)
        self.hooks[0x23010100]=lambda:self.ret(value=0)
        self.fragment(0x1FE2C95,0x1FE2D5A,{UC_X86_REG_ESI:reward})

    def full_bag_refresh(self):
        self.run(0x25FEB80,(0,),ecx=self.BAG)

    def update_source(self,count):
        self.u.mem_write(self.FRAME-0x17C,struct.pack('<HII',75,900,count))
        self.fragment(0x2026384,0x2026B36,{UC_X86_REG_EBX:self.BAG})


class FastJarLifetimeTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        path=os.environ.get('DNF_FAST_JAR_EXE')
        if not path:raise unittest.SkipTest('set DNF_FAST_JAR_EXE to V179')
        cls.data=Path(path).read_bytes()
        assert hashlib.sha256(cls.data).hexdigest()==SHA,'exact V179 required'

    def test_old_refresh_frees_cached_source_before_second_settlement(self):
        n=FastJarNative(self.data)
        n.consume_source()
        n.settle_and_continue()
        self.assertEqual(n.opened,[(0x1AB,n.ITEM)])
        n.full_bag_refresh()
        self.assertEqual(n.deleted,[n.ITEM])
        self.assertEqual(n.read(n.CONTRACT+8),n.ITEM)
        self.assertEqual(n.read(n.SLOTS+75*4),0)
        with self.assertRaises(UcError):n.settle_and_continue()
        self.assertEqual(n.reg(UC_X86_REG_EIP),0x232651C)

    def test_incremental_source_keeps_identity_for_twenty_one_opens(self):
        n=FastJarNative(self.data)
        for remaining in range(20,0,-1):
            n.consume_source()
            n.put(n.CONTRACT+0x10,remaining)
            n.settle_and_continue()
            n.update_source(remaining)
            self.assertEqual(n.count,remaining)
            self.assertEqual(n.read(n.SLOTS+75*4),n.ITEM)
        n.put(n.CONTRACT+0x10,0)
        n.consume_source()
        n.settle_and_continue()
        self.assertEqual(len(n.opened),20)
        self.assertEqual(n.deleted,[n.ITEM])

    def test_go_production_source_deltas_keep_native_cached_object(self):
        folder=os.environ.get('DNF_FAST_JAR_PACKET_DIR')
        if not folder:self.skipTest('set DNF_FAST_JAR_PACKET_DIR to Go output')
        for paid in ('true','false'):
            n=FastJarNative(self.data)
            for opened in range(1,22):
                files=sorted(Path(folder).glob(f'paid-{paid}-open-{opened:02d}-*.bin'))
                self.assertGreaterEqual(len(files),2)
                self.assertIn('op27.bin',files[0].name)
                n.consume_source()
                n.put(n.CONTRACT+0x10,21-opened)
                n.settle_and_continue()
                for file in files[1:]:
                    self.assertIn('op14.bin',file.name)
                    body=file.read_bytes()
                    for at in range(3,len(body),119):
                        slot,item,count=struct.unpack_from('<HII',body,at)
                        if slot==75 and item==900:n.update_source(count)
                self.assertEqual(n.read(n.CONTRACT+8),n.ITEM)
                self.assertEqual(n.read(n.SLOTS+75*4),n.ITEM if opened<21 else 0)
            self.assertEqual(len(n.opened),20)

    def test_completed_batch_does_not_dereference_source(self):
        n=FastJarNative(self.data)
        n.put(n.CONTRACT+0x10,0)
        n.full_bag_refresh()
        n.settle_and_continue()
        self.assertEqual(n.opened,[])


if __name__=='__main__':unittest.main()
