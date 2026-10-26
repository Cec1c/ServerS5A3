"""Add the essence income notice to the exact cumulative guardian candidate.

This is a client dependency candidate. See Docs/seeker/GUILD_GUARDIAN_WAR_PLAN.md
for the outstanding state/transaction contract before integrating or releasing it.
"""
import argparse
import json
from pathlib import Path
import struct

import patch_receivers_v128 as pe

INPUT_SHA256 = '42ae36b74699265e07cf673aa5a70f9912f9b040d93859bb5738791fa81d0c67'
INPUT_SIZE = 111902720
CALL_VA = 0x2523A13
PREVIOUS_GATE = 0x6EAD400
HELPER_VA = 0x6EAD500
PROFILES = {INPUT_SHA256: ('guild-guardian-essence-v166', PREVIOUS_GATE, HELPER_VA, 0x43F)}


def helper(helper_va=HELPER_VA, previous_gate=PREVIOUS_GATE):
    # movzx eax,[esp+4]; cmp byte [edi],0; jne fallback;
    # cmp eax,1284; jne fallback; mov eax,1; ret 4; jmp previous.
    # Short branches above land at the final relative jump (offset 25).
    code = bytes.fromhex('0fb7442404803f00750f3d040500007508b801000000c20400')
    return code + b'\xe9' + struct.pack('<i', previous_gate-(helper_va+len(code)+5))


def build(data):
    digest = pe.sha(data)
    if len(data) != INPUT_SIZE or digest not in PROFILES:
        raise ValueError('exact cumulative guardian V166 candidate required')
    profile, previous_gate, helper_va, expected_vsize = PROFILES[digest]
    opt, sections = pe.headers(data)
    table = opt + struct.unpack_from('<H', data, opt - 4)[0]
    matches = [table + i * 40 for i in range(len(sections))
               if data[table+i*40:table+i*40+8] == b'.r195rx\0']
    if len(matches) != 1:
        raise ValueError('expected existing R195 receive section')
    header = matches[0]
    vsize, rva, rawsize, raw = struct.unpack_from('<IIII', data, header + 8)
    if (rva + pe.IMAGE_BASE != 0x6EAD000 or vsize != expected_vsize or rawsize != 4096
            or struct.unpack_from('<I', data, header+36)[0] != 0x60000020
            or any(struct.unpack_from('<II', data, opt+128))):
        raise ValueError('unexpected V166 receive-section contract')
    call = pe.offset(sections, CALL_VA, 5)
    if data[call:call+5] != b'\xe8' + struct.pack('<i', previous_gate-CALL_VA-5):
        raise ValueError('receive call no longer targets the verified chain')
    if any(data[raw+vsize:raw+rawsize]):
        raise ValueError('receive-section padding is occupied')
    code = helper(helper_va, previous_gate)
    at = pe.offset(sections, helper_va, len(code))
    out = bytearray(data)
    out[at:at+len(code)] = code
    out[call:call+5] = b'\xe8' + struct.pack('<i', helper_va-CALL_VA-5)
    struct.pack_into('<I', out, header+8, helper_va-(rva+pe.IMAGE_BASE)+len(code))
    struct.pack_into('<I', out, opt+64, pe.checksum(out, opt+64))
    return bytes(out), dict(profile=profile, inputSha256=digest, outputSha256=pe.sha(out),
        size=len(out), helperVA=hex(helper_va), fallbackVA=hex(previous_gate),
        changedRanges=sorted([(call,call+5),(at,at+len(code)),(header+8,header+12),(opt+64,opt+68)]),
        added=[dict(packetClass=0,opcode=1284)],
        nativeReceiversChanged=False, serverIncomeImplemented=True, firstSummonImplemented=False)


def write_candidate(source, destination):
    source, destination = Path(source).resolve(), Path(destination).resolve()
    if source == destination or destination.exists():
        raise ValueError('separate new output required')
    out, receipt = build(source.read_bytes())
    destination.parent.mkdir(parents=True, exist_ok=True)
    with destination.open('xb') as f:
        f.write(out)
    if pe.sha(destination.read_bytes()) != receipt['outputSha256']:
        raise RuntimeError('candidate readback mismatch')
    return receipt


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    print(json.dumps(write_candidate(args.input, args.output), indent=2))
