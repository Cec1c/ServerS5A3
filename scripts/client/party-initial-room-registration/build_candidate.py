"""Exact-image local candidate for NOTI120's modern party registration path.

No live access or installation. Old/unsupported contexts retain the native
handler. The modern path adds an existing peer to the actual dungeon registry;
it does not invoke the native resurrection, HP/MP, or actor reset operations.
"""
from pathlib import Path
import argparse
import hashlib
import importlib.util
import json
import struct

HERE = Path(__file__).resolve().parent

def module(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    value = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(value)
    return value

pe = module('registration_pe', HERE.parent/'quiet-profile-refresh/build_candidate.py')
asm = module('registration_asm', HERE.parent/'dungeon-peer-retire/helpers.py')
INPUT_SHA = '244b5c1e3126967d8a2b718658c586c1ac675a08b255d7377e95b93b4b815112'
SITE, CODE, DATA = 0x1FF2F34, 0x6F88600, 0x6EB0100
CONTINUE, DONE = 0x1FF2F39, 0x1FF32B1
ORIGINAL = bytes.fromhex('e857f8faff')
COUNTERS = ('attempts', 'fallback', 'eligible', 'already_registered',
            'registered', 'registration_failed', 'last_uid', 'last_slot', 'local_ready_sweeps')

def helper():
    c = asm.Code(CODE)
    def count(index):
        c.raw('ff05' + struct.pack('<I', DATA + index*4).hex())
    c.raw('9c60')
    count(0)
    c.raw('0fb69d67ffffff 6a00 6a00'); c.call('process_slot')
    c.raw('83f802'); c.je('local_ready')
    c.raw('85c0'); c.jne('done')
    c.jump('fallback')
    c.label('local_ready')
    # The local actor is already a member of this registry. Retry a bounded
    # eight-slot sweep, pinning the observed registry and party across callbacks.
    count(8)
    c.raw('8bf1 8bfa 33db')
    c.label('sweep_slot')
    c.raw('57 56'); c.call('process_slot')
    c.raw('43 83fb08'); c.branch('0f82','sweep_slot')
    c.jump('fallback')  # local placement retains the original handler
    c.label('done'); c.raw('619d'); c.jump(DONE)
    c.label('fallback'); count(1); c.raw('619d')
    c.call(0x1FA2790); c.jump(CONTINUE)

    # EBX=slot, stack args=(expected registry, expected party), zero on initial
    # attempt. Return 0=original, 1=handled, 2=registered local readiness signal.
    c.label('process_slot'); c.raw('60')
    c.raw('83fb07'); c.ja('reject')
    # The old party object owns the untouched legacy path.
    c.raw('833d80907b0500'); c.jne('reject')
    c.call(0x1FA2790); c.raw('85c0'); c.je('reject')
    c.raw('8bf0 8bce'); c.call(0x2526B50)
    c.raw('83f803'); c.jne('reject')  # native current scene = dungeon
    c.raw('8bce'); c.call(0x2524FB0)  # same room selection as NOTI32
    c.raw('85c0'); c.je('reject')
    c.raw('8bb8c4000000 85ff'); c.je('reject')
    c.raw('837c242400'); c.je('registry_ok')
    c.raw('3b7c2424'); c.jne('reject')
    c.label('registry_ok')
    c.raw('8b0d68376a05 85c9'); c.je('reject')
    c.raw('53'); c.call(0x2974890)
    c.raw('85c0'); c.je('reject')
    c.raw('8bf0 837e4c01'); c.jne('reject')
    c.raw('8b96c8040000 85d2'); c.je('reject')
    c.raw('837a7001'); c.jne('reject')
    c.raw('837c242800'); c.je('party_ok')
    c.raw('3b542428'); c.jne('reject')
    c.label('party_ok')
    c.raw('8b0d58376a05 85c9'); c.je('reject')
    c.call(0x296DD80); c.raw('85c0'); c.je('reject')
    c.raw('8b96c8040000 3990c8040000'); c.jne('reject')
    c.raw('8b88f8040000 85c9'); c.je('reject')
    c.raw('3b0db06f7b05'); c.jne('reject')
    # Alias records may use the selected channel instead of namespace zero.
    c.raw('837a7400'); c.je('owner_ok')
    c.raw('8bce'); c.call(0x2964680)
    c.raw('0fb6c0 85c0'); c.je('reject')
    c.raw('8b96c8040000 394274'); c.jne('reject')
    c.label('owner_ok')
    c.raw('0fb74620 85c0'); c.je('reject')
    c.raw('3dffff0000'); c.je('reject')
    c.raw('3b449a2c'); c.jne('reject')
    c.raw('891d' + struct.pack('<I', DATA+28).hex())
    c.raw('8bd8 a3' + struct.pack('<I', DATA+24).hex())
    c.raw('8bb6f8040000 85f6'); c.je('reject')
    c.raw('3b35b06f7b05'); c.je('local_signal')
    count(2)
    c.raw('56 8bcf'); c.call(0x2879C90)  # native membership search
    c.raw('84c0'); c.jne('present')
    # A fresh, never-registered actor has kind=0 and no registry. A formerly
    # registered/dead/retired actor retains its kind; do not reintroduce it.
    c.raw('83bea800000000'); c.jne('reject')
    c.raw('83bec800000000'); c.jne('reject')
    # The registry key is not the member UID. Match NOTI120's native actor-key
    # calculation, including mode/slot-dependent keys and valid zero keys.
    c.raw('56'); c.call(0x296AA30); c.raw('83c404 8bd8')
    # register(parent=0, kind=0x111, native_actor_key, actor, 0,0,0,1)
    c.raw('6a01 6a00 6a00 6a00 56 53 6811010000 6a00 8bcf')
    c.call(0x288A260)
    c.raw('84c0'); c.je('failed')
    count(4); c.jump('handled')
    c.label('failed'); count(5); c.jump('handled')
    c.label('present'); count(3)
    c.label('handled'); c.raw('61 b801000000 c20800')
    c.label('reject'); c.raw('61 33c0 c20800')
    c.label('local_signal')
    c.raw('8bda 56 8bcf'); c.call(0x2879C90)
    c.raw('84c0'); c.je('reject')
    c.raw('897c2418 895c2414 61 b802000000 c20800')
    return c.finish()

def build(data):
    require = pe.require
    require(hashlib.sha256(data).hexdigest() == INPUT_SHA, 'exact baseline required')
    image = pe.PE(data)
    require(image.read(SITE, 5) == ORIGINAL, 'NOTI120 call changed')
    code = helper()
    code_section = next(s for s in image.sections if s['name'] == '.gld127')
    data_section = next(s for s in image.sections if s['name'] == '.a4rw')
    require(code_section['characteristics'] == 0x60000020, 'code is not RX')
    require(data_section['characteristics'] == 0xC0000040, 'counters are not RW')
    out = bytearray(data)
    ranges = []
    for section, va, payload in ((code_section, CODE, code),
                                  (data_section, DATA, bytes(len(COUNTERS)*4))):
        delta = va - image.base - section['rva']
        require(section['virtual_size'] <= delta and delta+len(payload) <= section['raw_size'],
                'helper/counters overlap occupied virtual bytes')
        offset = image.offset(va, len(payload))
        require(data[offset:offset+len(payload)] == bytes(len(payload)), 'padding occupied')
        out[offset:offset+len(payload)] = payload
        header = image.st + section['index']*40 + 8
        struct.pack_into('<I', out, header, delta+len(payload))
        ranges.extend(((offset, offset+len(payload)), (header, header+4)))
    offset = image.offset(SITE, 5)
    out[offset:offset+5] = b'\xe9' + struct.pack('<i', CODE-SITE-5)
    ranges.extend(((offset, offset+5), (image.opt+64, image.opt+68)))
    struct.pack_into('<I', out, image.opt+64, pe.pe_checksum(out, image.opt+64))
    restored = bytearray(out)
    for lo, hi in ranges:
        restored[lo:hi] = data[lo:hi]
    require(restored == data, 'unapproved byte delta')
    return bytes(out), dict(inputSHA256=INPUT_SHA, outputSHA256=hashlib.sha256(out).hexdigest(),
                           site=hex(SITE), codeVA=hex(CODE), codeSize=len(code), countersVA=hex(DATA),
                           counters=COUNTERS, allowedFileRanges=sorted(ranges),
                           liveValidated=False, installed=False, productionChanged=False)

if __name__ == '__main__':
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument('--input', type=Path, required=True)
    ap.add_argument('--output', type=Path, required=True)
    args = ap.parse_args()
    candidate, receipt = build(args.input.read_bytes())
    with args.output.open('xb') as f:
        f.write(candidate)
    print(json.dumps(receipt, indent=2))
