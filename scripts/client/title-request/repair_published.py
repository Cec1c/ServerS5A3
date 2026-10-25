"""Repair the five title CALL/return comparisons in the published V173 image."""
import argparse
import json
import struct
from pathlib import Path

from pe_support import Image, available_memory, pe_checksum, sha256
from title_extension import OP19UI, OLD_VIRTUAL_SIZE, TITLE_RETURNS, title_extension

INPUT_SHA = '9886866b28aef3ab6f97789fca9ba09b359b499735e7a80b3ca0ac6d08041060'
INPUT_SIZE = 111915008


def build(data):
    available_memory()
    image = Image.from_bytes(data)
    if image.sha256 != INPUT_SHA or len(data) != INPUT_SIZE:
        raise ValueError('exact published V173 required')
    va = OP19UI + OLD_VIRTUAL_SIZE
    corrected = title_extension(va)
    previous = corrected
    for ret in TITLE_RETURNS:
        pattern = b'\x81\x7d\x04' + struct.pack('<I', ret)
        if previous.count(pattern) != 1:
            raise ValueError('ambiguous saved-return comparison')
        previous = previous.replace(pattern, b'\x81\x7d\x04' + struct.pack('<I', ret-5))
    if image.bytes_at(va, len(previous)) != previous:
        raise ValueError('published title extension differs')
    out = bytearray(data)
    offset = image.file_offset(va, len(corrected))
    out[offset:offset+len(corrected)] = corrected
    checksum = image.optional + 64
    struct.pack_into('<I', out, checksum, pe_checksum(out, checksum))
    allowed = set(range(checksum, checksum+4))
    for ret in TITLE_RETURNS:
        at = corrected.index(b'\x81\x7d\x04'+struct.pack('<I',ret))+3+offset
        allowed.update(range(at,at+4))
    differences = [i for i,(a,b) in enumerate(zip(data,out)) if a!=b]
    if len(out)!=len(data) or any(i not in allowed for i in differences):
        raise ValueError('unreviewed image change')
    result = bytes(out)
    return result, dict(inputSha256=INPUT_SHA, outputSha256=sha256(result), length=len(result),
                       changedOffsets=differences, preservedOtherBytes=True,
                       correctedReturns=[hex(x) for x in TITLE_RETURNS], gameAcceptance=False)


def main():
    p=argparse.ArgumentParser()
    p.add_argument('--input',type=Path,required=True)
    p.add_argument('--output',type=Path,required=True)
    p.add_argument('--receipt',type=Path,required=True)
    a=p.parse_args()
    if a.output.exists() or a.receipt.exists():
        raise ValueError('output paths must be new')
    data,receipt=build(a.input.read_bytes())
    a.output.write_bytes(data)
    if sha256(a.output.read_bytes())!=receipt['outputSha256']:
        raise ValueError('disk readback differs')
    a.receipt.write_text(json.dumps(receipt,indent=2),encoding='utf8')
    print(json.dumps(receipt))


if __name__=='__main__':
    main()
