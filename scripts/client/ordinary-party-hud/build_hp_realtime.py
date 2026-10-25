"""Build the current-town P153 auxiliary-actor fix from exact published v173."""
import argparse
import hashlib
import json
import struct
from pathlib import Path

from hp_pe_support import PE, pe_checksum, helper, require
from hp_realtime_lookup import BASE, emit

INPUT_SHA = '9886866b28aef3ab6f97789fca9ba09b359b499735e7a80b3ca0ac6d08041060'
INPUT_SIZE = 111915008
SITE = 0x203323d


def build(data):
    require(len(data) == INPUT_SIZE and hashlib.sha256(data).hexdigest() == INPUT_SHA, 'exact published v173 required')
    pe = PE(data)
    require(pe.read(SITE, 5) == b'\xe8'+struct.pack('<i', 0x296d730-SITE-5), 'P153 lookup call differs')
    reviewed = helper()
    require(pe.read(0x6ec1000, len(reviewed)) == reviewed, 'current HUD lookup differs')
    code = emit()
    require(len(code) <= 0x200 and pe.read(BASE, 0x200) == bytes(0x200), 'helper window is occupied')
    out = bytearray(data)
    call_at, helper_at, checksum_at = pe.offset(SITE, 5), pe.offset(BASE, len(code)), pe.opt+64
    out[call_at:call_at+5] = b'\xe8'+struct.pack('<i', BASE-SITE-5)
    out[helper_at:helper_at+len(code)] = code
    struct.pack_into('<I', out, checksum_at, pe_checksum(out, checksum_at))
    ranges = ((call_at, call_at+5), (helper_at, helper_at+len(code)), (checksum_at, checksum_at+4))
    differences = [i for i,(a,b) in enumerate(zip(data,out)) if a != b]
    require(all(any(lo <= i < hi for lo,hi in ranges) for i in differences), 'unapproved byte delta')
    return bytes(out), dict(inputSha256=INPUT_SHA, outputSha256=hashlib.sha256(out).hexdigest(),
        length=len(out), changedOffsets=differences, helperVA=hex(BASE), helperSize=len(code),
        scope='P153 active lookup first; current ordinary-town party HUD cache fallback only', gameAcceptance=False)


def main():
    p=argparse.ArgumentParser(description=__doc__)
    for name in ('input','output','receipt'):p.add_argument('--'+name,type=Path,required=True)
    a=p.parse_args();require(not a.output.exists() and not a.receipt.exists(),'new output paths required')
    data,receipt=build(a.input.read_bytes());a.output.write_bytes(data)
    require(hashlib.sha256(a.output.read_bytes()).hexdigest()==receipt['outputSha256'],'readback mismatch')
    a.receipt.write_text(json.dumps(receipt,indent=2),encoding='utf8')
    print(json.dumps({k:v for k,v in receipt.items() if k!='changedOffsets'}))


if __name__=='__main__':main()
