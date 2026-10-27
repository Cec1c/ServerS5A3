"""Bind RaidMatchingWindow PartyFace to its actual raid cache namespace.

Builds a new local candidate; never edits an installed client. The exact
input already includes v152 job changes and the validated volcano candidate.
"""
from pathlib import Path
import sys, struct, json, argparse
sys.dont_write_bytecode = True
import pe_support as pe

HASH = 'aac86cd74eb2a0155f025e92c394124ffaaeceecf50479fe26e178fe2b04d661'
SIZE = 111853568
SITE = 0x1DF054F
ORIGINAL = bytes.fromhex('5353518b8d6cffffff')
NEXT = SITE + len(ORIGINAL)

def helper(va):
    # EDI is the raid object; ECX is participant key. Each push/xchg loads
    # one zero-extended byte without changing EAX, flags, or other registers.
    code = bytearray.fromhex('50 0fb64703 870424 50 0fb64702 870424 51 8b8d6cffffff e9')
    code.extend(struct.pack('<i', NEXT-(va+len(code)+4)))
    return bytes(code)

def build(data):
    if len(data) != SIZE or pe.sha(data) != HASH:
        raise ValueError('exact cumulative volcano candidate required')
    opt, sections = pe.headers(data)
    header = opt + struct.unpack_from('<H', data, opt-4)[0] + len(sections)*40
    assert len(sections) == 34 and header+40 <= 4096 and not any(data[header:header+40])
    assert struct.unpack_from('<II', data, opt+32) == (4096, 4096)
    assert struct.unpack_from('<I', data, opt+56)[0] == SIZE
    assert not any(struct.unpack_from('<II', data, opt+128))
    site = pe.offset(sections, SITE, len(ORIGINAL))
    assert data[site:site+len(ORIGINAL)] == ORIGINAL
    va = pe.IMAGE_BASE + SIZE
    code = helper(va)
    out = bytearray(data)
    out.extend(code + bytes(4096-len(code)))
    out[site:site+len(ORIGINAL)] = b'\xe9' + struct.pack('<i', va-SITE-5) + b'\x90'*4
    struct.pack_into('<H', out, opt-18, len(sections)+1)
    struct.pack_into('<I', out, opt+4, struct.unpack_from('<I', data, opt+4)[0]+4096)
    struct.pack_into('<I', out, opt+56, SIZE+4096)
    struct.pack_into('<8sIIIIIIHHI', out, header, b'.rface', len(code), SIZE, 4096, SIZE, 0,0,0,0,0x60000020)
    struct.pack_into('<I', out, opt+64, pe.checksum(out, opt+64))
    ranges = sorted([(site,site+len(ORIGINAL)),(opt-18,opt-16),(opt+4,opt+8),(opt+56,opt+60),(opt+64,opt+68),(header,header+40)])
    cursor = 0
    for lo,hi in ranges:
        assert data[cursor:lo] == out[cursor:lo]
        cursor=hi
    assert data[cursor:] == out[cursor:SIZE]
    receipt=dict(input_sha256=HASH,output_sha256=pe.sha(out),size=len(out),site=hex(SITE),helper_va=hex(va),helper_size=len(code),allowed_original_ranges=ranges,published=False,deployed=False,native_gui_accepted=False)
    return bytes(out),receipt

if __name__ == '__main__':
    parser=argparse.ArgumentParser()
    parser.add_argument('--input', required=True, type=Path)
    parser.add_argument('--output', required=True, type=Path)
    parser.add_argument('--receipt', required=True, type=Path)
    args=parser.parse_args()
    if args.output.exists() or args.receipt.exists(): raise ValueError('new output paths required')
    data,receipt=build(args.input.read_bytes())
    with args.output.open('xb') as f: f.write(data)
    with args.receipt.open('x',encoding='utf8') as f: json.dump(receipt,f,indent=2)
    print(json.dumps(receipt))
