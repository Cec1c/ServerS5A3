"""Retire the local party record without destroying its living actor, V173."""
import argparse
import json
import struct
from pathlib import Path

from build_candidate import PE, checksum, sha, require
import helpers

INPUT_SHA = '9886866b28aef3ab6f97789fca9ba09b359b499735e7a80b3ca0ac6d08041060'
INPUT_SIZE = 111915008
BASE = 0x6EC5000


def build(data):
    require(len(data) == INPUT_SIZE and sha(data) == INPUT_SHA, 'exact published V173 required')
    pe = PE(data)
    previous, kind0 = helpers.emit(BASE)
    corrected, unchanged = helpers.emit(BASE, preserve_local=True)
    require(kind0 == unchanged and len(corrected) <= 0x200, 'unreviewed helper layout')
    require(pe.read(BASE, 0x200) == previous + bytes(0x200-len(previous)), 'published kind3 differs')
    require(pe.read(BASE+0x200, len(kind0)) == kind0, 'published kind0 differs')
    out = bytearray(data)
    at = pe.offset(BASE, 0x200)
    out[at:at+0x200] = corrected + bytes(0x200-len(corrected))
    cs = pe.opt+64
    struct.pack_into('<I', out, cs, checksum(out, cs))
    ranges = ((at, at+0x200), (cs, cs+4))
    differences = [i for i,(a,b) in enumerate(zip(data,out)) if a != b]
    require(all(any(lo <= i < hi for lo,hi in ranges) for i in differences), 'change outside approved helper/checksum')
    return bytes(out), dict(inputSha256=INPUT_SHA, outputSha256=sha(out), length=len(out),
                           helperVA=hex(BASE), helperBytes=corrected.hex(), changedOffsets=differences,
                           preservedOtherBytes=True, gameAcceptance=False)


def main():
    p=argparse.ArgumentParser()
    for arg in ('input','output','receipt'):p.add_argument('--'+arg,type=Path,required=True)
    a=p.parse_args()
    require(not a.output.exists() and not a.receipt.exists(), 'new output paths required')
    data,receipt=build(a.input.read_bytes())
    a.output.write_bytes(data)
    require(sha(a.output.read_bytes())==receipt['outputSha256'],'disk readback mismatch')
    a.receipt.write_text(json.dumps(receipt,indent=2),encoding='utf8')
    print(json.dumps({k:v for k,v in receipt.items() if k not in ('helperBytes','changedOffsets')}))


if __name__ == '__main__':main()
