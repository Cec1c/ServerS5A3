"""Add a bounded native party-readiness query to the exact cumulative client.

PCQ1/PCR1 on class1 opcode 7f13. Native connection state is read, never changed.
No packet is a readiness proof unless its complete fixed roster, leader and
local slot agree. Old custom handlers and the original receive gate are chained.
"""
from pathlib import Path
import argparse
import hashlib
import importlib.util
import json
import struct

spec = importlib.util.spec_from_file_location('peutils', Path(__file__).resolve().parents[1] / 'quiet-profile-refresh/build_candidate.py')
p = importlib.util.module_from_spec(spec)
spec.loader.exec_module(p)
INPUT_SHA = '7861cd3c546e73651ddfd28047686a29e580f5bebd38b1a7fd681d3b36fc24e0'
INPUT_SIZE = 112607232
OPCODE = 0x7f13
DISPATCH_CALL = 0x6f202d3
GATE_CALL = 0x2523a13
OLD_DISPATCH = 0x6f20050
OLD_GATE = 0x6f9dc00

class Asm:
    def __init__(self, base):
        self.base, self.b, self.labels, self.fixups = base, bytearray(), {}, []
    def raw(self, s): self.b.extend(bytes.fromhex(s))
    def at(self, s):
        p.require(s not in self.labels, 'duplicate label')
        self.labels[s] = len(self.b)
    def rel(self, op, target):
        self.raw(op)
        self.fixups.append((len(self.b), target))
        self.b.extend(bytes(4))
    def end(self):
        for at, target in self.fixups:
            target = self.base + self.labels[target] if isinstance(target, str) else target
            struct.pack_into('<i', self.b, at, target-self.base-at-4)
        return bytes(self.b)

def payload(base):
    x = Asm(base)
    # cdecl bool dispatch(frame). Saved state and all response bytes are on stack.
    x.raw('55 8bec 83ec40 53 56 57 8b7508 85f6')
    x.rel('0f84', 'fallback')
    x.raw('803e01')
    x.rel('0f85', 'fallback')
    x.raw('66817e01137f')
    x.rel('0f85', 'fallback')
    x.raw('807e0f00')
    x.rel('0f85', 'handled')
    x.raw('837e0330') # fixed16 + 32 request
    x.rel('0f85', 'handled')
    x.raw('833dd49a7f0520')
    x.rel('0f85', 'handled')
    x.raw('8b35d09a7f05') # decoded-body global 057f9ad0
    x.raw('6a20 56')
    x.rel('e8', 0x6f189d0)
    x.raw('83c408 84c0')
    x.rel('0f84', 'handled')
    x.raw('813e50435131') # PCQ1
    x.rel('0f85', 'handled')
    x.raw('807e1c07')
    x.rel('0f87', 'handled')
    x.raw('807e1d07')
    x.rel('0f87', 'handled')
    x.raw('66837e1e00')
    x.rel('0f85', 'handled')
    x.raw('8d7dc0 b908000000 fc f3a5') # copy32 into ebp-40
    x.raw('c745c050435231 c745e0fefefefe c745e4fefefefe')
    # Validate party manager and self record before touching the roster.
    x.raw('8b3590376a05 687ca12000 56') # UDP manager through state arrays
    x.rel('e8', 0x6f189d0)
    x.raw('83c408 84c0')
    x.rel('0f84', 'send')
    x.raw('8b3d68376a05 6a6c 57')
    x.rel('e8', 0x6f189d0)
    x.raw('83c408 84c0')
    x.rel('0f84', 'send')
    x.raw('a158376a05 6a74 50')
    x.rel('e8', 0x6f189d0)
    x.raw('83c408 84c0')
    x.rel('0f84', 'send')
    x.raw('8bcf')
    x.rel('e8', 0x2974420) # true local actor slot
    x.raw('0fb64ddc 3bc1')
    x.rel('0f85', 'send')
    x.rel('e8', 0x296e590)
    x.raw('8bd8 6a50 53')
    x.rel('e8', 0x6f189d0)
    x.raw('83c408 84c0')
    x.rel('0f84', 'send')
    x.raw('0fb645dd 39434c')
    x.rel('0f85', 'send')
    x.raw('394768') # manager leader slot agrees with local record
    x.rel('0f85', 'send')
    x.raw('31ff')
    x.at('roster')
    x.raw('0fb744bb2c 663dffff')
    x.rel('0f85', 'uid')
    x.raw('31c0') # native empty UID -1 normalized to zero
    x.at('uid')
    x.raw('663b447dcc')
    x.rel('0f85', 'send')
    x.raw('47 83ff08')
    x.rel('0f82', 'roster')
    # All eight roster cells match. Every non-self occupied UDP slot must exist
    # in the native socket table AND have state zero. Missing slots fail closed.
    x.raw('31ff')
    x.at('states')
    x.raw('31c0 66837c7dcc00')
    x.rel('0f84', 'store')
    x.raw('0fb64ddc 39cf')
    x.rel('0f84', 'store')
    x.raw('b8ff000000 833cbde8276d06ff')
    x.rel('0f84', 'store')
    x.raw('8b84be7ca02000 83f803')
    x.rel('0f86', 'store')
    x.raw('b8ff000000')
    x.at('store')
    x.raw('88443de0 47 83ff08')
    x.rel('0f82', 'states')
    x.at('send')
    # Existing custom sender's native transport ownership checks. Do not enter
    # the packet builder if another packet is already open or the socket closed.
    x.raw('8b35d4d87e05 6869d02b00 56')
    x.rel('e8', 0x6f189d0)
    x.raw('83c408 84c0')
    x.rel('0f84', 'handled')
    x.raw('807e0800')
    x.rel('0f84', 'handled')
    x.raw('80be68d02b0000')
    x.rel('0f85', 'handled')
    x.raw('83be34cc2b0000')
    x.rel('0f85', 'handled')
    x.raw('83be2ccc2b0000')
    x.rel('0f85', 'handled')
    x.raw('68137f0000 8bce')
    x.rel('e8', 0x374f280)
    x.raw('85c0')
    x.rel('0f88', 'handled')
    x.raw('6a28 8d45c0 50 8bce')
    x.rel('e8', 0x374eeb0)
    x.rel('e8', 0x3750ac0)
    x.at('handled')
    x.raw('b801000000 5f5e5b 8be5 5d c3')
    x.at('fallback')
    x.raw('5f5e5b 8be5 5d')
    x.rel('e9', OLD_DISPATCH)
    while len(x.b) % 16: x.raw('cc')
    gate = base + len(x.b)
    x.raw('803f01')
    x.rel('0f85', 'old_gate')
    x.raw('66817c2404137f')
    x.rel('0f85', 'old_gate')
    x.raw('b801000000 c20400')
    x.at('old_gate')
    x.rel('e9', OLD_GATE)
    return x.end(), gate

