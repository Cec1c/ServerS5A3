"""Bounded V179 follow directory decisions and real connection endpoint selection."""
import json
import os
from pathlib import Path
import struct
import unittest

import patch_guild_social_channel_v179 as patch
from test_guild_social_channel_v179 import SocialSender
from unicorn.x86_const import *

class FollowNative(SocialSender):
    VECTOR, ROW, LOOKUP, FAILURE = 0x20018000, 0x20019000, 0x20019400, 0x20019500
    def __init__(self, data):
        super().__init__(data)
        self.put(self.ROW, 0x48995ec)
        self.put(self.ROW + 4, 1, 0)
        self.put(self.ROW + 0x70, 11)
        self.put(self.ROW + 0xA8, 0x0100007F, 7002)
        self.put(self.VECTOR, self.ROW)
        self.put(self.LOOKUP + 0x28, 11)
        self.hooks.update({0x142BD90: lambda: self.ret(value=self.mode),
                           0x3C1BEA0: lambda: self.ret(value=0),
                           0x32B55E0: lambda: self.ret(4, int(self.dialog)),
                           0x252E970: lambda: self.ret(4, int(self.admission)),
                           0x37DD6C0: lambda: self.ret(),
                           0x3816100: lambda: self.ret(4),
                           0x38168F0: lambda: self.ret(8),
                           0x1B4A020: lambda: self.ret(value=0x20019600),
                           0x1B39D80: lambda: self.ret(8, 0)})
        self.mode, self.dialog, self.admission = 0, False, False

    def string(self, at, value):
        self.u.mem_write(at, bytes(32))
        if len(value) <= 14:
            self.u.mem_write(at+4, value+b'\0\0')
            self.put(at+20, len(value)//2, 7)
        else:
            self.u.mem_write(0x2001A000, value+b'\0\0')
            self.put(at+4, 0x2001A000)
            self.put(at+20, len(value)//2, len(value)//2)

    def connect(self):
        calls=[]
        def formatted():
            value='.'.join(str(self.arg(i)) for i in (2,3,4,5))
            self.u.mem_write(0x2001B000, value.encode('utf-16le')+b'\0\0')
            self.ret(value=0x2001B000)
        def socket_connect():
            calls.append(dict(ip=self.wide(self.arg()).decode('utf-16le'),port=self.arg(1)))
            self.u.reg_write(UC_X86_REG_EIP, self.STOP)
        self.hooks.update({0x3816180:lambda:self.ret(value=0),
                           0x1B37730:lambda:self.ret(value=0),
                           0x1B39D80:lambda:self.ret(8,self.ROW),
                           0x1B373E0:lambda:self.ret(value=0), # Available directory row fixture.
                           0x1C4FAE0:lambda:self.ret(value=self.ACTOR),
                           0x1FA0850:lambda:self.ret(value=1),
                           0x3E79A15:lambda:self.ret(value=self.NAME),
                           0x376F110:lambda:self.ret(4),
                           0x37E0840:formatted,
                           0x374FA00:socket_connect})
        self.put(self.NETWORK+8,0)
        self.put(self.ROW+0xC,0)
        self.put(0x6E83968,1) # Login has already consumed the cold-landing guard.
        self.put(self.STACK,self.STOP)
        self.u.reg_write(UC_X86_REG_ESP,self.STACK)
        self.u.reg_write(UC_X86_REG_ECX,0x568171C)
        self.u.emu_start(0x1F4F620,self.STOP,count=10000)
        assert calls,hex(self.reg(UC_X86_REG_EIP))
        return calls

    def lookup(self, channel=11, kind=0, mode=0, admission=False, dialog=False):
        self.mode, self.dialog, self.admission = mode, dialog, admission
        self.put(self.ROW + 8, kind)
        self.put(self.LOOKUP + 0x28, channel)
        self.put(self.FAILURE, 0)
        selected = self.run(0x3537A40, (self.VECTOR, self.VECTOR+4, self.LOOKUP, self.FAILURE), pop=0)
        return dict(selected=selected == self.VECTOR, failure=self.read(self.FAILURE))

    def confirm_endpoint(self):
        # Confirmed button branch: real endpoint getters, connection manager,
        # endpoint setter and timer-arming code; UI and directory lookup adapted.
        ip = self.run(0x2677900, ecx=self.ROW)
        port = self.run(0x1B37620, ecx=self.ROW) & 0xFFFF
        self.put(self.MENU+0x2C, ip, port)
        self.put(0x5681738, 1)
        self.u.reg_write(UC_X86_REG_ESI, self.MENU)
        self.u.reg_write(UC_X86_REG_ESP, self.STACK)
        self.u.emu_start(0x353A240, 0x353A257, count=10000)
        assert self.reg(UC_X86_REG_EIP) == 0x353A257
        assert self.reg(UC_X86_REG_ESP) == self.STACK
        return dict(ip=self.read(0x5681260), port=self.read(0x5681264)&0xffff,
                    activated=self.read(0x568171C)&255, mode=self.read(0x5681720))


class GuildFollowTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        path = os.environ.get('DNF_GUILD_SOCIAL_V179_CLIENT')
        if not path:
            raise unittest.SkipTest('requires exact V179 image')
        cls.data, _ = patch.build(Path(path).read_bytes())

    def test_directory_restrictions_and_missing_channel(self):
        lab = FollowNative(self.data)
        for kind in (0, 3, 22, 40):
            self.assertEqual(lab.lookup(kind=kind), dict(selected=True, failure=0))
            self.assertEqual(lab.lookup(kind=kind, mode=1), dict(selected=False, failure=2))
        self.assertEqual(lab.lookup(kind=33), dict(selected=False, failure=3))
        self.assertEqual(lab.lookup(kind=33, mode=1), dict(selected=True, failure=0))
        self.assertEqual(lab.lookup(kind=11), dict(selected=False, failure=1))
        self.assertEqual(lab.lookup(kind=11, admission=True), dict(selected=True, failure=0))
        self.assertEqual(lab.lookup(channel=12), dict(selected=False, failure=0))
        self.assertFalse(lab.lookup(dialog=True)['selected'])

    def test_go_directory_to_native_connect(self):
        folder = os.environ.get('DNF_GUILD_SOCIAL_NATIVE_DIR')
        if not folder:
            self.skipTest('set Go packet export directory')
        plan = json.loads(Path(folder, 'follow-directory.json').read_text())
        results = []
        for case in plan:
            lab = FollowNative(self.data)
            body = bytes.fromhex(case['ack'])
            self.assertEqual(lab.lookup_reply(body), 0xffffffff)
            self.assertEqual(lab.read(lab.MENU+0x9c), case['directory'])
            self.assertEqual(lab.read(lab.MENU+0xa0), case['channel'])
            lab.put(lab.ROW+0x70, case['channel'])
            lab.put(lab.ROW+0xa8, struct.unpack('<I', bytes(map(int, case['ip'].split('.'))))[0], case['port'])
            self.assertTrue(lab.lookup(channel=case['channel'], kind=case['kind'], admission=True)['selected'])
            lab.confirm_endpoint()
            connection = lab.connect()
            self.assertEqual(connection, [dict(ip=case['ip'], port=case['port'])])
            results.append(dict(name=case['name'], channel=case['channel'], **connection[0]))
        Path(folder, 'follow-native-connections.json').write_text(json.dumps(results, indent=2))


if __name__ == '__main__':
    unittest.main()
