"""V179 actor mood reader and setter executed with production Go u16 payloads.

Resource/effect creation is adapted; packet consumption, actor storage and
ordinary/special selection execute native x86. No game process or network.
"""
import hashlib
import os
from pathlib import Path
import struct
import unittest
from test_guild_war_refresh_v173 import WarNative

SHA = '72254abd5214bc24f8bcb76a16184e07105dde33f4e762b4c71c2e264bc7789c'

class MoodLab(WarNative):
    def __init__(self, data):
        super().__init__(data)
        self.hooks.update({
            0x29659b0: lambda: self.ret(4, 0),
            0x352f8b0: lambda: self.effect('ordinary', 0),
            0x352f5a0: lambda: self.effect('special', 0),
        })

    def receive(self, payload):
        self.u.mem_write(self.PACKET, payload)
        self.put(0x57f9ad0, self.PACKET, len(payload), 0, 0)
        self.run(0x22be190, (self.SCENE,), pop=0)
        return struct.unpack('<H', self.u.mem_read(self.SCENE+0x8ec, 2))[0]

class MoodNativeTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        path = os.environ.get('MOOD_NATIVE_EXE')
        if not path: raise unittest.SkipTest('MOOD_NATIVE_EXE required')
        cls.data = Path(path).read_bytes()
        assert hashlib.sha256(cls.data).hexdigest() == SHA

    def test_change_and_clear_same_actor(self):
        lab = MoodLab(self.data)
        for value in (7, 1001, 0, 7):
            self.assertEqual(lab.receive(struct.pack('<H',value)), value)
            self.assertEqual(lab.read(0x57f9ad4), 0)
            self.assertEqual(lab.effects[-1][0], 'special' if value >= 1000 else 'ordinary')

    def test_go_production_projection(self):
        folder = os.environ.get('DNF_MOOD_NATIVE_EXPORT')
        if not folder: self.skipTest('DNF_MOOD_NATIVE_EXPORT required')
        paths = sorted(Path(folder).glob('mood-*-peer-*.bin'))
        self.assertEqual(len(paths), 6)
        lab = MoodLab(self.data)
        for path in paths:
            expected = int(path.name.split('-')[1])
            self.assertEqual(lab.receive(path.read_bytes()), expected)
            self.assertEqual(lab.effects[-1][0], 'special' if expected >= 1000 else 'ordinary')

if __name__ == '__main__': unittest.main()
