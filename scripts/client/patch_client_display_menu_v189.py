"""Preserve literal channel names and same-channel proxy menus in exact V189."""
import argparse
import json
from pathlib import Path
import struct

import patch_receivers_v128 as pe

INPUT_SHA256 = '7c64c854a02ecc1c3bb6ae7ee174733c5b2e5957fa8a76b88db2110fcc3df4ba'
INPUT_SIZE = 111988736
# Only display-name fields. Dungeon keys and the shared string resolver remain
# unchanged. At these sites EAX points to the validated quoted token and ECX
# is its length minus one; the original code has not removed either quote yet.
SITES = (
    (0x1B4B3B0, 0x6EADEC0, 0x1B4B428, bytes.fromhex('6aff6a018d957cfdffff')),
    (0x1B4A91E, 0x6EADF40, 0x1B4A993, bytes.fromhex('6aff6a018d953cfdffff')),
)


def rel(op, address, target):
    return op + struct.pack('<i', target - address - len(op) - 4)


def helper(site, address, literal, stolen):
    code = bytearray(b'\x9c')
    branches = []
    # Empty names, resource syntax and already nested literal syntax retain
    # their original treatment. Nonempty ordinary text keeps its quotes so
    # the existing resolver can perform its normal literal conversion.
    for check, jump in ((bytes.fromhex('83f901'), b'\x0f\x86'),
                        (bytes.fromhex('8078013c'), b'\x0f\x84'),
                        (bytes.fromhex('80780160'), b'\x0f\x84')):
        code.extend(check)
        branches.append((len(code), jump))
        code.extend(bytes(6))
    code.extend(b'\x9d')
    code.extend(rel(b'\xe9', address + len(code), literal))
    legacy = address + len(code)
    code.extend(b'\x9d' + stolen)
    code.extend(rel(b'\xe9', address + len(code), site + len(stolen)))
    for at, jump in branches:
        code[at:at+6] = rel(jump, address + at, legacy)
    return bytes(code)


MENU_SITE = 0x3550E05
MENU_HELPER = 0x6EADF80
MENU_EXPECTED = bytes.fromhex('e826c941ff')


def menu_helper(address):
    # The caller has already matched the menu's real channel and directory.
    # Prefer a live scene actor, then use the same UID/channel/directory cache
    # as the existing open/build/click paths. Never manufacture an actor.
    code = bytearray(bytes.fromhex('ff742404'))
    code.extend(rel(b'\xe8', address+len(code), 0x296D730))
    code.extend(bytes.fromhex('83c40485c0'))
    branch = len(code)
    code.extend(bytes(6))
    code.extend(bytes.fromhex('0fb64649500fb6464850ff74240c'))
    code.extend(rel(b'\xe8', address+len(code), 0x1A5A5E0))
    code.extend(bytes.fromhex('83c40c'))
    done = address+len(code)
    code.extend(b'\xc3')
    code[branch:branch+6] = rel(b'\x0f\x85', address+branch, done)
    return bytes(code)


