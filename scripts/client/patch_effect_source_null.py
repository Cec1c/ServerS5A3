"""Guard a nullable effect-source lookup at native call site 010164B1.

Produces a separate candidate. Does not launch the game or publish an update.
"""
import argparse
import json
from pathlib import Path
import struct

import patch_receivers_v128 as pe
from patch_at5t_input_priority import Code

INPUTS = {
    '244b5c1e3126967d8a2b718658c586c1ac675a08b255d7377e95b93b4b815112': 112607232,
}
SITE = 0x010164B1
ORIGINAL = bytes.fromhex('8bc8 e868189301')
CONTINUE = 0x010164B8
CALLEE = 0x02947D20
HELPER = 0x06F88600


def helper(base):
    c = Code(base)
    c.emit('9c 85c0')  # Preserve the lookup's flags on both branches.
    c.branch('0f84', 'missing')
    c.emit('9d 8bc8')
    c.branch('e8', CALLEE)  # Retain thiscall and the five existing arguments.
    c.branch('e9', CONTINUE)
    c.label('missing')
    # No callee was entered: discharge exactly its five arguments, retaining
    # EAX=0. The original common tail writes this null result to row+18 and
    # restores saved registers. Do not leave a stale row result or return early.
    c.emit('9d 8d642414')
    c.branch('e9', CONTINUE)
    return c.finish()


def build(data):
    input_sha = pe.sha(data)
    if INPUTS.get(input_sha) != len(data):
        raise ValueError('exact verified cumulative input SHA256/size required')
    opt, sections = pe.headers(data)
    coff = opt - 24
    count = struct.unpack_from('<H', data, coff + 6)[0]
    table = opt + struct.unpack_from('<H', data, coff + 20)[0]
    if struct.unpack_from('<II', data, opt + 32) != (4096, 4096) or any(struct.unpack_from('<II', data, opt + 128)):
        raise ValueError('unexpected alignment or certificate')
    # The pinned cumulative image has 93 sections and no spare header slot.
    # Use zero padding after the retained-network, Buff, object-init and inspection helpers in the
    # existing RX section; do not move raw data or expand a writable section.
    headers = [table + i * 40 for i in range(count)
               if data[table + i * 40:table + i * 40 + 8].rstrip(b'\0') == b'.gld127']
    if len(headers) != 1:
        raise ValueError('expected unique helper section')
    header = headers[0]
    virtual_size, rva, raw_size, raw_offset = struct.unpack_from('<IIII', data, header + 8)
    if struct.unpack_from('<I', data, header + 36)[0] != 0x60000020:
        raise ValueError('helper section must remain read/execute only')
    code = helper(HELPER)
    delta = HELPER - pe.IMAGE_BASE - rva
    if delta < virtual_size or delta + len(code) > raw_size:
        raise ValueError('helper exceeds unused section padding')
    cave = pe.offset(sections, HELPER, len(code))
    if cave != raw_offset + delta or data[cave:cave + len(code)] != bytes(len(code)):
        raise ValueError('helper padding occupied or misaligned')
    at = pe.offset(sections, SITE, len(ORIGINAL))
    if data[at:at + len(ORIGINAL)] != ORIGINAL:
        raise ValueError('native fingerprint changed')
    for va, expected in (
        (0x0101649F, bytes.fromhex('6aff6a006aff51f30f1104246a018bceffd0')),
        (CONTINUE, bytes.fromhex('8d4d08518bcf8bf0e86bd6e1ff8970185f5e5b5dc20c00')),
        (CALLEE, bytes.fromhex('558bec83ec0c5356578b7d108bd985ff79096a01e8e78cfeff')),
        (0x02947D49, bytes.fromhex('5f5e33c05b8be55dc21400')),
    ):
        off = pe.offset(sections, va, len(expected))
        if data[off:off + len(expected)] != expected:
            raise ValueError('nullable effect caller/callee fingerprint changed at ' + hex(va))
    out = bytearray(data)
    out[cave:cave + len(code)] = code
    out[at:at + len(ORIGINAL)] = b'\xe9' + struct.pack('<i', HELPER - SITE - 5) + b'\x90' * (len(ORIGINAL) - 5)
    struct.pack_into('<I', out, header + 8, delta + len(code))
    struct.pack_into('<I', out, opt + 64, pe.checksum(out, opt + 64))
    changes = sorted([(at, at + len(ORIGINAL)), (cave, cave + len(code)),
                      (opt + 64, opt + 68), (header + 8, header + 12)])
    normalized = bytearray(out)
    for lo, hi in changes:
        normalized[lo:hi] = data[lo:hi]
    if normalized != data:
        raise ValueError('change outside approved ranges')
    return bytes(out), dict(inputSha256=input_sha, outputSha256=pe.sha(out), size=len(out),
                           helperVA=hex(HELPER), helperSize=len(code), changedRanges=changes,
                           helperSection='.gld127', appendedRange=None, playerAcceptance='pending', scope='call-site null-result guard; model visibility remains unresolved')


def write_candidate(source, destination):
    source, destination = Path(source).resolve(), Path(destination).resolve()
    if source == destination or destination.exists():
        raise ValueError('output must be a separate new file')
    result, receipt = build(source.read_bytes())
    with destination.open('xb') as stream:
        stream.write(result)
    if pe.sha(source.read_bytes()) != receipt['inputSha256'] or pe.sha(destination.read_bytes()) != receipt['outputSha256']:
        raise RuntimeError('input/output readback mismatch')
    return receipt


if __name__ == '__main__':
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--input', type=Path, required=True)
    p.add_argument('--output', type=Path, required=True)
    a = p.parse_args()
    print(json.dumps(write_candidate(a.input, a.output), indent=2))
