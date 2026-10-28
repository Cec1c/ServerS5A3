"""Carry reviewed social fixes forward from the published V183 baseline."""
import argparse
import json
from pathlib import Path
import struct

import patch_receivers_v128 as pe
import patch_guild_social_channel_v179 as social
import patch_main_whisper_v179 as whisper

INPUT_SHA256 = 'd27ad8902d991861fa0fdfe439f6c5a6ff2896a77f83987782093cdb7136085a'
INPUT_SIZE = 111984640
PREVIOUS_GATE = 0x6EADA00


def build(data):
    if len(data) != INPUT_SIZE or pe.sha(data) != INPUT_SHA256:
        raise ValueError('exact published V183 input required')
    opt, sections = pe.headers(data)
    table = opt + struct.unpack_from('<H', data, opt - 4)[0]
    headers = [table + i * 40 for i in range(len(sections))
               if data[table+i*40:table+i*40+8] == b'.r195rx\0']
    if len(headers) != 1:
        raise ValueError('game receive section missing')
    header = headers[0]
    vsize, rva, rawsize, raw = struct.unpack_from('<IIII', data, header + 8)
    if (vsize, rva + pe.IMAGE_BASE, rawsize) != (0xB1D, 0x6EAD000, 4096):
        raise ValueError('game receive section layout changed')
    if (struct.unpack_from('<I', data, header + 36)[0] != 0x60000020
            or any(data[raw+vsize:raw+rawsize])
            or any(struct.unpack_from('<II', data, opt + 128))):
        raise ValueError('game receive padding or signature contract changed')

    out = bytearray(data)
    ranges = [(opt+64, opt+68), (header+8, header+12)]

    def replace(va, expected, replacement):
        if len(expected) != len(replacement):
            raise ValueError('replacement size differs')
        at = pe.offset(sections, va, len(expected))
        if data[at:at+len(expected)] != expected:
            raise ValueError('native fingerprint changed at %08x' % va)
        out[at:at+len(expected)] = replacement
        ranges.append((at, at+len(expected)))

    # Chain through the existing gate, which already carries controls, ranking,
    # mail and Fate. The only new receive permission is class 1 / 804.
    replace(social.CALL_VA, whisper.rel(b'\xe8', social.CALL_VA, PREVIOUS_GATE),
            whisper.rel(b'\xe8', social.CALL_VA, social.HELPER_VA))
    for va, destination in social.FRIEND_BRANCHES:
        replace(va, whisper.rel(b'\x0f\x85', va, destination),
                whisper.rel(b'\xe9', va, destination) + b'\x90')
    for va, expected, helper in (
            (whisper.GUARD_SITE, bytes.fromhex('8b85b8fcffff'), whisper.GUARD_VA),
            (whisper.DISPLAY_SITE, bytes.fromhex('baa8010000'), whisper.DISPLAY_VA)):
        replace(va, expected, whisper.rel(b'\xe9', va, helper) + b'\x90'*(len(expected)-5))

    blocks = [(social.HELPER_VA, social.helper(PREVIOUS_GATE)),
              (whisper.GUARD_VA, whisper.guard()),
              (whisper.DISPLAY_VA, whisper.display())]
    for va, code in blocks:
        if va < pe.IMAGE_BASE+rva+vsize or va+len(code) > pe.IMAGE_BASE+rva+rawsize:
            raise ValueError('helper is outside reviewed game RX padding')
        replace(va, bytes(len(code)), code)
    struct.pack_into('<I', out, header+8,
                     max(va+len(code) for va, code in blocks)-pe.IMAGE_BASE-rva)
    struct.pack_into('<I', out, opt+64, pe.checksum(out, opt+64))
    ranges.sort()
    return bytes(out), dict(inputSha256=INPUT_SHA256, outputSha256=pe.sha(out),
                           size=len(out), changedRanges=ranges,
                           class1Added=[804], fallbackVA=hex(PREVIOUS_GATE),
                           explicitZeroFriendGroup=True, nativeOutgoingWhisper=True,
                           serverEcho=False, noticeBytesPreserved=True)


def write_candidate(source, destination):
    source, destination = Path(source).resolve(), Path(destination).resolve()
    if source == destination or destination.exists():
        raise ValueError('separate new output required')
    out, receipt = build(source.read_bytes())
    destination.parent.mkdir(parents=True, exist_ok=True)
    with destination.open('xb') as stream:
        stream.write(out)
    if (pe.sha(source.read_bytes()) != INPUT_SHA256
            or pe.sha(destination.read_bytes()) != receipt['outputSha256']):
        raise RuntimeError('input or output readback differs')
    return receipt


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input', required=True, type=Path)
    parser.add_argument('--output', required=True, type=Path)
    args = parser.parse_args()
    print(json.dumps(write_candidate(args.input, args.output), indent=2))