def build(data):
    if len(data) != INPUT_SIZE or pe.sha(data) != INPUT_SHA256:
        raise ValueError('exact published V189 input required')
    opt, sections = pe.headers(data)
    table = opt + struct.unpack_from('<H', data, opt - 4)[0]
    matches = [table+i*40 for i in range(len(sections))
               if data[table+i*40:table+i*40+8] == b'.r195rx\0']
    if len(matches) != 1:
        raise ValueError('game receive section missing')
    header = matches[0]
    vsize, rva, rawsize, raw = struct.unpack_from('<IIII', data, header+8)
    if (vsize, rva+pe.IMAGE_BASE, rawsize) != (0xEB7, 0x6EAD000, 4096):
        raise ValueError('published V189 section layout differs')
    if (struct.unpack_from('<I', data, header+36)[0] != 0x60000020
            or any(data[raw+vsize:raw+rawsize])
            or any(struct.unpack_from('<II', data, opt+128))):
        raise ValueError('RX padding or certificate contract differs')
    out = bytearray(data)
    ranges = [(opt+64, opt+68), (header+8, header+12)]
    end = vsize
    for site, address, literal, stolen in SITES:
        code = helper(site, address, literal, stolen)
        if not pe.IMAGE_BASE+rva+vsize <= address < address+len(code) <= pe.IMAGE_BASE+rva+rawsize:
            raise ValueError('helper outside unused RX tail')
        at, dest = pe.offset(sections, site, len(stolen)), pe.offset(sections, address, len(code))
        if data[at:at+len(stolen)] != stolen or any(data[dest:dest+len(code)]):
            raise ValueError('native fingerprint or padding differs')
        out[at:at+len(stolen)] = rel(b'\xe9', site, address) + b'\x90'*(len(stolen)-5)
        out[dest:dest+len(code)] = code
        ranges.extend(((at, at+len(stolen)), (dest, dest+len(code))))
        end = max(end, address+len(code)-pe.IMAGE_BASE-rva)
    code = menu_helper(MENU_HELPER)
    if not pe.IMAGE_BASE+rva+end <= MENU_HELPER < MENU_HELPER+len(code) <= pe.IMAGE_BASE+rva+rawsize:
        raise ValueError('menu helper overlaps display helpers or RX boundary')
    at = pe.offset(sections, MENU_SITE, len(MENU_EXPECTED))
    dest = pe.offset(sections, MENU_HELPER, len(code))
    if data[at:at+len(MENU_EXPECTED)] != MENU_EXPECTED or any(data[dest:dest+len(code)]):
        raise ValueError('menu lifetime fingerprint or padding differs')
    context = pe.offset(sections, MENU_SITE-5, 26)
    if data[context:context+26] != bytes.fromhex('0fb7562452e826c941ff83c40485c00f8508010000e955fbffff'):
        raise ValueError('menu lifetime decision context differs')
    out[at:at+5] = rel(b'\xe8', MENU_SITE, MENU_HELPER)
    out[dest:dest+len(code)] = code
    ranges.extend(((at,at+5),(dest,dest+len(code))))
    end = MENU_HELPER+len(code)-pe.IMAGE_BASE-rva
    struct.pack_into('<I', out, header+8, end)
    struct.pack_into('<I', out, opt+64, pe.checksum(out, opt+64))
    ranges.sort()
    # Enforce byte preservation outside the declared gameplay changes,
    # including all notification sections and original notification hooks.
    previous = 0
    for start, stop in ranges:
        if data[previous:start] != out[previous:start]:
            raise AssertionError('undeclared byte change')
        previous = stop
    if data[previous:] != out[previous:]:
        raise AssertionError('undeclared trailing change')
    return bytes(out), dict(inputSha256=INPUT_SHA256, outputSha256=pe.sha(out),
                           size=len(out), changedRanges=ranges,
                           displayNameFields=['server', 'dungeon'],
                           menuLifetimeLookup=True,
                           resourceSyntaxPreserved=True, noticeBytesPreserved=True,
                           gameAccepted=False)


def write_candidate(source, destination):
    source, destination = Path(source).resolve(), Path(destination).resolve()
    if source == destination or destination.exists():
        raise ValueError('separate new output required')
    out, receipt = build(source.read_bytes())
    destination.parent.mkdir(parents=True, exist_ok=True)
    with destination.open('xb') as stream:
        stream.write(out)
    if pe.sha(source.read_bytes()) != INPUT_SHA256 or pe.sha(destination.read_bytes()) != receipt['outputSha256']:
        raise RuntimeError('input or candidate readback differs')
    return receipt


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input', required=True, type=Path)
    parser.add_argument('--output', required=True, type=Path)
    args = parser.parse_args()
    print(json.dumps(write_candidate(args.input, args.output), indent=2))
