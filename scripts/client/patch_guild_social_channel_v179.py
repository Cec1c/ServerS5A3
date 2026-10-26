"""Repair channel lookup reception and explicit zero-group friends in V179."""
import argparse
import json
from pathlib import Path
import struct

import patch_receivers_v128 as pe
from patch_guild_grade_region import checksum

INPUT_SHA256 = '72254abd5214bc24f8bcb76a16184e07105dde33f4e762b4c71c2e264bc7789c'
CALL_VA = 0x2523A13
PREVIOUS_GATE = 0x6EAD900
HELPER_VA = 0x6EADC00
CONTROLS_SHA256 = '4e66a1bf755736ed11e22ace5cc971b7d61e6e5321563f8c609ea6e58f815c47'
FRIEND_BRANCHES = ((0x29C2E0E, 0x29C2EBE), (0x29C2FD8, 0x29C3088))


def helper(previous_gate):
    code = bytearray.fromhex('803f01')  # Only an ACK, not a notification.
    code.extend(b'\x0f\x85' + struct.pack('<i', previous_gate - (HELPER_VA + 9)))
    code.extend(bytes.fromhex('0fb74424043d24030000'))
    code.extend(b'\x0f\x85' + struct.pack('<i', previous_gate - (HELPER_VA + 25)))
    code.extend(bytes.fromhex('b801000000c20400'))
    return bytes(code)


def build(data):
    digest = pe.sha(data)
    if digest not in (INPUT_SHA256, CONTROLS_SHA256):
        raise ValueError('exact V179 or reviewed V179 controls candidate required')
    previous_gate = PREVIOUS_GATE if digest == INPUT_SHA256 else 0x6EADA00
    expected_size = 0x99D if digest == INPUT_SHA256 else 0xB1D
    opt, sections = pe.headers(data)
    table = opt + struct.unpack_from('<H', data, opt - 4)[0]
    matches = [table + i * 40 for i in range(len(sections))
               if data[table+i*40:table+i*40+8] == b'.r195rx\0']
    if len(matches) != 1:
        raise ValueError('expected one existing receive-gate section')
    header = matches[0]
    vsize, rva, rawsize, raw = struct.unpack_from('<IIII', data, header + 8)
    if (rva + pe.IMAGE_BASE != 0x6EAD000 or vsize != expected_size or rawsize != 4096
            or struct.unpack_from('<I', data, header + 36)[0] != 0x60000020
            or any(struct.unpack_from('<II', data, opt + 128))):
        raise ValueError('unexpected gate section contract')
    call = pe.offset(sections, CALL_VA, 5)
    if data[call:call+5] != b'\xe8' + struct.pack('<i', previous_gate-CALL_VA-5):
        raise ValueError('unexpected receive call')
    code = helper(previous_gate)
    at = pe.offset(sections, HELPER_VA, len(code))
    if any(data[raw+vsize:raw+rawsize]):
        raise ValueError('gate padding is occupied')
    result = bytearray(data)
    friend_ranges = []
    for va, destination in FRIEND_BRANCHES:
        offset = pe.offset(sections, va, 6)
        if data[offset:offset+6] != b'\x0f\x85' + struct.pack('<i', destination-va-6):
            raise ValueError('unexpected friend zero-group fallback')
        # Keep the caller's authoritative group, including zero. The skipped
        # block substitutes the directory ID from NOTI1 field7 for group zero.
        result[offset:offset+6] = b'\xe9' + struct.pack('<i', destination-va-5) + b'\x90'
        friend_ranges.append((offset, offset+6))
    result[at:at+len(code)] = code
    result[call:call+5] = b'\xe8' + struct.pack('<i', HELPER_VA-CALL_VA-5)
    struct.pack_into('<I', result, header+8, HELPER_VA-(rva+pe.IMAGE_BASE)+len(code))
    struct.pack_into('<I', result, opt+64, checksum(result, opt+64))
    return bytes(result), dict(inputSha256=digest, outputSha256=pe.sha(result),
        helperVA=hex(HELPER_VA), fallbackVA=hex(previous_gate), class1Added=[804],
        explicitZeroFriendGroup=True,
        changedRanges=sorted([(call, call+5), (at, at+len(code)),
                              (header+8, header+12), (opt+64, opt+68)] + friend_ranges))


def write_candidate(source, destination):
    source, destination = Path(source).resolve(), Path(destination).resolve()
    if source == destination or destination.exists():
        raise ValueError('separate new output required')
    data, receipt = build(source.read_bytes())
    destination.parent.mkdir(parents=True, exist_ok=True)
    with destination.open('xb') as stream:
        stream.write(data)
    if pe.sha(source.read_bytes()) != receipt['inputSha256'] or pe.sha(destination.read_bytes()) != receipt['outputSha256']:
        raise RuntimeError('input or candidate readback mismatch')
    return receipt


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input', required=True, type=Path)
    parser.add_argument('--output', required=True, type=Path)
    args = parser.parse_args()
    print(json.dumps(write_candidate(args.input, args.output), indent=2))
