"""Keep peer Buff registrations when the local snapshot is replaced.

Fixed cumulative input; writes an independent candidate, never a live process.
"""
from pathlib import Path
import argparse
import hashlib
import importlib.util
import json
import struct

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location('buff_snapshot_pe', HERE.parent/'quiet-profile-refresh/build_candidate.py')
peutils = importlib.util.module_from_spec(spec)
spec.loader.exec_module(peutils)
INPUT_SHA = '52223a060313b352fe43e718bc813ccb6824ea07913579f097d699dd21a91a1e'
INPUT_SIZE = 112607232
SITE = 0x9988F5
HELPER = 0x6F88300
GLOBAL_RESET = 0x997210
ACTOR_RESET = 0x997C30
LOCAL_ACTOR = 0x57B6FB0
ORIGINAL = bytes.fromhex('e816e9ffff')


def helper():
    # ECX already holds the native manager. Actor-reset is thiscall/ret4;
    # the original call site and its packet reader retain their original ABI.
    code = bytearray(b'\xa1' + struct.pack('<I', LOCAL_ACTOR) + b'\x85\xc0\x74\x07\x50')
    code += b'\xe8' + struct.pack('<i', ACTOR_RESET-(HELPER+len(code)+5))
    code += b'\xc3'
    code += b'\xe9' + struct.pack('<i', GLOBAL_RESET-(HELPER+len(code)+5))
    return bytes(code)


def build(data):
    require = peutils.require
    require(len(data) == INPUT_SIZE and hashlib.sha256(data).hexdigest() == INPUT_SHA, 'fixed cumulative input differs')
    pe = peutils.PE(data)
    pe.require_contiguous()
    require(pe.read(SITE, 5) == ORIGINAL, 'snapshot reset call changed')
    require(pe.read(ACTOR_RESET, 9) == bytes.fromhex('558bec83ec2c53568b'), 'native actor-reset entry changed')
    require(pe.read(0x997DCA, 3) == bytes.fromhex('c20400'), 'native actor-reset ABI changed')
    section = next(s for s in pe.sections if s['name'] == '.gld127')
    code = helper()
    delta = HELPER-pe.base-section['rva']
    require(section['characteristics'] == 0x60000020, 'helper section is not RX')
    require(delta >= section['virtual_size'] and delta+len(code) <= section['raw_size'], 'helper exceeds unused padding')
    cave = pe.offset(HELPER, len(code))
    require(data[cave:cave+len(code)] == bytes(len(code)), 'helper padding occupied')
    site = pe.offset(SITE, 5)
    header = pe.st+section['index']*40+8
    out = bytearray(data)
    out[cave:cave+len(code)] = code
    out[site:site+5] = b'\xe8'+struct.pack('<i', HELPER-SITE-5)
    struct.pack_into('<I', out, header, delta+len(code))
    struct.pack_into('<I', out, pe.opt+64, peutils.pe_checksum(out, pe.opt+64))
    ranges = sorted([(cave,cave+len(code)),(site,site+5),(header,header+4),(pe.opt+64,pe.opt+68)])
    restored = bytearray(out)
    for a,b in ranges:
        restored[a:b] = data[a:b]
    require(restored == data, 'unexpected byte changes')
    result = bytes(out)
    require(peutils.PE(result).read(HELPER,len(code)) == code, 'helper readback failed')
    return result, dict(inputSha256=INPUT_SHA, outputSha256=hashlib.sha256(result).hexdigest(), size=len(result),
        site=hex(SITE), helper=hex(HELPER), helperBytes=code.hex(), allowedRanges=ranges,
        installed=False, released=False)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    result, receipt = build(args.input.read_bytes())
    with args.output.open('xb') as stream:
        stream.write(result)
    assert hashlib.sha256(args.output.read_bytes()).hexdigest() == receipt['outputSha256']
    print(json.dumps(receipt, indent=2))
