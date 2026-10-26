"""Keep V173 guild-war balances current without rebuilding an unchanged base.

Only the 1297 receiver's base-scene side effects are conditional. The complete
model copy, UI updates, upgrade notifications and scene-entry builder remain.
"""
import argparse
import json
from pathlib import Path
import struct

import patch_receivers_v128 as pe
from patch_at5t_input_priority import Code

INPUT_SHA256 = '9886866b28aef3ab6f97789fca9ba09b359b499735e7a80b3ca0ac6d08041060'
INPUT_SIZE = 111915008
HONGBAO_SHA256 = '3f835ec79827f5ddb62506ca8b248ff802f84623ad25fc923224dbcf950066cb'
FRAME_SITE, COMPARE_SITE, SCENE_SITE = 0x1077461, 0x107758F, 0x1077652
FINGERPRINTS = (
    (FRAME_SITE, bytes.fromhex('81ec080f0000')),
    (COMPARE_SITE, bytes.fromhex('b925020000')),
    (SCENE_SITE, bytes.fromhex('e839b1f200')),
)
# AP/GP balances and per-guardian invested AP/GP are numerical progress.
# Retain the original refresh for every other changed byte, including all
# building records, selected ID, guardian ID/level/skills and unknown fields.
NUMERIC_RANGES = ((0x17, 0x1F), *( (0x7F8+18*i+10, 0x7F8+18*i+18) for i in range(5)))


def helper(base):
    c = Code(base)
    c.label('compare')
    c.emit('9c 60 fc 8bd7')  # save flags/registers; EDX=old snapshot base
    c.emit('c785e8f0ffff01000000')  # one extra frame-local u32 at ebp-0xF18
    cursor = 0
    for lo, hi in (*NUMERIC_RANGES, (0x895, 0x895)):
        if lo > cursor:
            # EDI still points at the old snapshot; the new body is on stack.
            c.emit('8db5'); c.data.extend(struct.pack('<i', -0x8A8+cursor))
            c.emit('8dba'); c.data.extend(struct.pack('<I', cursor))
            c.emit('b9'); c.data.extend(struct.pack('<I', lo-cursor))
            c.emit('f3a6')
            c.branch('0f85', 'compared')
        cursor = hi
    c.emit('c785e8f0ffff00000000')
    c.label('compared')
    c.emit('61 9d b925020000 c3')  # replay replaced MOV ECX, 0x225
    c.label('scene')
    # This hook is reached only in town8 AND the configured guardian area.
    # Other areas keep every original branch and side effect.
    c.emit('83bde8f0ffff00')
    c.branch('0f84', 0x107770E)
    c.branch('e8', 0x1FA2790)
    c.branch('e9', SCENE_SITE+5)
    return c.finish(), c.labels


def build(data):
    input_sha = pe.sha(data)
    if len(data) != INPUT_SIZE or input_sha not in (INPUT_SHA256, HONGBAO_SHA256):
        raise ValueError('exact official V173 or verified V173 hongbao input required')
    opt, sections = pe.headers(data)
    coff = opt-24
    count = struct.unpack_from('<H', data, coff+6)[0]
    table = opt+struct.unpack_from('<H', data, coff+20)[0]
    header = table+count*40
    size_headers = struct.unpack_from('<I', data, opt+60)[0]
    if header+40 > size_headers or any(data[header:header+40]):
        raise ValueError('no empty section header')
    if struct.unpack_from('<II', data, opt+32) != (4096, 4096) or any(struct.unpack_from('<II', data, opt+128)):
        raise ValueError('unexpected PE alignment or certificate')
    if len(data)%4096 or max(raw+size for _,size,raw in sections) != len(data):
        raise ValueError('unexpected overlay or raw alignment')
    for va, expected in FINGERPRINTS:
        at = pe.offset(sections, va, len(expected))
        if data[at:at+len(expected)] != expected:
            raise ValueError('native receiver fingerprint mismatch')
    image_size = struct.unpack_from('<I', data, opt+56)[0]
    end = max(struct.unpack_from('<I',data,table+i*40+12)[0] + max(struct.unpack_from('<I',data,table+i*40+8)[0],struct.unpack_from('<I',data,table+i*40+16)[0]) for i in range(count))
    if image_size != (end+4095)&~4095:
        raise ValueError('unexpected image end')
    code, labels = helper(pe.IMAGE_BASE+image_size)
    if len(code)>4096:
        raise ValueError('helper exceeds new section')
    out = bytearray(data)
    out.extend(code+bytes(4096-len(code)))
    changes = []
    for va, original in FINGERPRINTS:
        at = pe.offset(sections, va, len(original))
        if va == FRAME_SITE:
            replacement = bytes.fromhex('81ec0c0f0000')
        else:
            opcode = b'\xe8' if va == COMPARE_SITE else b'\xe9'
            target = labels['compare' if va == COMPARE_SITE else 'scene']
            replacement = opcode+struct.pack('<i',target-va-5)
        out[at:at+len(original)] = replacement
        changes.append((at,at+len(original)))
    struct.pack_into('<H',out,coff+6,count+1)
    struct.pack_into('<I',out,opt+4,struct.unpack_from('<I',data,opt+4)[0]+4096)
    struct.pack_into('<I',out,opt+56,image_size+4096)
    struct.pack_into('<8sIIIIIIHHI',out,header,b'.gwr173',len(code),image_size,4096,len(data),0,0,0,0,0x60000020)
    struct.pack_into('<I',out,opt+64,pe.checksum(out,opt+64))
    changes += [(coff+6,coff+8),(opt+4,opt+8),(opt+56,opt+60),(opt+64,opt+68),(header,header+40)]
    changes.sort()
    cursor=0
    for lo,hi in changes:
        assert data[cursor:lo]==out[cursor:lo]
        cursor=hi
    assert data[cursor:]==out[cursor:len(data)]
    receipt=dict(inputSha256=input_sha,outputSha256=pe.sha(out),size=len(out),
        changedRanges=changes,newSectionVA=hex(pe.IMAGE_BASE+image_size),helperLength=len(code),
        labels={k:hex(v) for k,v in labels.items()},numericRanges=NUMERIC_RANGES,
        receiverModelAndUIKept=True,sceneEntryKept=True,receiveGatesKept=True)
    return bytes(out),receipt


def write_candidate(source,destination):
    source,destination=Path(source).resolve(),Path(destination).resolve()
    if source==destination or destination.exists():
        raise ValueError('distinct new output required')
    out,receipt=build(source.read_bytes())
    destination.parent.mkdir(parents=True,exist_ok=True)
    with destination.open('xb') as stream:stream.write(out)
    if pe.sha(destination.read_bytes())!=receipt['outputSha256'] or pe.sha(source.read_bytes())!=receipt['inputSha256']:
        raise RuntimeError('file readback mismatch')
    return receipt


if __name__=='__main__':
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input',type=Path,required=True)
    parser.add_argument('--output',type=Path,required=True)
    args=parser.parse_args()
    print(json.dumps(write_candidate(args.input,args.output),indent=2))
