"""Native guild row/menu/producer checks with explicit heap, string and UI adapters."""
import os
import json
from pathlib import Path
import struct
import sys
import unittest

import patch_guild_member_profile as patch
from test_guild_social_channel_v179 import SocialSender
from unicorn.x86_const import *

sys.path.insert(0, str(Path(__file__).parent / 'cross-scene-profile'))
import aux_cache
import portable_core


class GuildProfileNative(SocialSender):
    ROW = 0x20011000

    def __init__(self, data):
        super().__init__(data)
        self.data = data
        self.created, self.names, self.levels, self.built = [], [], [], []
        self.channel, self.directory = 19, 1
        self.fail_alloc = False
        self.cache = {}
        self.hooks.pop(0x8BAB80, None)
        self.hooks.update({
            0x1A62AA0: self.create_proxy,
            0x37E3000: self.proxy_name,
            0x29C0360: lambda: self.ret(4,0),
            0x37DF710: self.menu_flag,
            0x35450E0: self.menu_build,
            0x296D730: lambda: self.ret(value=0),
            0x1B4A020: lambda: self.ret(value=0x5678E38),
            0x1B37D40: lambda: self.ret(value=0),
            0x25253D0: lambda: self.ret(value=0),
            0x25254F0: lambda: self.ret(value=1),
            0xE66830: lambda: self.ret(value=self.MENU),
        })
        for address in (0x37DF130,0x37DF1B0,0x37DF230,0x37DF2B0,0x37DF010): self.hooks[address] = self.set_level
        self.install_cache()

    def directory_group(self):
        self.put(self.arg(1), self.channel if self.reg(UC_X86_REG_ECX)==0x56A1E80 else self.directory)
        self.ret(8)

    def install_cache(self):
        windows = portable_core.read_window_pairs(self.data, tuple(aux_cache.RANGES.items()))
        aux_cache.install(self.u, self.u.mem_write, self.put, {4: self.cache}, windows)

    def create_proxy(self):
        values = [self.arg(i) for i in range(4)]
        self.created.append(values)
        if self.fail_alloc: return self.ret(value=0)
        uid, channel, category, directory = values
        assert category == 4
        key = aux_cache.identity(uid, channel, directory)
        # Allocation/actor initialization is a fixture; native UID/channel/
        # directory setter contracts were independently checked in this input.
        self.u.mem_write(self.ACTOR, bytes(2296))
        self.put(self.ACTOR+0x20, uid)
        self.u.mem_write(self.ACTOR+0x604, bytes((channel,directory)))
        self.put(self.ACTOR+0x30+1076, 7)
        self.cache[key] = self.ACTOR
        self.install_cache()
        self.ret(value=self.ACTOR)

    def proxy_name(self):
        assert self.reg(UC_X86_REG_ECX) == self.ACTOR+0x30
        self.names.append(self.wide(self.arg()).decode('utf-16le'))
        self.ret(4, self.ACTOR+0x30)

    def set_level(self):
        assert self.reg(UC_X86_REG_ECX) == self.ACTOR+0x44
        assert self.arg(1) == self.ACTOR+0x48
        self.levels.append(self.read(self.arg()))
        self.ret(8)

    def menu_flag(self):
        self.u.mem_write(self.arg(1), b'\0'); self.ret(8)

    def menu_build(self):
        assert self.reg(UC_X86_REG_ECX) == self.MENU
        self.built.append((self.read(self.MENU+0x24)&65535, *bytes(self.u.mem_read(self.MENU+0x48,2))))
        self.ret(8)

    def prepare_row(self, uid=17, channel=11, directory=1, name='成员角色', actor=0, account_row=False):
        self.u.mem_write(self.ROW, bytes(0x200))
        encoded=name.encode('utf-16le')+b'\0\0'
        if len(name)<8: self.u.mem_write(self.ROW+4,encoded);self.put(self.ROW+0x18,7)
        else: self.u.mem_write(self.NAME,encoded);self.put(self.ROW+4,self.NAME);self.put(self.ROW+0x18,len(name))
        self.put(self.ROW+0x14,len(name))
        self.put(self.ROW+0x20,90,1,3,2)
        self.u.mem_write(self.ROW+0x31,bytes((directory,)))
        self.put(self.ROW+0x34,channel,uid,actor)
        self.u.mem_write(self.ROW+0x140,bytes((int(account_row),)))
        self.u.mem_write(self.ROW+0x182,b'\x07')

    def row_decision(self):
        self.u.reg_write(UC_X86_REG_ESP,self.STACK)
        self.u.reg_write(UC_X86_REG_EDI,self.ROW)
        guard=bytes(range(64));self.u.mem_write(self.STACK,guard)
        saved={r:0x12345670+i for i,r in enumerate((UC_X86_REG_EAX,UC_X86_REG_EBX,UC_X86_REG_ECX,UC_X86_REG_EDX,UC_X86_REG_ESI,UC_X86_REG_EBP))}
        for r,v in saved.items():self.u.reg_write(r,v)
        result=[]
        def stop(value):result.append(value);self.u.emu_stop()
        self.hooks[0x11B8992]=lambda:stop('name')
        self.hooks[0x11B8954]=lambda:stop('actor')
        self.u.emu_start(patch.SITE,self.STOP,count=10000)
        assert len(result)==1 and self.reg(UC_X86_REG_ESP)==self.STACK
        assert all(self.reg(r)==v for r,v in saved.items()) and self.reg(UC_X86_REG_EDI)==self.ROW
        assert bytes(self.u.mem_read(self.STACK,64))==guard
        return result[0]

    def build_menu(self):
        self.put(self.MENU+0xC4,7)
        self.run(0x3549600,(self.read(self.ROW+0x3C),0,0,0),ecx=self.MENU)
        return self.built

    def finish(self):
        super().finish(); self.u.emu_stop()

    def request(self, kind):
        frame=self.STACK+0x2000
        uid=self.read(self.MENU+0x24)&65535
        channel,directory=bytes(self.u.mem_read(self.MENU+0x48,2))
        self.put(frame-0x238,uid)
        self.put(frame-0x248,channel)
        self.put(frame-0x24C,directory)
        self.u.reg_write(UC_X86_REG_ESP,self.STACK);self.u.reg_write(UC_X86_REG_EBP,frame)
        self.u.reg_write(UC_X86_REG_EDI,self.MENU);self.u.reg_write(UC_X86_REG_EBX,self.directory)
        # Actual common-menu lookup, including the cumulative auxiliary fallback.
        self.u.emu_start(0x353ADDA,0x353AE20,count=2000)
        assert self.reg(UC_X86_REG_ESI)==self.ACTOR
        self.u.emu_start(0x353B80B if kind==0 else 0x353CD87,self.STOP,count=10000)
        assert len(self.sent)==1
        return self.sent[0]


class GuildMemberProfileTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        name=os.environ.get('DNF_GUILD_PROFILE_INPUT')
        if not name: raise unittest.SkipTest('requires exact cumulative candidate')
        cls.before=Path(name).read_bytes();cls.after,cls.receipt=patch.build(cls.before)

    def test_01_missing_online_actor_red_green_and_native_requests(self):
        for channel in (11,19):
            for kind in (0,4):
                old=GuildProfileNative(self.before);old.prepare_row(channel=channel)
                self.assertEqual(old.row_decision(),'name')
                new=GuildProfileNative(self.after);new.prepare_row(channel=channel)
                self.assertEqual(new.row_decision(),'actor')
                self.assertEqual(new.created,[[17,channel,4,1]])
                self.assertEqual(new.names,['成员角色']);self.assertEqual(new.levels,[90])
                self.assertEqual(new.build_menu(),[(17,channel,1)])
                expected=(819,struct.pack('<BHBB',channel,17,kind,1)) if channel!=19 else ((8,b'\x11\0\3') if kind==0 else (1401,b'\x11\0'))
                self.assertEqual(new.request(kind),expected)

    def test_02_offline_invalid_identity_and_allocation_failure(self):
        for kwargs in ({'channel':-1},{'channel':0},{'channel':255},{'uid':0},{'uid':65535},{'uid':65536},{'directory':250},{'name':''},{'account_row':True}):
            new=GuildProfileNative(self.after);new.prepare_row(**kwargs)
            self.assertEqual(new.row_decision(),'name');self.assertEqual(new.created,[])
        new=GuildProfileNative(self.after);new.fail_alloc=True;new.prepare_row()
        self.assertEqual(new.row_decision(),'name');self.assertEqual(new.read(new.ROW+0x3C),0)

    def test_03_existing_actor_preserved_and_long_name(self):
        new=GuildProfileNative(self.after);new.prepare_row(actor=new.ACTOR)
        self.assertEqual(new.row_decision(),'actor');self.assertEqual(new.created,[])
        new=GuildProfileNative(self.after);new.prepare_row(name='跨频道成员很长的名字')
        self.assertEqual(new.row_decision(),'actor');self.assertEqual(new.names,['跨频道成员很长的名字'])

    def test_04_scoped_changes_and_bad_input(self):
        cursor=0
        for lo,hi in self.receipt['changedRanges']:
            self.assertEqual(self.before[cursor:lo],self.after[cursor:lo]);cursor=hi
        self.assertEqual(self.before[cursor:],self.after[cursor:len(self.before)])
        with self.assertRaises(ValueError):patch.build(self.after)

    def test_05_production_roster_reader_to_menu(self):
        path=os.environ.get('DNF_GUILD_PROFILE_PACKETS')
        if not path: self.skipTest('requires exported server packets')
        body=bytes.fromhex(json.loads(Path(path).read_text())['roster'])
        for image,expected in ((self.before,'name'),(self.after,'actor')):
            lab=GuildProfileRosterNative(image)
            lab.roster(body)
            lab.ROW=lab.rows[17]
            self.assertEqual(lab.read(lab.ROW+0x38),17)
            self.assertEqual(lab.read(lab.ROW+0x34),11)
            self.assertEqual(bytes(lab.u.mem_read(lab.ROW+0x31,1)),b'\1')
            self.assertEqual(lab.row_decision(),expected)
            if expected=='actor':
                self.assertEqual(lab.names,['peer'])
                self.assertEqual(lab.build_menu(),[(17,11,1)])
                self.assertEqual(lab.request(0),(819,b'\x0b\x11\0\0\1'))

    def test_06_production_adventure_reply_selects_foreign_window(self):
        path=os.environ.get('DNF_GUILD_PROFILE_PACKETS')
        if not path:self.skipTest('requires exported server packets')
        body=bytes.fromhex(json.loads(Path(path).read_text())['adventure'])
        lab=GuildProfileAdventureNative(self.after)
        lab.reply(body)
        self.assertEqual(lab.target_uid,17)
        self.assertEqual(lab.raw,body[19:7443])
        self.assertEqual(lab.read(0x57F9AD4),0)

    def test_07_production_profile_routes_to_target_namespace(self):
        path=os.environ.get('DNF_GUILD_PROFILE_PACKETS')
        if not path:self.skipTest('requires exported server packets')
        packets={k:bytes.fromhex(v) for k,v in json.loads(Path(path).read_text()).items()}
        lab=GuildProfileNative(self.after)
        for mode,body,key_offset,entry,caller,stop in (
                (0,packets['prime'],76,0x22C5054,0x22C8AAD,0x22C50C3),
                (3,packets['detail'],13,0x22C42E6,0x22C8ACD,0x22BE730),
                (3,packets['detail'],13,0x22C42E6,0x12345678,0x22BE730)):
            directory,channel=body[3:5];uid=struct.unpack_from('<H',body,key_offset)[0]
            self.assertEqual((uid,channel,directory),(17,11,1))
            frame=lab.STACK+0x2000
            lab.put(frame+4,caller);lab.put(frame-0x4B8,uid);lab.put(frame-0x4C8,uid)
            lab.u.mem_write(0x568A519,bytes((channel,directory)))
            lab.u.reg_write(UC_X86_REG_EBP,frame);lab.u.reg_write(UC_X86_REG_ESP,lab.STACK)
            lab.u.reg_write(UC_X86_REG_EDI,uid)
            hits=[]
            def reached():
                hits.append(lab.reg(UC_X86_REG_ESI if mode==0 else UC_X86_REG_EDI));lab.u.emu_stop()
            lab.hooks[stop]=reached
            lab.u.emu_start(entry,lab.STOP,count=20000)
            del lab.hooks[stop]
            self.assertEqual(hits,[lab.ACTOR])
        self.assertEqual(lab.created,[[17,11,4,1]])


