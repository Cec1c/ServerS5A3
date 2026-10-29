"""Native trade menu, peer prompts and handshake receivers with explicit UI adapters."""
import base64
import hashlib
import json
import os
from pathlib import Path
import struct
import unittest
from collections import deque

from test_guild_town_pvp_v179 import TownPVPNative
from unicorn.x86_const import *

CLIENT_SHAS = {
    '0cfc74d29de563c6612b2118a47f5f731c80f3e5a16b17b9e0bfd70fbd93acc9',  # V186
    '01b313275faf7b598211f4d8adb932e1b31ce4347827583a2df3cfa4d350e94c',  # cumulative profile candidate
}


class TradeNative(TownPVPNative):
    TRADE = 0x2001A000
    WINDOW = 0x20016000
    ITEM = 0x2001D000

    def __init__(self, data, local=1):
        super().__init__(data)
        self.history=deque(maxlen=60)
        self.local, self.opened, self.checked_reads = local, [], []
        self.moved, self.slot_reads = [], []
        self.put(self.ITEM,0x2001E000)
        for off,fn in ((4,0x22000200),(40,0x22000210),(72,0x22000220),(84,0x22000230),(76,0x22000240)):
            self.put(0x2001E000+off,fn)
        self.put(0x2001F000+24,3037)
        self.put(0x56A37CC, self.TRADE)
        self.put(self.TRADE+4, 0xFFFF)
        self.put(0x568A50C, 0x2001B100)
        self.put(0x2001B140, 0xFFFFFFFF)
        self.hooks.pop(0x8BAB80, None)
        self.hooks.update({
            0x225F6C0: lambda: self.ret(value=0x2001B000),
            0x225A0F0: lambda: self.ret(4,0),
            0x296DF10: lambda: self.ret(4,1),
            0x32B4BE0: lambda: self.ret(4,1),
            0x3531D80: lambda: self.ret(value=1000000),
            0x29E7BA0: lambda: self.ret(),
            0x209BBE0: lambda: self.ret(value=0),
            0x37E9F40: lambda: self.ret(value=0),
            0x35CA060: lambda: self.ret(value=0),
            0x3810650: lambda: self.ret(value=self.CHAT),
            0x2A02AC0: lambda: self.ret(4),
            0x2A00580: lambda: self.ret(32),
            0x8EF130: lambda: self.ret(value=0),
            0x32BC210: self.open_trade,
            0x3816100: lambda: self.ret(4),
            0x3E743D8: self.localtime,
            0x3E77844: lambda: self.ret(value=1793100000),
            0x32B4C00: lambda: self.ret(value=self.WINDOW),
            0x331DAF0: lambda: self.ret(value=self.read(self.WINDOW+0x260)),
            0x3553560: lambda: self.ret(),
            0x32C7DB0: lambda: self.ret(),
            0x25F3F10: lambda: self.ret(),
            0x8A9480: lambda: self.ret(value=0x2001B300),
            0xB36EF0: lambda: self.ret(4),
            0x25EB680: self.item_lookup,
            0x2531A80: lambda: self.ret(value=0x2001F000),
            0x17C1FF0: lambda: self.ret(4),
            0x2531940: lambda: self.ret(value=0),
            0x25F04E0: self.move_slot,
            0x1819820: lambda: self.ret(),
            0x32B6810: lambda: self.ret(8),
            0x1CC3D80: lambda: self.ret(20),
            0x20A3150: lambda: self.ret(),
            0x20A2250: lambda: self.ret(4),
            0x29610E0: lambda: self.ret(value=self.ACTOR),
            0x331DB90: lambda: self.ret(value=0),
            0x331DC80: lambda: self.ret(value=0),
            0x25EA540: lambda: self.ret(value=self.ITEM),
            0x26072B0: lambda: self.ret(),
            0x22000200: lambda: self.ret(value=3037),
            0x22000210: lambda: self.ret(value=0x2001F000),
            0x22000220: lambda: self.ret(value=1000),
            0x22000230: lambda: self.ret(value=0x2000B800),
            0x22000240: lambda: self.ret(4),
        })
        for address in (0x37DF130,0x37DF1B0,0x37DF230,0x37DF2B0,0x37DF010):
            self.hooks[address] = self.protected_write
        # Check bounds before allowing the actual scalar readers to execute.
        for address,width in ((0x37E82E0,1),(0x37E8350,2),(0x37E83C0,4)):
            self.hooks[address] = lambda width=width: self.check_read(width)
        self.put(self.dialog+0x1C0,5,42,1,1)
        self.put(self.dialog,0x20012000)
        self.put(0x20012000+124,0x22000100)
        self.put(self.dialog+432,0x20013000)
        self.put(self.dialog+440,0x20013100)

    def protected(self):
        ecx = self.reg(UC_X86_REG_ECX)
        value = self.local if ecx == 0x57B7218 else 0
        self.put(self.arg(1),value)
        self.ret(8)

    def hook(self,u,at,size,ctx):
        if at<0x4000000:self.history.append(hex(at))
        super().hook(u,at,size,ctx)

    def protected_write(self):
        self.put(self.reg(UC_X86_REG_ECX),self.read(self.arg()))
        self.ret(8)

    def localtime(self):
        self.put(0x2001B200+20,125)
        self.ret(value=0x2001B200)

    def item_lookup(self):
        slot=self.arg()&65535
        self.slot_reads.append(slot)
        self.ret(4,self.ITEM if slot==3 else 0)

    def move_slot(self):
        self.moved.append([self.arg(i) for i in range(4)])
        self.ret(16)

    def open_trade(self):
        self.opened.append([self.arg(i) for i in range(3)])
        self.ret(12)

    def check_read(self,width):
        remaining=self.read(0x57F9AD4)
        self.checked_reads.append(width)
        assert remaining >= width, ('packet underflow',remaining,width)

    def receive(self,classification,op,body):
        callbacks={(1,10):0x1FC2BE0,(1,11):0x1FC3760,(0,7):0x2035F40,
                   (0,8):0x20364A0,(0,17):0x200B300,(1,24):0x1FBAED0,
                   (0,16):0x200AF50,(0,18):0x20121F0}
        self.put(0x2000C000,callbacks[classification,op],0)
        wire=bytearray(16)+body
        wire[0]=classification
        struct.pack_into('<H',wire,1,op)
        struct.pack_into('<I',wire,3,len(wire))
        self.u.mem_write(self.PACKET,bytes(wire))
        try:
            if classification==0:
                # Class-0 transport queue is an adapter; registration and the
                # entire native notification receiver were matched in input.
                self.u.mem_write(self.PACKET,body)
                self.put(0x57F9AD0,self.PACKET,len(body),0,0)
                self.run(callbacks[classification,op],(op,0),pop=0)
            else:self.run(0x25239A0,(0,self.PACKET,0),ecx=self.NETWORK)
        except Exception as exc:
            raise AssertionError(str(exc)+f' class={classification} op={op} pc={self.reg(UC_X86_REG_EIP):x} trace={list(self.history)}') from exc
        assert self.read(0x57F9AD4)==0


class TradeInvitationTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        path=os.environ.get('DNF_TRADE_INVITATION_CLIENT')
        if not path: raise unittest.SkipTest('matching cumulative client required')
        cls.data=Path(path).read_bytes()
        assert hashlib.sha256(cls.data).hexdigest() in CLIENT_SHAS

    def test_native_menu_to_packed_manager_and_request(self):
        lab=TradeNative(self.data)
        lab.put(lab.STACK+0x2000-0x238,5)
        lab.u.reg_write(UC_X86_REG_EBP,lab.STACK+0x2000)
        lab.u.reg_write(UC_X86_REG_ESP,lab.STACK)
        lab.u.emu_start(0x353B550,0x353B562,count=100000)
        self.assertEqual(lab.reg(UC_X86_REG_EIP),0x353B562)
        self.assertEqual(lab.sent,[(10,struct.pack('<HBII',5,1,1000000,0))])
        busy=TradeNative(self.data);busy.put(busy.TRADE+4,5)
        busy.run(0x2606500,(5,),ecx=busy.TRADE)
        self.assertEqual(busy.sent,[])
        self.assertIn(420,busy.messages)

    def test_accept_and_native_reject_have_different_lengths(self):
        lab=TradeNative(self.data)
        lab.run(0x34E2480,ecx=lab.dialog)
        self.assertEqual(lab.sent,[(11,struct.pack('<HBI',5,1,1000000))])
        self.assertEqual(lab.closed,1)
        lab=TradeNative(self.data);lab.click=0x20013100
        lab.put(lab.STACK,lab.STOP)
        lab.u.reg_write(UC_X86_REG_ESP,lab.STACK)
        lab.u.reg_write(UC_X86_REG_ECX,lab.dialog)
        # Stop after the native close callback; later widget maintenance is
        # outside the invitation sender and is deliberately not emulated.
        lab.u.emu_start(0x34E41C0,0x34E48FE,count=100000)
        self.assertEqual(lab.reg(UC_X86_REG_EIP),0x34E48FE)
        self.assertEqual(lab.sent,[(11,struct.pack('<HBIH',5,1,1000000,133))])
        self.assertEqual(lab.closed,1)

    def test_failure_envelopes_consume_mode_without_opening_trade(self):
        for op in (10,11):
            lab=TradeNative(self.data)
            with self.assertRaisesRegex(AssertionError,'underflow'):
                lab.receive(1,op,bytes((0,19)))
            lab=TradeNative(self.data)
            lab.receive(1,op,bytes((0,19,1)))
            self.assertEqual(lab.opened,[])
            self.assertTrue(lab.messages)

    def test_actual_go_invitation_acceptance_and_locked_states(self):
        name=os.environ.get('DNF_TRADE_INVITATION_PACKETS')
        if not name:self.skipTest('actual Go packet export required')
        rows=json.loads(Path(name).read_text('utf-8'))
        labs={'requester':TradeNative(self.data,1),'recipient':TradeNative(self.data,5)}
        seen=set()
        for row in rows:
            if row['stage'] not in ('invite','accept','locked','one-consent'):continue
            op,classification=row['opcode'],row['class']
            if (classification,op) not in ((1,10),(1,11),(0,7),(0,8),(0,17),(1,24)):continue
            body=base64.b64decode(row['body'])
            lab=labs[row['peer']]
            if op==8:
                old=TradeNative(self.data)
                with self.assertRaisesRegex(AssertionError,'underflow'):
                    old.receive(0,8,body[:7])
            lab.receive(classification,op,body)
            seen.add((row['stage'],row['peer'],classification,op))
        self.assertIn(('accept','requester',0,8),seen)
        self.assertIn(('accept','recipient',1,11),seen)
        for name,lab in labs.items():
            self.assertEqual(len(lab.opened),1,name)
            self.assertEqual([lab.read(lab.TRADE+8),lab.read(lab.TRADE+12)],[1,1],name)
            self.assertEqual(lab.u.mem_read(lab.TRADE+0x54,1),b'\1',name)

    def test_actual_go_completion_rows_and_empty_handshake_rollback(self):
        name=os.environ.get('DNF_TRADE_INVITATION_PACKETS')
        if not name:self.skipTest('actual Go packet export required')
        rows=json.loads(Path(name).read_text('utf-8'))
        seen=0
        for row in rows:
            if row['class']!=0 or row['opcode']!=18:continue
            lab=TradeNative(self.data)
            lab.put(lab.TRADE+4,5)
            lab.receive(0,18,base64.b64decode(row['body']))
            self.assertEqual(lab.read(lab.TRADE+4)&65535,65535)
            self.assertIn(644,lab.messages)
            seen+=1
        self.assertEqual(seen,2)
        lab=TradeNative(self.data)
        lab.put(lab.TRADE+4,5)
        lab.receive(0,16,b'\0\0')
        self.assertEqual(lab.read(lab.TRADE+4)&65535,65535)
        self.assertIn(643,lab.messages)


if __name__=='__main__':unittest.main()
