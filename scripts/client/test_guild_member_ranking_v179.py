"""Execute V179 member query sender, response reader and three UI text setters.

Socket, allocation and string resource helpers are bounded adapters. The native
field reads, float conversion, model copy and window update order run unchanged.
"""
import json
import os
from pathlib import Path
import struct
import unittest

import patch_receivers_v128 as pe
from test_guild_ranking_board_native import BoardNative
from unicorn.x86_const import UC_X86_REG_ESP, UC_X86_REG_ECX

V179 = '72254abd5214bc24f8bcb76a16184e07105dde33f4e762b4c71c2e264bc7789c'


class MemberNative(BoardNative):
    WINDOW = 0x11003000

    def __init__(self, data):
        super().__init__(data)
        self.put(0x56a3798, self.MANAGER)
        self.labels, self.formatted, self.resources, self.titles = [], [], [], []
        self.text = 0x11007000
        for i, offset in enumerate((0x1f4, 0x1fc, 0x204)):
            widget = 0x11004000 + i*32
            self.put(self.WINDOW+offset, widget)
            self.put(widget, 0x20000600)
        self.put(0x200006e4, 0x20000800)

    def hook(self, u, at, size, context):
        sp = self.u.reg_read(UC_X86_REG_ESP)
        ecx = self.u.reg_read(UC_X86_REG_ECX)
        arg = lambda i: self.read(sp+4*i)
        if at == 0x3c1b570:
            self.ret(ecx)
        elif at == 0x32b5c90:
            assert (arg(1),arg(2)) == (0xb6,0)
            self.ret(self.WINDOW,8)
        elif at == 0x39a6620:
            self.resources.append(arg(1))
            self.u.mem_write(self.text, ('resource '+str(arg(1))+'\0').encode('utf-16le'))
            self.ret(self.text)
        elif at == 0x37e0840:
            value = arg(3)
            assert value < 0x80000000
            self.formatted.append(value)
            self.u.mem_write(self.text, (str(value)+'\0').encode('utf-16le'))
            self.ret(arg(1))
        elif at == 0x1331a10:
            self.ret(self.text)
        elif at == 0x20000800:
            self.labels.append(self.wbytes(arg(1)).decode('utf-16le'))
            self.ret(0,4)
        elif at == 0x33617e0:
            self.titles.append(self.resources[-1])
            self.ret(0,36)
        elif at == 0x32bea50:
            self.titles.append(('error',self.resources[-1])); self.ret(0,12)
        elif at == 0x374f3f0:
            self.sent.append((at,arg(1))); self.ret(0x11008000,4)
        elif at == 0x374f550:
            self.sent.append((at,self.wbytes(arg(1)).decode('utf-16le')))
            self.ret(0x11008000,4)
        else:
            super().hook(u,at,size,context)


class MemberRankingNativeTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        source = os.environ.get('DNF_MEMBER_RANKING_CLIENT')
        wire = os.environ.get('GUILD_MEMBER_RANKING_WIRE_OUTPUT')
        if not source or not wire:
            raise unittest.SkipTest('requires matched client and exported Go replies')
        cls.data = Path(source).read_bytes()
        if pe.sha(cls.data) not in (V179, '4bc360ccbdb5f569553d49813705c1afa4ec73ed6ff25ae7105fcb8fc2e41a64'):
            raise ValueError('exact V179 required')
        cls.packets = {k:bytes.fromhex(v) for k,v in json.loads(Path(wire).read_text()).items()}

    def test_native_sender_identity_layout(self):
        n=MemberNative(self.data)
        n.put(n.WINDOW+0x230,123)
        n.u.mem_write(n.WINDOW+0x22c,b'\x00')
        n.wide(n.WINDOW+0x20c,'成员'.encode('utf-16le'))
        n.run(0x11ac5c0,ecx=n.WINDOW)
        self.assertEqual(n.sent,[(0x374f280,1505),(0x374f3f0,123),(0x374f390,0),(0x374f550,'成员'),('flush',0)])
        self.assertEqual(n.titles,[100002194])

    def test_production_reader_float_conversion_and_labels(self):
        n=MemberNative(self.data)
        for name,body in self.packets.items():
            with self.subTest(name=name):
                n.labels=[]; n.formatted=[]; n.titles=[]
                n.run(0x11b0a80,(0,body[0],0),payload=body[1:])
                self.assertEqual(bytes(n.u.mem_read(n.WINDOW+0x1dc,16)),body[1:])
                expected=[int(x) for x in struct.unpack_from('<3f',body,5)]
                self.assertEqual(n.formatted,expected)
                self.assertEqual(n.labels,[str(x) for x in expected])
                self.assertEqual(n.titles,[100002195])

    def test_error_response_uses_native_message(self):
        n=MemberNative(self.data)
        n.run(0x11b0a80,(0,0,100))
        self.assertEqual(n.titles,[('error',100002196)])
        self.assertEqual(n.labels,[])


if __name__ == '__main__': unittest.main()