class GuildProfileAdventureNative(GuildProfileNative):
    def reply(self, body):
        self.hooks.update({
            0x3E77400:self.stack_probe,
            0xE5F260:lambda:self.ret(value=self.reg(UC_X86_REG_ECX)),
            # Fixed-size protected serialization is an adapter; scalar readers,
            # status gate, target UID and foreign/self choice execute natively.
            0x3752020:self.read_raw,
            0xE65BBC:self.foreign_window,
        })
        self.put(0x2000C000,0xE650D0,0)
        wire=bytearray(16)+body;wire[0]=1
        struct.pack_into('<H',wire,1,1401);struct.pack_into('<I',wire,3,len(wire))
        self.u.mem_write(self.PACKET,bytes(wire))
        self.put(self.STACK,self.STOP,0,self.PACKET,0)
        self.u.reg_write(UC_X86_REG_ESP,self.STACK);self.u.reg_write(UC_X86_REG_ECX,self.NETWORK)
        self.u.emu_start(0x25239A0,self.STOP,count=20000)
        assert hasattr(self,'target_uid'),'foreign info path not reached'

    def stack_probe(self):
        sp=self.reg(UC_X86_REG_ESP);dest=self.read(sp);amount=self.reg(UC_X86_REG_EAX)
        self.u.reg_write(UC_X86_REG_ESP,sp+4-amount);self.u.reg_write(UC_X86_REG_EIP,dest)

    def read_raw(self):
        start,remaining=self.read(0x57F9AD0),self.read(0x57F9AD4)
        assert remaining>=7428 and self.read(start)==7424
        self.raw=bytes(self.u.mem_read(start+4,7424));self.u.mem_write(self.arg(),self.raw)
        self.put(0x57F9AD0,start+7428,remaining-7428);self.ret()

    def foreign_window(self):
        self.target_uid=self.read(self.reg(UC_X86_REG_EBP)-0x1F24)&65535
        self.u.emu_stop()


