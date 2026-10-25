"""Bounded V171 receivers/setter and factor-selector execution with Go packets.

Requires Unicorn and GUILD_BOARD_WIRE_OUTPUT exported by the Go handler test.
The socket reader, STL allocation/string helpers and UI resource calls are
replaced; native field reads, setter loop and score selectors run unchanged.
"""
import json
import os
from pathlib import Path
import struct
import unittest

from test_patch_guild_grade_region import Sender
from unicorn import UC_HOOK_CODE
from unicorn.x86_const import *
import patch_receivers_v128 as pe
import patch_guild_board_v171


class BoardNative:
    MANAGER, MODEL, ROWS = 0x11001000, 0x11002000, 0x11006000
    STOP = 0x20000000

    def __init__(self, data):
        self.u = Sender(data).u
        self.u.mem_map(0, 4096)
        self.put(0x56A378C, self.MANAGER)
        self.put(0x458820C, 0x20000200)
        self.u.mem_map(0x12000000, 0x10000)
        self.next_alloc = 0x12000000
        self.guilds, self.names = {}, {}
        self.progress = []
        self.sent = []
        self.payload, self.cursor, self.ui = b'', 0, []
        self.handle = self.u.hook_add(UC_HOOK_CODE, self.hook)

    def read(self, at): return struct.unpack('<I', self.u.mem_read(at, 4))[0]
    def put(self, at, value): self.u.mem_write(at, struct.pack('<I', value))
    def alloc(self, size):
        at = self.next_alloc
        self.next_alloc += (size+15) & ~15
        assert self.next_alloc < 0x12010000
        return at
    def ret(self, value=0, pop=0):
        sp = self.u.reg_read(UC_X86_REG_ESP)
        self.u.reg_write(UC_X86_REG_EAX, value)
        self.u.reg_write(UC_X86_REG_ESP, sp+4+pop)
        self.u.reg_write(UC_X86_REG_EIP, self.read(sp))

    def wide(self, obj, data):
        self.u.mem_write(obj, bytes(32))
        if len(data) <= 14:
            self.u.mem_write(obj+4, data+b'\0\0')
            self.put(obj+24, 7)
        else:
            at = self.alloc(len(data)+2)
            self.u.mem_write(at, data+b'\0\0')
            self.put(obj+4, at)
            self.put(obj+24, len(data)//2)
        self.put(obj+20, len(data)//2)

    def wbytes(self, at):
        data = bytearray()
        while bytes(self.u.mem_read(at, 2)) != b'\0\0':
            data.extend(self.u.mem_read(at, 2)); at += 2
            assert len(data) < 128
        return bytes(data)

    def hook(self, _u, at, _size, _ctx):
        sp, ecx = self.u.reg_read(UC_X86_REG_ESP), self.u.reg_read(UC_X86_REG_ECX)
        arg = lambda i: self.read(sp+4*i)
        if at in (0x37E82E0, 0x37E8350, 0x37E8430):
            n = 1 if at == 0x37E82E0 else 2 if at == 0x37E8350 else arg(2)
            assert self.cursor+n <= len(self.payload)
            self.u.mem_write(arg(1), self.payload[self.cursor:self.cursor+n])
            self.cursor += n; self.ret(1)
        elif at == 0x8A9480: self.ret(self.alloc(arg(1)))
        elif at in (0x3E70882, 0x3E72CE4): self.ret()
        elif at == 0x3E734F0:
            self.u.mem_write(arg(1), bytes([arg(2)&255])*arg(3)); self.ret(arg(1))
        elif at == 0x360DE20:
            begin = self.read(ecx+4) or self.ROWS
            end = self.read(ecx+8) or begin
            self.u.mem_write(end, bytes(self.u.mem_read(arg(1), 72)))
            for offset, value in ((4, begin), (8, end+72), (12, end+72)): self.put(ecx+offset, value)
            self.ret(0, 4)
        elif at in (0x224E5F0, 0xA8F6C0):
            out = arg(1)
            for offset, value in ((0, self.read(ecx)), (4, 0), (8, self.read(ecx+(4 if at == 0x224E5F0 else 8)))):
                self.put(out+offset, value)
            self.ret(out, 4)
        elif at == 0x2986980:
            ident = arg(1)
            if ident not in self.guilds: self.guilds[ident] = self.alloc(1024)
            self.ret(self.guilds[ident], 4)
        elif at == 0x2986AB0: self.ret(self.guilds.get(arg(1),0),4)
        elif at == 0x37E0990: self.ret(936)
        elif at == 0x20000200:
            raw = bytes(self.u.mem_read(arg(3), arg(4)))
            value = raw.decode('gb18030').encode('utf-16le')
            assert len(value)//2 <= arg(6)
            self.u.mem_write(arg(5), value); self.ret(len(value)//2, 24)
        elif at == 0x45FDA0:
            value = bytes(self.u.mem_read(arg(1), arg(2)*2))
            self.names[ecx] = value.decode('utf-16le')
            self.wide(ecx, value); self.ret(ecx, 8)
        elif at == 0x45FEA0:
            self.wide(ecx, self.wbytes(arg(1))); self.ret(ecx, 4)
        elif at == 0x2984DB0:
            obj = sp+4
            src = self.read(obj+4) if self.read(obj+24) >= 8 else obj+4
            value = bytes(self.u.mem_read(src, self.read(obj+20)*2))
            self.names[ecx+0xA8] = value.decode('utf-16le')
            self.wide(ecx+0xA8, value); self.ret(0, 32)
        elif at == 0x11C40B0:
            self.u.mem_write(ecx, bytes(self.u.mem_read(arg(1), 20))); self.ret(ecx, 4)
        elif at == 0x11C6910:
            begin, end = arg(2), arg(3)
            self.ui.append((arg(6)&255, bytes(self.u.mem_read(begin, end-begin)) if begin else b''))
            self.ret()
        elif at == 0x2986C20: self.ret(self.MODEL, 4)
        elif at == 0xB43CF0: self.ret(ecx+236)
        elif at in (0x37E0840, 0x37DE240): self.ret(0x11007000)
        elif at == 0x1331A10: self.ret(0x11007000)
        elif at == 0x37DEAF0: self.ret()
        elif at == 0x39A6620: self.ret(0x11007000)
        elif at == 0x1C5B350: self.ret(ecx, 4)
        elif at in (0x20000400,): self.ret(0,4)
        elif at == 0x20000420:
            self.progress.append((arg(1),arg(2))); self.ret(0,8)
        elif at in (0x1AD59D0,0x209BBE0,0x37E9F40): self.ret(100)
        elif at == 0x37EA170: self.ret(0,8)
        elif at == 0x24A9760: self.ret(0x11007000)
        elif at == 0x374EA10: self.ret(0x11008000)
        elif at in (0x374F280,0x374F390,0x374F3C0):
            self.sent.append((at,arg(1))); self.ret(0x11008000,4)
        elif at == 0x3750AC0: self.sent.append(('flush',0)); self.ret()

    def run(self, at, args=(), payload=b'', ecx=None, pop=0):
        self.payload, self.cursor = payload, 0
        u, sp = self.u, 0x10008000
        saved = {UC_X86_REG_EBP:0x10009000, UC_X86_REG_EBX:0x12345678,
                 UC_X86_REG_ESI:0x22334455, UC_X86_REG_EDI:0x33445566}
        for reg, value in saved.items(): u.reg_write(reg, value)
        u.mem_write(sp, struct.pack('<'+'I'*(len(args)+1), self.STOP, *args))
        u.reg_write(UC_X86_REG_ESP, sp); u.reg_write(UC_X86_REG_ECX, self.MANAGER if ecx is None else ecx)
        try: u.emu_start(at, self.STOP, count=20000)
        except Exception as exc: raise AssertionError(f'native {at:x} stopped at {u.reg_read(UC_X86_REG_EIP):x}: {exc}') from exc
        assert u.reg_read(UC_X86_REG_EIP) == self.STOP, hex(u.reg_read(UC_X86_REG_EIP))
        assert u.reg_read(UC_X86_REG_ESP) == sp+4+pop
        assert all(u.reg_read(reg)==value for reg,value in saved.items())
        assert self.cursor == len(payload)
        return u.reg_read(UC_X86_REG_EAX)


class GuildBoardNativeTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        image, packets = os.environ.get('DNF_GUILD_BOARD_CLIENT'), os.environ.get('GUILD_BOARD_WIRE_OUTPUT')
        if not image or not packets: raise unittest.SkipTest('requires matched client and exported Go packets')
        cls.data = Path(image).read_bytes()
        if pe.sha(cls.data) not in ('c962e82d2258e15edb0ac372f1a4e3b58a1360ee53192201720f723302be5409', 'e1e78b6792e4d22ddae10e502d5b40555fb7a5f657f185578992eaefbe1348d4'):
            raise ValueError('exact V171 or four-receiver cumulative input required')
        if pe.sha(cls.data) == patch_guild_board_v171.INPUT_SHA256:
            cls.data, _ = patch_guild_board_v171.build(cls.data)
        cls.packets = {k:bytes.fromhex(v) for k,v in json.loads(Path(packets).read_text()).items()}

    def test_production_podium_full_receiver_and_native_cache(self):
        n = BoardNative(self.data); body = self.packets['podium']
        n.run(0x1FB5A70, (0,body[0]), body[1:])
        self.assertEqual([n.read(n.MANAGER+0x220+4*i) for i in range(3)], [1,2,0])
        for i in range(2):
            raw = body[6+72*i:6+72*(i+1)]; ident, rank, score = struct.unpack_from('<IIf',raw)
            self.assertEqual(bytes(n.u.mem_read(n.MANAGER+0x228+rank*4,4)), raw[8:12])
            obj = n.guilds[ident]
            self.assertEqual(n.names[obj+12], raw[15:38].split(b'\0')[0].decode('gb18030'))
            self.assertEqual(n.names[obj+0xA8], raw[38:68].split(b'\0')[0].decode('gb18030'))
            self.assertEqual(bytes(n.u.mem_read(obj+0xE8,2)),raw[68:70])
        self.assertEqual(n.ui[-1][0], 0)
        # Failure leaves the settled cache; successful empty month clears it.
        n.run(0x1FB5A70,(0,0))
        self.assertEqual(n.read(n.MANAGER+0x220),1)
        body=self.packets['empty_month'];n.run(0x1FB5A70,(0,body[0]),body[1:])
        self.assertEqual(bytes(n.u.mem_read(n.MANAGER+0x220,27)),bytes(27))

    def test_complete_native_senders_match_handler_requests(self):
        n=BoardNative(self.data)
        n.run(0x2983710)
        self.assertEqual(n.sent,[(0x374F280,1504),(0x374F390,1),(0x374F390,0),(0x374F3C0,1),('flush',0)])
        n.sent=[]
        n.run(0x29836C0,(0,0,2))
        self.assertEqual(n.sent,[(0x374F280,1504),(0x374F390,0),(0x374F390,0),(0x374F3C0,2),('flush',0)])

    def test_list_dispatch_does_not_overwrite_hall(self):
        n=BoardNative(self.data);n.put(n.MANAGER+0x220,99)
        body=self.packets['list'];n.run(0x1FB5A70,(0,body[0]),body[1:])
        self.assertEqual(n.ui[-1],(1,body[6:]))
        self.assertEqual(n.read(n.MANAGER+0x220),99)

    def test_production_model_copy_rank_and_float_getters(self):
        n=BoardNative(self.data);body=self.packets['model']
        n.run(0x1FC0E70,(0,body[0]),body[1:])
        self.assertEqual(bytes(n.u.mem_read(n.MODEL+236,108)),body[1:])
        for i in range(3):
            self.assertEqual(n.run(0x2983420,(i,),ecx=n.MODEL,pop=4),struct.unpack_from('<I',body,5+8*i)[0])
            at=0x20000100+64*i
            code=b'\xb9'+struct.pack('<I',n.MODEL)+b'\x6a'+bytes([i])+b'\xe8'+struct.pack('<i',0x2983460-(at+12))+b'\xd9\x1d'+struct.pack('<I',0x11005000)+b'\xc3'
            n.u.mem_write(at,code);n.run(at)
            self.assertEqual(bytes(n.u.mem_read(0x11005000,4)),body[9+8*i:13+8*i])

    def test_factor_selectors_and_current_progress_widget_from_received_model(self):
        # Enter the real renderer and stop after native selection, or after
        # the progress widget's virtual setter. Only drawing resources mocked.
        expected=[(50,25,100),(30,10,90),(90,60,120),(100,35,500),
                  (4000,2000,9450),(50,20,60),(700,350,7000),(40,15,0),(90,45,0)]
        for index,want in enumerate(expected):
            with self.subTest(index=index):
                n=BoardNative(self.data);body=self.packets['model']
                n.run(0x1FC0E70,(0,1),body[1:])
                ui,widget,vtable=0x11003000,0x11004000,0x20000300
                n.put(ui,index);n.put(widget,vtable)
                for offset in (0x3c,0x44,0x4c):n.put(ui+offset,widget)
                for offset in (8,0xdc,0xe4):n.put(vtable+offset,0x20000400)
                n.put(vtable+0xe0,0x20000420)
                n.u.reg_write(UC_X86_REG_ESP,0x10008000)
                n.u.reg_write(UC_X86_REG_ECX,ui)
                n.put(0x10008000,n.STOP)
                n.u.emu_start(0x11A7D60,0x11A8249,count=5000)
                self.assertEqual(n.u.reg_read(UC_X86_REG_EIP),0x11A8249)
                bp=n.u.reg_read(UC_X86_REG_EBP)
                got=(n.read(bp-0x7c),n.read(bp-0x78),n.read(bp-0x74))
                self.assertEqual(got,want)
                if index<7:
                    n.u.emu_start(0x11A8249,0x11A8384,count=5000)
                    self.assertEqual(n.u.reg_read(UC_X86_REG_EIP),0x11A8384)
                    self.assertEqual(n.progress,[(want[0],want[2])])

    def test_successful_missing_month_selects_native_counting_text(self):
        n=BoardNative(self.data);body=self.packets['empty_month']
        n.run(0x1FB5A70,(0,1),body[1:])
        # Renderer per-slot branch after font acquisition. Real cleared cache
        # supplies both ID and score; stop at the actual DSTR lookup call.
        for index in range(3):
            n.u.reg_write(UC_X86_REG_EBP,0x10009000)
            n.u.reg_write(UC_X86_REG_ESP,0x10008000)
            n.u.reg_write(UC_X86_REG_EBX,n.MANAGER+0x22c+4*index)
            n.u.reg_write(UC_X86_REG_EDI,n.read(n.MANAGER+0x220+4*index))
            n.u.emu_start(0x2987999,0x39A6620,count=100)
            self.assertEqual(n.u.reg_read(UC_X86_REG_EIP),0x39A6620)
            self.assertEqual(n.read(n.u.reg_read(UC_X86_REG_ESP)+4),100002202)

    def test_sampler_fractional_member_score_uses_native_integer_zero(self):
        n=BoardNative(self.data);body=self.packets['low_model']
        n.run(0x1FC0E70,(0,1),body[1:])
        self.assertAlmostEqual(struct.unpack_from('<f',body,29)[0],0.2)
        ui=0x11003000
        n.put(ui,0)
        n.u.reg_write(UC_X86_REG_ESP,0x10008000)
        n.u.reg_write(UC_X86_REG_ECX,ui);n.put(0x10008000,n.STOP)
        n.u.emu_start(0x11A7D60,0x11A8249,count=5000)
        self.assertEqual(n.u.reg_read(UC_X86_REG_EIP),0x11A8249)
        bp=n.u.reg_read(UC_X86_REG_EBP)
        self.assertEqual((n.read(bp-0x7c),n.read(bp-0x74)),(0,100))

    def test_current_total_display_reads_period_zero_from_received_model(self):
        n=BoardNative(self.data);body=self.packets['model']
        n.run(0x1FC0E70,(0,1),body[1:])
        n.u.reg_write(UC_X86_REG_ESP,0x10008000)
        n.u.reg_write(UC_X86_REG_EBP,0x10009000)
        n.u.reg_write(UC_X86_REG_EDI,n.MODEL)
        n.u.emu_start(0x11A931D,0x11A9338,count=2000)
        self.assertEqual(n.u.reg_read(UC_X86_REG_EIP),0x11A9338)
        self.assertEqual(n.u.reg_read(UC_X86_REG_EBX),int(struct.unpack_from('<f',body,9)[0]))
        self.assertEqual(n.u.reg_read(UC_X86_REG_EDI),struct.unpack_from('<I',body,5)[0])


if __name__ == '__main__': unittest.main()
