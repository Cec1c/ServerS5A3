"""Admit only the native class1/CMD121 completion on the exact cumulative EXE.

Produces an independent candidate. The original callback, party projection,
transport and all other receive decisions remain unchanged.
"""
from pathlib import Path
import argparse
import hashlib
import importlib.util
import json
import struct

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('host_pe', ROOT / 'quiet-profile-refresh/build_candidate.py')
peutils = importlib.util.module_from_spec(spec)
spec.loader.exec_module(peutils)
INPUT_SHA = 'd333e4ce5f210a254932d7ba6e06e0649fc37f45ec248f033534e175e72e2d0b'
INPUT_SIZE = 112607232
CALL = 0x2523A13
FALLBACK = 0x6F20400
HELPER = 0x6F9DC00

def helper():
    code = bytearray.fromhex('0fb7442404803f01')
    def branch(op, target):
        code.extend(bytes.fromhex(op))
        code.extend(struct.pack('<i', target - (HELPER + len(code) + 4)))
    branch('0f85', FALLBACK)
    code.extend(bytes.fromhex('3d79000000'))
    branch('0f85', FALLBACK)
    code.extend(bytes.fromhex('b801000000c20400'))
    return bytes(code)

def build(data):
    require = peutils.require
    require(len(data) == INPUT_SIZE and hashlib.sha256(data).hexdigest() == INPUT_SHA,
            'exact cumulative input required; repeated patches rejected')
    pe = peutils.PE(data)
    pe.require_contiguous()
    require(pe.read(CALL, 5) == b'\xe8' + struct.pack('<i', FALLBACK-CALL-5),
            'receive gate hook changed')
    require(pe.read(0x1FA56A0, 9) == bytes.fromhex('558bec807d0c007534'),
            'native completion callback changed')
    section = next(s for s in pe.sections if s['name'] == '.krtext')
    require(section['virtual_size'] == 0xCB1F and section['raw_size'] == 0xD000
            and section['characteristics'] == 0x60000020, 'RX padding layout changed')
    code = helper()
    pos = pe.offset(HELPER, len(code))
    delta = HELPER-pe.base-section['rva']
    require(section['virtual_size'] <= delta and delta+len(code) <= section['raw_size'],
            'helper exceeds unused RX padding')
    require(data[pos:pos+len(code)] == bytes(len(code)), 'helper padding is occupied')
    header = pe.st+section['index']*40+8
    call = pe.offset(CALL, 5)
    result = bytearray(data)
    result[pos:pos+len(code)] = code
    result[call:call+5] = b'\xe8'+struct.pack('<i', HELPER-CALL-5)
    struct.pack_into('<I', result, header, delta+len(code))
    struct.pack_into('<I', result, pe.opt+64, peutils.pe_checksum(result, pe.opt+64))
    ranges = sorted([(pos,pos+len(code)), (call,call+5), (header,header+4), (pe.opt+64,pe.opt+68)])
    normalized = bytearray(result)
    for lo,hi in ranges:
        normalized[lo:hi] = data[lo:hi]
    require(normalized == data, 'change outside reviewed ranges')
    final = peutils.PE(result)
    final.require_contiguous()
    require(final.count == pe.count and len(result) == len(data), 'image layout changed')
    return bytes(result), dict(inputSha256=INPUT_SHA, outputSha256=hashlib.sha256(result).hexdigest(),
        size=len(result), changedRanges=ranges, admittedPair=[1,121], helper=hex(HELPER),
        nativeCallback=hex(0x1FA56A0), installed=False, gameplayAcceptance=False)

if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    result, receipt = build(args.input.read_bytes())
    with args.output.open('xb') as stream:
        stream.write(result)
    peutils.require(hashlib.sha256(args.output.read_bytes()).hexdigest() == receipt['outputSha256'], 'disk readback mismatch')
    print(json.dumps(receipt, indent=2))
