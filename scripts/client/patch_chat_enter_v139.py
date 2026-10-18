"""Route main-scene Return through the existing native text-input channel.

Only the main-scene caller of the generic key publisher omits its DI Return
fallback. WM/text Return, every other key, and all other publisher callers
retain their original paths. No global input state or widget is modified.
"""
import argparse
import json
from pathlib import Path
import struct
import patch_receivers_v128 as pe

INPUT_SHA256 = '9363574786928ec58ab6f82be0d40847a814d6e4ef42753a1a54750134a09d9c'
INPUT_SIZE = 111812608
PATCH_VA = 0x29BA981
MAIN_RETURN = 0x2A0E0FA
CONTINUE = 0x29BA987
SKIP = 0x29BA9EB
HELPER_OFFSET = 512
ORIGINAL = bytes.fromhex('8b0d98376a05')


def helper(va):
    b = bytearray(b'\x81\x7d\x04' + struct.pack('<I', MAIN_RETURN))
    b += b'\x0f\x84' + struct.pack('<i', SKIP - (va + len(b) + 6))
    b += ORIGINAL
    b += b'\xe9' + struct.pack('<i', CONTINUE - (va + len(b) + 5))
    assert len(b) == 24
    return bytes(b)


def build(data):
    if len(data) != INPUT_SIZE or pe.sha(data) != INPUT_SHA256:
        raise ValueError('exact released v138 input required')
    opt, sections = pe.headers(data)
    header = opt + struct.unpack_from('<H', data, opt - 4)[0] + 23 * 40
    if len(sections) != 24 or data[header:header+8] != b'.clsgate':
        raise ValueError('unexpected adapter section')
    vsize, rva, capacity, raw = struct.unpack_from('<IIII', data, header+8)
    if vsize != 448 or capacity != 4096 or struct.unpack_from('<I', data, header+36)[0] != 0x60000020:
        raise ValueError('unexpected RX adapter layout')
    if any(data[raw+448:raw+capacity]) or any(struct.unpack_from('<II',data,opt+128)):
        raise ValueError('unexpected padding or certificate')
    at = pe.offset(sections, PATCH_VA, 6)
    if data[at:at+6] != ORIGINAL:
        raise ValueError('unexpected generic publisher guard')
    caller = pe.offset(sections, MAIN_RETURN-5, 5)
    if data[caller:caller+5] != b'\xe8' + struct.pack('<i',0x29BA8F0-MAIN_RETURN):
        raise ValueError('unexpected main-scene caller')
    # This exact predicate ensures all non-Return keys bypass our helper.
    if data[at-9:at] != bytes.fromhex('83ff100f8589000000'):
        raise ValueError('unexpected Return-only branch')
    helper_va = pe.IMAGE_BASE + rva + HELPER_OFFSET
    code = helper(helper_va)
    out = bytearray(data)
    out[at:at+6] = b'\xe9' + struct.pack('<i',helper_va-PATCH_VA-5) + b'\x90'
    out[raw+HELPER_OFFSET:raw+HELPER_OFFSET+len(code)] = code
    struct.pack_into('<I',out,header+8,HELPER_OFFSET+len(code))
    struct.pack_into('<I',out,opt+64,0)
    struct.pack_into('<I',out,opt+64,pe.checksum(out,opt+64))
    receipt = dict(input_sha256=INPUT_SHA256,output_sha256=pe.sha(out),
        input_size=len(data),output_size=len(out),patch_va=hex(PATCH_VA),
        main_scene_return=hex(MAIN_RETURN),helper_va=hex(helper_va),helper_size=len(code),
        helper_offset=HELPER_OFFSET,input_helper_size=448,adapter_virtual_size=536,
        helper_capacity=capacity,receive_adapter_preserved=True,
        original_guard_for_other_callers=True,global_input_state_modified=False,
        file_size_unchanged=True,full_ui_acceptance=False,
        expected_original=ORIGINAL.hex(),patch_offset=at,
        helper_file_offset=raw+HELPER_OFFSET,section_virtual_size_offset=header+8,
        checksum_offset=opt+64)
    return bytes(out),receipt


def main():
    p=argparse.ArgumentParser();p.add_argument('input',type=Path);p.add_argument('output',type=Path)
    p.add_argument('--receipt',type=Path,required=True);a=p.parse_args()
    if a.output.exists() or a.receipt.exists():raise SystemExit('output already exists')
    out,r=build(a.input.read_bytes());a.output.parent.mkdir(parents=True,exist_ok=True)
    with a.output.open('xb') as f:f.write(out)
    with a.receipt.open('x',encoding='utf-8') as f:json.dump(r,f,indent=2)
    print(json.dumps(r))


if __name__=='__main__':main()
