"""Restore V179's prepared outgoing main-whisper record after local send."""
import argparse
import json
from pathlib import Path
import struct

import patch_receivers_v128 as pe
from patch_guild_grade_region import checksum

INPUT_SHA256 = '4bc360ccbdb5f569553d49813705c1afa4ec73ed6ff25ae7105fcb8fc2e41a64'
GUARD_VA, DISPLAY_VA = 0x6EADE00, 0x6EADE80
GUARD_SITE, DISPLAY_SITE = 0x2A039EE, 0x2A03C4F


def rel(op, address, target):
    return op+struct.pack('<i', target-address-len(op)-4)


def guard():
    code = bytearray(b'\x9c')
    code.extend(bytes.fromhex('83bdb8fcffff01'))  # type 1 only
    jne1 = len(code); code.extend(bytes(6))
    code.extend(bytes.fromhex('6683bdc4fcffff11'))  # actual op17
    jne2 = len(code); code.extend(bytes(6))
    code.extend(bytes.fromhex('85c0'))  # HRESULT returned by native finish
    jns = len(code); code.extend(bytes(6))
    code.extend(b'\x9d')
    code.extend(rel(b'\xe9', GUARD_VA+len(code), 0x2A03C5E))
    normal = GUARD_VA+len(code)
    code.extend(bytes.fromhex('9d8b85b8fcffff'))
    code.extend(rel(b'\xe9', GUARD_VA+len(code), GUARD_SITE+6))
    for at, op in ((jne1, b'\x0f\x85'), (jne2, b'\x0f\x85'), (jns, b'\x0f\x89')):
        code[at:at+6] = rel(op, GUARD_VA+at, normal)
    return bytes(code)


def display():
    code = bytearray(bytes.fromhex('9c6083bdb8fcffff01'))
    jne1 = len(code); code.extend(bytes(6))
    code.extend(bytes.fromhex('6683bdc4fcffff11'))
    jne2 = len(code); code.extend(bytes(6))
    # This site is reached only after the original record constructor.
    # The record already owns peer name, original text and the local UID.
    code.extend(bytes.fromhex('8d8588fdffff508bcb'))
    code.extend(rel(b'\xe8', DISPLAY_VA+len(code), 0x29FFDC0))
    done = DISPLAY_VA+len(code)
    code.extend(bytes.fromhex('619dbaa8010000'))
    code.extend(rel(b'\xe9', DISPLAY_VA+len(code), DISPLAY_SITE+5))
    for at in (jne1, jne2):
        code[at:at+6] = rel(b'\x0f\x85', DISPLAY_VA+at, done)
    return bytes(code)


def build(data):
    if pe.sha(data) != INPUT_SHA256:
        raise ValueError('exact cumulative controls/social/member candidate required')
    opt, sections = pe.headers(data)
    table = opt+struct.unpack_from('<H', data, opt-4)[0]
    matches = [table+i*40 for i in range(len(sections)) if data[table+i*40:table+i*40+8] == b'.r195rx\0']
    if len(matches) != 1: raise ValueError('receive section missing')
    header = matches[0]
    vsize, rva, rawsize, raw = struct.unpack_from('<IIII', data, header+8)
    if (vsize, rva+pe.IMAGE_BASE, rawsize) != (0xD1D, 0x6EAD000, 4096):
        raise ValueError('cumulative section contract differs')
    if struct.unpack_from('<I', data, header+36)[0] != 0x60000020 or any(data[raw+vsize:raw+rawsize]):
        raise ValueError('RX padding contract differs')
    if any(struct.unpack_from('<II', data, opt+128)): raise ValueError('signed input unsupported')
    out, ranges = bytearray(data), [(opt+64, opt+68), (header+8, header+12)]
    for site, expected, helper, code in ((GUARD_SITE, bytes.fromhex('8b85b8fcffff'), GUARD_VA, guard()),
                                           (DISPLAY_SITE, bytes.fromhex('baa8010000'), DISPLAY_VA, display())):
        at, dest = pe.offset(sections, site, len(expected)), pe.offset(sections, helper, len(code))
        if data[at:at+len(expected)] != expected: raise ValueError('native site differs')
        out[at:at+len(expected)] = rel(b'\xe9', site, helper)+b'\x90'*(len(expected)-5)
        out[dest:dest+len(code)] = code
        ranges.extend(((at, at+len(expected)), (dest, dest+len(code))))
    struct.pack_into('<I', out, header+8, DISPLAY_VA-(rva+pe.IMAGE_BASE)+len(display()))
    struct.pack_into('<I', out, opt+64, checksum(out, opt+64))
    return bytes(out), dict(inputSha256=pe.sha(data), outputSha256=pe.sha(out), changedRanges=sorted(ranges),
                           source='native op17/type1 prepared local-UID record', serverEcho=False)


def write_candidate(source, destination):
    source, destination = Path(source).resolve(), Path(destination).resolve()
    if source == destination or destination.exists(): raise ValueError('separate new output required')
    out, receipt = build(source.read_bytes())
    destination.parent.mkdir(parents=True, exist_ok=True)
    with destination.open('xb') as stream: stream.write(out)
    if pe.sha(source.read_bytes()) != receipt['inputSha256'] or pe.sha(destination.read_bytes()) != receipt['outputSha256']:
        raise RuntimeError('candidate readback differs')
    return receipt


if __name__ == '__main__':
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--input', required=True, type=Path)
    p.add_argument('--output', required=True, type=Path)
    a = p.parse_args()
    print(json.dumps(write_candidate(a.input, a.output), indent=2))