def build(data):
    p.require(len(data) == INPUT_SIZE and hashlib.sha256(data).hexdigest() == INPUT_SHA, 'exact cumulative EXE required')
    pe = p.PE(data); pe.require_contiguous()
    p.require(not p.u16(data, pe.opt+70) & 0x40, 'relocatable image unsupported')
    p.require(data[pe.opt+128:pe.opt+136] == bytes(8), 'certificate present')
    p.require(len(data) == max(s['raw']+s['raw_size'] for s in pe.sections), 'overlay present')
    section = next(s for s in pe.sections if s['name'] == '.krtext')
    p.require(section['characteristics'] == 0x60000020, 'RX section changed')
    delta = p.align(section['virtual_size'], 16)
    va = pe.base + section['rva'] + delta
    code, gate = payload(va)
    p.require(delta+len(code) <= section['raw_size'], 'helper exceeds RX padding')
    cave = pe.offset(va, len(code))
    p.require(data[cave:cave+len(code)] == bytes(len(code)), 'padding occupied')
    header = pe.st+40*section['index']+8
    out = bytearray(data); edits = []
    for site, old, target in [(DISPATCH_CALL, OLD_DISPATCH, va), (GATE_CALL, OLD_GATE, gate)]:
        at = pe.offset(site, 5)
        p.require(pe.read(site, 5) == b'\xe8'+struct.pack('<i', old-site-5), 'hook changed')
        out[at:at+5] = b'\xe8'+struct.pack('<i', target-site-5)
        edits.append((at, at+5))
    out[cave:cave+len(code)] = code
    struct.pack_into('<I', out, header, delta+len(code))
    struct.pack_into('<I', out, pe.opt+64, p.pe_checksum(out, pe.opt+64))
    edits += [(cave,cave+len(code)),(header,header+4),(pe.opt+64,pe.opt+68)]
    normalized=bytearray(out[:len(data)])
    for lo,hi in edits: normalized[lo:hi]=data[lo:hi]
    p.require(normalized == data, 'unreviewed byte mutation')
    p.PE(out).require_contiguous()
    return bytes(out), dict(inputSha256=INPUT_SHA, outputSha256=hashlib.sha256(out).hexdigest(), dispatch=va, gate=gate, codeBytes=len(code), changedRanges=sorted(edits), opcode=OPCODE, installed=False, gameplayAccepted=False)

if __name__ == '__main__':
    ap=argparse.ArgumentParser(description=__doc__); ap.add_argument('--input',type=Path,required=True); ap.add_argument('--output',type=Path,required=True)
    a=ap.parse_args(); data,receipt=build(a.input.read_bytes())
    with a.output.open('xb') as f:f.write(data)
    p.require(hashlib.sha256(a.output.read_bytes()).hexdigest()==receipt['outputSha256'],'readback mismatch')
    print(json.dumps(receipt,indent=2))
