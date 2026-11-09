"""Execute the shipped title notification reader on five empty pages offline.

Packet readers and the final category/info-type/owner decisions execute native
code. Cache clearing, scene lookup, strings and the UI manager are adapters.
This does not test populated item construction or a running client UI.
"""
import argparse
import gc
import hashlib
import json
from pathlib import Path
import struct
import sys

INPUT_SHA256 = '244b5c1e3126967d8a2b718658c586c1ac675a08b255d7377e95b93b4b815112'


def verify(data):
    if hashlib.sha256(data).hexdigest() != INPUT_SHA256:
        raise ValueError('exact shipped cumulative image required')
    sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
    from test_guild_war_refresh_v173 import WarNative
    from unicorn.x86_const import UC_X86_REG_ECX

    class Reader(WarNative):
        WINDOW, ACTOR = 0x20010000, 0x20011000

        def __init__(self):
            super().__init__(data)
            self.cleared, self.opened, self.labels = [], [], []
            self.present = True
            for category in range(5):
                self.put(0x56A350C + 4*category, self.DATA + 0x100*category)
                self.put(0x56A36B0 + 4*category, self.DATA + 0x1000 + 0x100*category)
            self.hooks.update({
                0x32B5C90: lambda: self.ret(8, 0),
                0x25F3F10: self.clear,
                0x32BEA50: self.open,
                0x296D730: lambda: self.ret(value=self.ACTOR if self.present else 0),
                0x8EF130: lambda: self.ret(),
                0x33617E0: self.label,
            })

        def clear(self):
            self.cleared.append(self.reg(UC_X86_REG_ECX))
            self.ret()

        def open(self):
            assert self.arg() == 0xD4
            self.opened.append(0xD4)
            self.ret(12, self.WINDOW)

        def label(self):
            assert self.reg(UC_X86_REG_ECX) == self.WINDOW
            self.labels.append(True)
            self.ret(36)

        def page(self, category, owner=17, info=1):
            body=struct.pack('<BHII', info, owner, category, 0)
            self.u.mem_write(self.PACKET, body)
            self.put(0x57F9AD0, self.PACKET, len(body), 0, 0)
            self.run(0x2029B10)
            assert self.read(0x57F9AD0) == self.PACKET + len(body)
            assert self.read(0x57F9AD4) == 0

    lab=Reader()
    for category in range(5):
        lab.page(category)
        assert len(lab.opened) == int(category == 4)
    assert lab.cleared == [lab.DATA + 0x100*i for i in range(5)]
    assert len(lab.labels) == 1
    for owner, info in [(0, 1), (17, 0)]:
        lab.opened.clear();lab.labels.clear()
        lab.page(4, owner, info)
        assert not lab.opened and not lab.labels
    lab.opened.clear();lab.labels.clear();lab.present=False
    lab.page(4)
    # The original reader asks the UI manager to open before checking the actor;
    # departed actors therefore skip the title label without dereferencing NULL.
    assert lab.opened == [0xD4] and not lab.labels
    del lab
    gc.collect()
    return dict(passed=True, inputSha256=INPUT_SHA256,
                nativeEmptyPageReads=8, fivePeerCategoriesCleared=True,
                peerFinalPageOpensD4=True, selfAndNonOpenUpdatesDoNotOpen=True,
                departedTargetSkipsLabelSafely=True,
                cacheLookupStringAndUIAPIsStubbed=True,
                populatedTitleConstructionTested=False, gameplayAcceptance=False)


if __name__ == '__main__':
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--exe', type=Path, required=True)
    p.add_argument('--python-deps', type=Path, required=True)
    p.add_argument('--output', type=Path, required=True)
    a=p.parse_args();sys.path.insert(0,str(a.python_deps))
    result=verify(a.exe.read_bytes())
    with a.output.open('x',encoding='utf8') as f:json.dump(result,f,indent=2)
    print(json.dumps(result))
