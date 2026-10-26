"""Build the exact local-face-based empire monster name patch; never deploy."""
from __future__ import annotations

import argparse
from array import array
import ctypes
import hashlib
import json
import os
from pathlib import Path
import struct
import sys

INPUT_SHA = '8ab4297d1e3ebe2dc9d71801ebae210cb3ec673dbcdf707b35a3d5c8faf6f790'
INPUT_SIZE = 111865856
START, END, BRANCH, CAVE = 0x1466150, 0x1466200, 0x1466172, 0x14661F2
FUNCTION = bytes.fromhex(
    '558bec8b45083da3b560067f3f74623d721501007f17742b2d60f90000740583e8037565'
    'b8170201005dc204002d73150100740648740c48754fb8150201005dc20400b814020100'
    '5dc204003de7bb60067f27743b2da5b56006741583e80274072d2f0300007521b81602'
    '01005dc20400b8190201005dc204002deabb6006741883e804740a48741083c8ff5dc2'
    '0400b8130201005dc20400b8180201005dc20400' + 'cc' * 14)
NEXT = bytes.fromhex('8b0de8f1660585c974088b018b106a01')


def require(condition, message):
    if not condition:
        raise ValueError(message)


def memory_guard():
    if os.name != 'nt':
        raise RuntimeError('This pinned offline builder requires Windows memory guard')
    class MemoryStatus(ctypes.Structure):
        _fields_ = [('length', ctypes.c_ulong), ('load', ctypes.c_ulong)] + [
            (name, ctypes.c_ulonglong) for name in ('total', 'available', 'page_total',
                                                   'page_available', 'virtual_total',
                                                   'virtual_available', 'extended')]
    status = MemoryStatus()
    status.length = ctypes.sizeof(status)
    require(ctypes.windll.kernel32.GlobalMemoryStatusEx(ctypes.byref(status)), 'memory query failed')
    require(status.available >= 3 * 1024**3, 'available memory below 3 GiB')
    return round(status.available / 1024**3, 3)


def sha(data):
    return hashlib.sha256(data).hexdigest()


def checksum_offset(data):
    return struct.unpack_from('<I', data, 0x3C)[0] + 24 + 64


def pe_checksum(data):
    words = array('H')
    words.frombytes(data)
    if sys.byteorder != 'little':
        words.byteswap()
    index = checksum_offset(data) // 2
    words[index] = words[index + 1] = 0
    total = sum(words)
    while total >> 16:
        total = (total & 0xFFFF) + (total >> 16)
    return total + len(data)


def offset(data, va, size):
    require(data[:2] == b'MZ', 'MZ')
    pe = struct.unpack_from('<I', data, 0x3C)[0]
    require(data[pe:pe + 4] == b'PE\0\0', 'PE')
    require(struct.unpack_from('<H', data, pe + 4)[0] == 0x14C, 'x86')
    optional = pe + 24
    require(struct.unpack_from('<H', data, optional)[0] == 0x10B, 'PE32')
    base = struct.unpack_from('<I', data, optional + 28)[0]
    table = optional + struct.unpack_from('<H', data, pe + 20)[0]
    for i in range(struct.unpack_from('<H', data, pe + 6)[0]):
        row = table + i * 40
        _, rva, raw_size, raw = struct.unpack_from('<IIII', data, row + 8)
        if base + rva <= va and va + size <= base + rva + raw_size:
            require(raw + va - base - rva + size <= len(data), 'raw bounds')
            require(struct.unpack_from('<I', data, row + 36)[0] & 0x20000000, 'executable section')
            return raw + va - base - rva
    raise ValueError('unmapped VA')


def checked_input(path):
    data = path.read_bytes()
    require(len(data) == INPUT_SIZE and sha(data) == INPUT_SHA,
            'exact local-face SHA/size required; readiness and other inputs rejected')
    off = offset(data, START, END - START + len(NEXT))
    require(data[off:off + len(FUNCTION)] == FUNCTION, 'full original function/padding mismatch')
    require(data[off + len(FUNCTION):off + len(FUNCTION) + len(NEXT)] == NEXT, 'next function mismatch')
    require(struct.unpack_from('<I', data, checksum_offset(data))[0] == pe_checksum(data), 'input PE checksum')
    return data


def edits(data):
    # Short JNE ends at 6174; cave starts at 61F2 (rel8 = 0x7E).
    cave = b'\x83\xf8\x04\x0f\x84' + struct.pack('<i', 0x1466174 - (CAVE + 9))
    cave += b'\xe9' + struct.pack('<i', 0x14661D9 - (CAVE + 14))
    require(len(cave) == 14, 'cave length')
    return [(BRANCH, offset(data, BRANCH, 2), b'\x75\x7e'),
            (CAVE, offset(data, CAVE, 14), cave)]


def write_json(path, value):
    with path.open('x', encoding='utf-8', newline='\n') as stream:
        stream.write(json.dumps(value, ensure_ascii=False, indent=2) + '\n')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input', type=Path, required=True)
    parser.add_argument('--out-dir', type=Path, required=True)
    args = parser.parse_args()
    output = args.out_dir / 'DNF-r195-empire-name.exe'
    receipt_path = args.out_dir / 'build-receipt.json'
    for path in (output, receipt_path, args.out_dir / 'verify-receipt.json'):
        if path.exists():
            raise FileExistsError(f'refusing existing artifact: {path}; use a new --out-dir')
    available = memory_guard()
    data = checked_input(args.input)
    require(output.resolve() != args.input.resolve(), 'input must not be overwritten')
    result = bytearray(data)
    changes = []
    for va, off, replacement in edits(data):
        changes.append(dict(va=hex(va), file_offset=off, before=data[off:off + len(replacement)].hex(),
                            after=replacement.hex()))
        result[off:off + len(replacement)] = replacement
    checksum_off = checksum_offset(data)
    checksum = pe_checksum(result)
    struct.pack_into('<I', result, checksum_off, checksum)
    memory_guard()
    args.out_dir.mkdir(parents=True, exist_ok=True)
    receipt = dict(input=str(args.input), input_sha256=sha(data), output=str(output),
                   output_sha256=sha(result), size=len(result), changes=changes,
                   function_sha256=sha(FUNCTION), next_function_anchor=NEXT.hex(),
                   protectionFloorGiB=3, availableGiB=available, deployed=False,
                   pvf_accessed=False, protocol_changes=False, new_sections=False,
                   checksum_offset=checksum_off,
                   checksum_before=struct.unpack_from('<I', data, checksum_off)[0],
                   checksum_after=checksum, checksum_policy='recompute nonzero PE checksum')
    # Reserve both destinations exclusively before writing either payload. If reservation
    # fails, an empty newly created file may remain; existing files are never removed.
    with output.open('xb') as binary, receipt_path.open('x', encoding='utf-8', newline='\n') as log:
        binary.write(result)
        log.write(json.dumps(receipt, ensure_ascii=False, indent=2) + '\n')
    print(json.dumps(receipt, ensure_ascii=False))


if __name__ == '__main__':
    main()
