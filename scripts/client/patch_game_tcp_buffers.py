"""Raise the exact V209 game TCP socket buffers from 1 KiB to 256 KiB.

The source image is read-only. The command writes a separate candidate with the
same length, changing only the immediate in the audited buffer setup path and
the PE checksum.
"""
import argparse
import hashlib
import json
from pathlib import Path
import struct

from patch_receivers_v128 import checksum, headers, offset


INPUT_SHA256 = '478f4f5dc070253e7679aa7d8f8fda3b71ea6376569fc794865c44d0107098f4'
INPUT_SIZE = 112549888

CONNECT_FUNCTION_VA = 0x38C8250
CONNECT_FUNCTION_SIZE = 0x3D1
MANAGER_METHOD_VA = 0x374FA00
DIRECT_CALL_VA = 0x374FB2A
ENTRY_CALLS = (
    (0x1F4F3A1, MANAGER_METHOD_VA),
    (0x25247EB, MANAGER_METHOD_VA),
)

BUFFER_INSTRUCTION_VA = 0x38C83F9
BUFFER_INSTRUCTION_BEFORE = bytes.fromhex('b800040000')
BUFFER_INSTRUCTION_AFTER = bytes.fromhex('b800000400')
BUFFER_BEFORE = 0x400
BUFFER_AFTER = 0x40000

FUNCTION_PREFIX = bytes.fromhex('558bec83ec40a1dc3b5b0533c58945fc56')
SOCKET_SEQUENCE_VA = 0x38C8358
SOCKET_SEQUENCE = bytes.fromhex('6a066a016a02ff15f0865804')
SEND_BUFFER_SEQUENCE_VA = 0x38C83F9
SEND_BUFFER_SEQUENCE = bytes.fromhex(
    'b8000400006a048945c08945c48d45c050680110000068ffff000051ffd7'
)
RECEIVE_BUFFER_SEQUENCE_VA = 0x38C8435
RECEIVE_BUFFER_SEQUENCE = bytes.fromhex(
    '8b86c40100006a048d55c452680210000068ffff000050ffd7'
)
TCP_NODELAY_SEQUENCE_VA = 0x38C846C
TCP_NODELAY_SEQUENCE = bytes.fromhex(
    '8b96c40100006a048d4dc8516a016a0652c745c801000000ffd7'
)


def sha(data):
    return hashlib.sha256(data).hexdigest()


def _require_call(data, sections, call_va, expected_target):
    at = offset(sections, call_va, 5)
    if data[at] != 0xE8:
        raise ValueError('unexpected connection entry call')
    target = call_va + 5 + struct.unpack_from('<i', data, at + 1)[0]
    if target != expected_target:
        raise ValueError('connection entry call target changed')


def _require_bytes(data, sections, va, expected, message):
    at = offset(sections, va, len(expected))
    if data[at:at + len(expected)] != expected:
        raise ValueError(message)


def _validate_exact_layout(data):
    if len(data) != INPUT_SIZE or sha(data) != INPUT_SHA256:
        raise ValueError('exact V209 input SHA-256/length mismatch')

    opt, sections = headers(data)
    _require_call(data, sections, DIRECT_CALL_VA, CONNECT_FUNCTION_VA)
    for call_va, expected_target in ENTRY_CALLS:
        _require_call(data, sections, call_va, expected_target)

    _require_bytes(data, sections, CONNECT_FUNCTION_VA, FUNCTION_PREFIX,
                   'connection function prologue changed')
    _require_bytes(data, sections, SOCKET_SEQUENCE_VA, SOCKET_SEQUENCE,
                   'AF_INET/SOCK_STREAM/IPPROTO_TCP creation changed')
    _require_bytes(data, sections, SEND_BUFFER_SEQUENCE_VA,
                   SEND_BUFFER_SEQUENCE, 'TCP send-buffer setup changed')
    _require_bytes(data, sections, RECEIVE_BUFFER_SEQUENCE_VA,
                   RECEIVE_BUFFER_SEQUENCE, 'TCP receive-buffer setup changed')
    _require_bytes(data, sections, TCP_NODELAY_SEQUENCE_VA,
                   TCP_NODELAY_SEQUENCE, 'TCP_NODELAY setup changed')

    function_at = offset(sections, CONNECT_FUNCTION_VA, CONNECT_FUNCTION_SIZE)
    function_bytes = data[function_at:function_at + CONNECT_FUNCTION_SIZE]
    if function_bytes.count(BUFFER_INSTRUCTION_BEFORE) != 1:
        raise ValueError('buffer immediate is not unique in connection function')
    return opt, sections


def build(data):
    opt, sections = _validate_exact_layout(data)
    at = offset(sections, BUFFER_INSTRUCTION_VA, len(BUFFER_INSTRUCTION_BEFORE))
    out = bytearray(data)
    out[at:at + len(BUFFER_INSTRUCTION_AFTER)] = BUFFER_INSTRUCTION_AFTER

    checksum_at = opt + 64
    struct.pack_into('<I', out, checksum_at, checksum(out, checksum_at))
    result = bytes(out)
    if len(result) != len(data):
        raise RuntimeError('candidate length changed')

    changed_offsets = [index for index, (before, after)
                       in enumerate(zip(data, result)) if before != after]
    changed_ranges = [[at + 2, at + 4], [checksum_at, checksum_at + 4]]
    receipt = {
        'input_sha256': INPUT_SHA256,
        'input_size': len(data),
        'output_sha256': sha(result),
        'output_size': len(result),
        'length_unchanged': True,
        'connect_function_va': hex(CONNECT_FUNCTION_VA),
        'buffer_instruction_va': hex(BUFFER_INSTRUCTION_VA),
        'buffer_instruction_file_offset': at,
        'buffer_instruction_before': BUFFER_INSTRUCTION_BEFORE.hex(),
        'buffer_instruction_after': BUFFER_INSTRUCTION_AFTER.hex(),
        'send_buffer_bytes': {'before': BUFFER_BEFORE, 'after': BUFFER_AFTER},
        'receive_buffer_bytes': {'before': BUFFER_BEFORE, 'after': BUFFER_AFTER},
        'changed_ranges': changed_ranges,
        'changed_file_offsets': changed_offsets,
        'scope': 'client_exe_only',
        'online_verified': False,
    }
    return result, receipt


def write_candidate(source, destination):
    source, destination = Path(source).resolve(), Path(destination).resolve()
    if source == destination or destination.exists():
        raise ValueError('output must be a separate new file')

    source_before = source.read_bytes()
    result, receipt = build(source_before)
    destination.parent.mkdir(parents=True, exist_ok=True)
    with destination.open('xb') as stream:
        stream.write(result)

    if source.read_bytes() != source_before:
        raise RuntimeError('input changed while writing candidate')
    if destination.read_bytes() != result:
        raise RuntimeError('output readback mismatch')
    return receipt


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    print(json.dumps(write_candidate(args.input, args.output), indent=2))


if __name__ == '__main__':
    main()
