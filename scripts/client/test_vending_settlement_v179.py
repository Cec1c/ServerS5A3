"""Matched V179 vending request, reply and actual settlement control flow.

UI, item resources and STL have adapters; transport parsing and settlement
decisions execute original x86. No game process, player data or network.
Set VENDING_NATIVE_EXE; optionally DNF_VENDING_NATIVE_EXPORT for Go replies.
"""
import hashlib, json, os, struct, unittest
from pathlib import Path
from test_guild_placement_v177 import PlacementLab
from unicorn.x86_const import *

class VendingLab(PlacementLab):
    def __init__(self,data):
        super().__init__(data)
        self.coin=self.TEMP+0x1000
        self.result=self.TEMP+0x1200
        self.effect=[]
        self.put(self.coin,self.TEMP+0x1400)
        self.put(self.TEMP+0x1400+0x40,self.STOP+0x100)
        self.put(self.TEMP+0x1400+0x48,self.STOP+0x120)
        self.hooks.update({
            0x25eca10:lambda:self.ret(4,121),
            0x374f3c0:self.write_u16,
            0x25eb680:lambda:self.ret(4,self.coin),
            0x25ece10:lambda:self.ret(4,0xffffffff),
            self.STOP+0x100:self.set_coin,
            self.STOP+0x120:lambda:self.ret(value=3),
            0x32b4d70:lambda:self.ret(value=self.CONTROL),
            0x3302820:lambda:self.ret(8,self.result),
            0x3302710:lambda:self.ret(4,0),
            0x8a9480:lambda:self.ret(value=self.alloc(self.arg())),
            0x1fe62b0:self.clear_vector,
            0x33044f0:lambda:(self.effect.append('finish'),self.ret()),
        })
    def write_u16(self):self.current+=struct.pack('<H',self.arg()&65535);self.ret(4)
    def set_coin(self):self.effect.append(('coin',self.arg(),self.arg(1)));self.ret(8)
    def send_native(self,machine):
        self.put(self.CONTROL+0x188,machine-1,self.coin)
        self.run(0x3302650,ecx=self.CONTROL)
        return self.sent[-1]
    def receive_native(self,body):
        self.put(self.CONTROL+0x1d8,0,0,0,0)
        self.u.mem_write(self.PACKET,body[1:])
        self.put(0x57f9ad0,self.PACKET,len(body)-1,0,0)
        self.run(0x1fe64d0,(215,body[0],body[1] if not body[0] else 0,0),pop=0)
        return dict(effects=self.effect,remaining=self.read(0x57f9ad4),reward=(self.read(self.CONTROL+0x1c0),self.read(self.CONTROL+0x1c4)))

class SettlementLab(VendingLab):
    def __init__(self,data):
        super().__init__(data)
        self.hooks.pop(0x33044f0)
        self.reward=self.TEMP+0x1800
        self.defn=self.TEMP+0x1a00
        self.window=self.TEMP+0x1c00
        self.reward_count=0; self.display=[];self.entries=[];self.has_reward=True;self.coin_count=3;self.reward_kind=1
        vt=self.TEMP+0x1e00
        self.put(self.reward,vt);self.put(self.defn+0x18,2)
        self.put(vt,self.STOP+0x200)
        self.put(vt+0x28,self.STOP+0x220)
        self.put(vt+0x40,self.STOP+0x240)
        self.put(vt+0x48,self.STOP+0x260)
        self.put(vt+0x5c,self.STOP+0x280)
        self.put(vt+0x114,self.STOP+0x2a0)
        self.put(vt+0x118,self.STOP+0x2a0)
        self.hooks.update({
            0x32bea50:lambda:self.ret(12,self.window),
            0x3e73cb6:lambda:self.ret(value=self.arg()),
            0x3302820:lambda:self.ret(8,self.reward),
            0x2531a80:lambda:self.ret(value=self.TEMP+0x1f00),
            0x25eb680:self.lookup_slot,
            0x2434af0:self.item_ctor,
            0x2a32b30:self.item_factory,
            0x25f04e0:self.insert_reward,
            0x25ee980:lambda:(self.effect.append(('remove_coin',self.arg())),self.ret(4)),
            0x985650:lambda:self.ret(),
            0x3419210:self.display_new,
            0x34195d0:self.display_delta,
            0x3e4b210:lambda:self.ret(),
            0x1d44730:lambda:self.ret(value=0),
            0x994e30:lambda:self.ret(value=0),
            0x33024c0:lambda:self.ret(4,0),
            0x3a2e8e0:lambda:self.ret(4,0),
            0x25531d0:lambda:self.ret(),
            0x296dd80:lambda:self.ret(4,self.SCENE),
            0x3a2d530:self.advance,
            self.STOP+0x200:lambda:self.ret(value=self.reward_kind),
            self.STOP+0x220:lambda:self.ret(value=self.defn),
            self.STOP+0x240:self.set_reward,
            self.STOP+0x260:lambda:self.ret(value=self.reward_count),
            self.STOP+0x280:lambda:self.ret(4,1),
            self.STOP+0x2a0:lambda:self.ret(4),
            self.STOP+0x120:lambda:self.ret(value=self.coin_count),
            # Item metadata/resource normalization: no network bytes consumed.
            0x251e2a0:lambda:self.ret(),0x251e2f0:lambda:self.ret(),
            0x251e650:lambda:self.ret(),0x3d37550:lambda:self.ret(value=self.reg(UC_X86_REG_ECX)),
            0x21125e0:lambda:self.ret(value=self.SCENE),0x2110f80:lambda:self.ret(),
            0x1e9c220:self.init_vector,0x1fe6400:self.push_entry,
            0x1f142a0:lambda:self.ret(4,self.SCENE),0x1f0f300:lambda:self.ret(),
        })
    def lookup_slot(self):self.ret(4,self.coin if self.arg()==121 else self.reward if self.has_reward else 0)
    def item_ctor(self):self.pending_count=self.arg();self.ret(92,self.reg(UC_X86_REG_ECX))
    def item_factory(self):self.reward_count=self.pending_count;self.ret(value=self.reward)
    def insert_reward(self):self.has_reward=True;self.ret(16,1)
    def set_reward(self):self.reward_count=self.arg();self.ret(8)
    def display_new(self):self.display.append(('new',self.arg(),self.arg(1)));self.ret(12)
    def display_delta(self):self.display.append(('delta',self.arg(),self.arg(1)));self.ret(16)
    def advance(self):
        at=self.reg(UC_X86_REG_ECX);self.put(at+8,self.read(at+8)+8);self.ret(8)
    def push_point(self):
        at=self.reg(UC_X86_REG_ECX)
        if not self.read(at+4):
            p=self.alloc(4096);self.put(at+4,p,p,p+4096)
        super().push_point()
    def push_entry(self):
        at=self.reg(UC_X86_REG_ECX)
        if not self.read(at+4):
            p=self.alloc(4096);self.vec(at,p,p)
        end=self.read(at+8);row=bytes(self.u.mem_read(self.arg(),120))
        self.entries.append(row.hex());self.u.mem_write(end,row);self.put(at+8,end+120);self.ret(4)

class VendingNativeTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        path=os.environ.get('VENDING_NATIVE_EXE')
        if not path: raise unittest.SkipTest('set VENDING_NATIVE_EXE')
        cls.data=Path(path).read_bytes()
        if hashlib.sha256(cls.data).hexdigest() != '72254abd5214bc24f8bcb76a16184e07105dde33f4e762b4c71c2e264bc7789c':
            raise ValueError('requires exact V179 source')

    def body(self,total=None,remaining=2):
        body=struct.pack('<B H I I I I I H',1,121,remaining,15,2,0,0,int(total is not None))
        if total is not None:
            row=bytearray(119);struct.pack_into('<HII',row,0,122,15,total)
            body+=b'\0'+row
        return body

    def test_native_machine_requests(self):
        for machine in (1,2,3,6):
            lab=VendingLab(self.data)
            self.assertEqual(lab.send_native(machine),dict(opcode=215,body=struct.pack('<IIH',machine,1,121).hex()))

    def test_old_empty_reply_has_no_settlement_row(self):
        lab=SettlementLab(self.data)
        got=lab.receive_native(self.body())
        self.assertEqual((got['remaining'],lab.display,lab.entries),(0,[],[]))
        self.assertIn(('coin',2,1),lab.effect)

    def test_committed_total_supports_new_merge_and_prior_refresh(self):
        for existing,total,kind in ((None,2,'new'),(5,7,'delta'),(7,7,'new')):
            with self.subTest(existing=existing):
                lab=SettlementLab(self.data);lab.has_reward=existing is not None;lab.reward_count=existing or 0
                got=lab.receive_native(self.body(total))
                self.assertEqual(got['remaining'],0)
                self.assertEqual(lab.reward_count,total)
                self.assertEqual(lab.display[0][0],kind)
                self.assertEqual(len(lab.display),1)
                row=bytes.fromhex(lab.entries[0])
                self.assertEqual((row[0],struct.unpack_from('<HII',row,2)),(0,(122,15,total)))

    def test_two_settlements_and_final_coin(self):
        lab=SettlementLab(self.data);lab.has_reward=False
        lab.receive_native(self.body(2,1))
        lab.coin_count=1
        lab.hooks[0x25ece10]=lambda:lab.ret(4,121)
        lab.receive_native(self.body(4,0))
        self.assertEqual(lab.reward_count,4)
        self.assertEqual(len(lab.display),2)
        self.assertIn(('remove_coin',121),lab.effect)

    def test_exported_production_replies(self):
        directory=os.environ.get('DNF_VENDING_NATIVE_EXPORT')
        if not directory:self.skipTest('set DNF_VENDING_NATIVE_EXPORT')
        paths=list(Path(directory).glob('*.bin'))
        self.assertGreater(len(paths),100)
        for path in paths:
            with self.subTest(reply=path.name):
                body=path.read_bytes();lab=SettlementLab(self.data);lab.has_reward=False
                meta=json.loads(path.with_suffix('.json').read_text())
                lab.reward_kind=2 if meta['kind']=='equipment' else 1
                got=lab.receive_native(body)
                self.assertEqual(got['remaining'],0)
                self.assertEqual(len(lab.display),1)
                entry=bytes.fromhex(lab.entries[0])
                self.assertEqual(struct.unpack_from('<I',entry,4)[0],meta['itemID'])
                self.assertEqual(struct.unpack_from('<I',entry,8)[0],struct.unpack_from('<I',body,32)[0])

if __name__=='__main__':unittest.main(verbosity=2)