class GuildProfileRosterNative(GuildProfileNative):
    def roster(self, body):
        self.rows={}
        self.hooks.update({
            0x296DE50:lambda:self.ret(value=1),
            0x297E0F0:lambda:self.ret(),
            0x297C400:self.record_init,
            0x297EC40:self.record_copy,
            0x297A650:lambda:self.ret(),
            0x45FDA0:self.wide_assign,
            0x3DAC380:self.grow_decode,
            0xC38670:lambda:self.ret(12),
            0x2982940:lambda:self.ret(4),
            0x29788D0:lambda:self.ret(),
            0x37E3000:lambda:self.ret(4),
        })
        for address in (0x37DF130,0x37DF1B0,0x37DF230,0x37DF2B0,0x37DF010):self.hooks[address]=lambda:self.ret(8)
        self.u.mem_write(self.PACKET,body);self.put(0x57F9AD0,self.PACKET,len(body),0,0)
        self.run(0x1FD2D10,(67,1),pop=0)
        assert self.read(0x57F9AD4)==0
        self.hooks[0x37E3000]=self.proxy_name
        for address in (0x37DF130,0x37DF1B0,0x37DF230,0x37DF2B0,0x37DF010):self.hooks[address]=self.set_level

    def record_init(self):
        at=self.reg(UC_X86_REG_ECX);self.u.mem_write(at,bytes(0x188));self.ret(value=at)

    def record_copy(self):
        at=self.arg();uid=self.read(at+0x38);dest=0x20011000+len(self.rows)*0x400
        self.rows[uid]=dest;self.u.mem_write(dest,bytes(self.u.mem_read(at,0x188)));self.ret(4,dest)

    def wide_assign(self):
        at=self.reg(UC_X86_REG_ECX);self.string(at,self.wide(self.arg()));self.ret(8,at)

    def grow_decode(self):
        self.u.mem_write(self.reg(UC_X86_REG_ECX),bytes((self.arg()&255,)));self.ret(4)


if __name__=='__main__':unittest.main()
