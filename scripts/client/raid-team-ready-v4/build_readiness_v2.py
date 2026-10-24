"""Offline v2: raid-specific 601 disposition plus the existing Anton gate.

The v1 source and candidate remain immutable. No installation is implemented.
"""
import argparse
import json
import struct
import sys
from pathlib import Path
sys.dont_write_bytecode=True
import build_readiness as v1
pe=v1.pe
INPUT_SHA=v1.INPUT_SHA
INPUT_SIZE=v1.INPUT_SIZE
SITES=dict(v1.SITES,capture=(0x201DCBF,bytes.fromhex('8b0dd0376a05')))
ANCHORS=v1.ANCHORS+[
    (0x20325F7,19,'01bb3d465ce0d2562f54adb09d1ec863558247517b5793145f47bd2df7babf28'),
    (0x201DC5E,121,'a4de796dbd8aec65bb68a99c75a4d50a65821b79073484fe6f56574a381f4a36'),
    (0x2678190,16,'4227c0f94a6599614413c17d15a843494ed6b87bf6be815e34554e2e64c49a63'),
]
# Active: outer(0), manager(4), raid(8), key(12), freshModel(16), disposition(20).
# Result: manager(24), raid(28), key(32), rawPhase(36), disposition(40).
# Dispositions: 0 unknown/unbound, 1 eligible, 2 denied, 3 invalid-bypass.
DATA_SIZE=44


def helpers(code_va,data_va):
    c=v1.Code(code_va)
    c.label('capture')
    c.raw('9c60');c.imm('bb',data_va)
    # Invalidate active before parsing/looking up the next result. Old tokens
    # are never dereferenced; zero all result fields before publishing status.
    c.raw('c70300000000 c7431000000000 c7431400000000')
    for offset in (24,28,32,36,40): c.raw(f'c743{offset:02x}00000000')
    c.raw('8b3d78376a05 85ff');c.je('capture_done')
    c.raw('8b7704 83feff');c.je('capture_done')
    c.raw('8bcf');c.call(0x1A553E0)  # native lookup may fill manager+8
    c.raw('85c0');c.je('capture_done')
    c.raw('3b3d78376a05');c.jne('capture_done')
    c.raw('3b7704');c.jne('capture_done')
    c.raw('3b4708');c.jne('capture_done')
    c.raw('83782400');c.jne('capture_done')
    c.raw('897b18 89431c 897320 0fb655a5 895324')
    c.raw('837d9c00');c.jne('capture_invalid')
    c.raw('83fa01');c.branch('0f87','capture_invalid')
    c.raw('0fb645af 83f801');c.branch('0f87','capture_invalid')
    c.raw('40 894328');c.jump('capture_done') # option0->1 / option1->2
    c.label('capture_invalid');c.raw('c7432803000000')
    c.label('capture_done');c.raw('619d 8b0dd0376a05');c.jump(0x201DCC5)

    c.label('reset')
    c.raw('9c60');c.imm('bb',data_va)
    c.raw('c70300000000 c7431000000000 c7431400000000')
    c.raw('8b742418 85f6');c.je('reset_done')
    c.raw('3b3598376a05');c.jne('reset_done')
    c.raw('8bb6ccc30000 85f6');c.je('reset_done')
    c.raw('8b0d78376a05 85c9');c.je('reset_done')
    c.raw('837904ff');c.je('reset_done')
    c.raw('8b4108 85c0');c.je('reset_done')
    c.raw('83782400');c.jne('reset_done')
    c.raw('894b04 894308 8b5104 89530c 8933')
    c.raw('3b4b18');c.jne('reset_done')
    c.raw('3b431c');c.jne('reset_done')
    c.raw('3b5320');c.jne('reset_done')
    c.raw('8b4328 894314')             # preserve result across repeated 587
    c.label('reset_done');c.raw('619d');c.jump(0x32B6530)

    c.label('mark')
    c.raw('9c60');c.imm('bb',data_va)
    c.raw('807dcf03');c.jne('mark_done')
    c.raw('807dce00');c.je('mark_done')
    c.raw('8b7db0 85ff');c.je('mark_done')
    c.raw('837f0401');c.jne('mark_done')
    c.raw('8b0d98376a05 85c9');c.je('mark_done')
    c.raw('8bb1ccc30000 85f6');c.je('mark_done')
    v1.identity(c,'mark_done');c.raw('897b10')
    c.label('mark_done');c.raw('619d 8b0d98376a05');c.jump(0x202FCCE)

    c.label('gate')
    c.raw('9c60');c.imm('bb',data_va)
    c.raw('837c242804');c.jne('pass')
    c.raw('8b742418 85f6');c.je('pass')
    c.raw('83be2004000003');c.jne('pass')
    c.raw('80be2804000000');c.jne('pass')
    c.raw('8b7dec 85ff');c.je('pass')
    c.raw('837f0401');c.jne('pass')
    v1.identity(c,'pass')
    c.raw('837b1401');c.je('owned')    # no scene/party prerequisite
    c.raw('837b1400');c.jne('pass')   # denied/invalid-bypass wins over old party
    c.raw('8b0d58376a05 85c9');c.je('pass')
    c.raw('83797000');c.je('pass')
    c.raw('6a00 8bcf');c.call(0x192B0D0)
    c.raw('84c0');c.jne('owned')
    c.raw('6a00 8bcf');c.call(0x192B140)
    c.raw('84c0');c.je('pass')
    c.label('owned')
    c.raw('3b7b10');c.jne('wait')
    c.raw('6a00 6a02 8bcf');c.call(0x192AD70)
    c.raw('85c0');c.je('wait')
    c.label('pass');c.raw('619d');c.jump(0x192F0A0)
    c.label('wait');c.raw('619d c20400')
    return c.done(),{name:code_va+c.labels[name] for name in SITES}


def build(original):
    # Reuse immutable exact-input checks and PE layout; replace only our own
    # new section payload/three branches before adding the fourth branch.
    out,r=v1.build(original)
    opt,sections=pe.headers(original)
    for va,size,sha in ANCHORS:
        off=pe.offset(sections,va,size)
        if pe.sha(original[off:off+size])!=sha:raise ValueError(f'anchor {va:x}')
    code,entries=helpers(r['code_va'],r['data_va'])
    if len(code)>4096:raise ValueError('code exceeds one page')
    out[INPUT_SIZE:INPUT_SIZE+4096]=code+bytes(4096-len(code))
    for name,(va,raw) in SITES.items():
        off=pe.offset(sections,va,len(raw))
        if original[off:off+len(raw)]!=raw:raise ValueError('hook changed')
        op=b'\xe9' if name in ('mark','capture') else b'\xe8'
        out[off:off+len(raw)]=op+struct.pack('<i',entries[name]-va-5)+b'\x90'*(len(raw)-5)
    p=struct.unpack_from('<I',original,0x3c)[0]
    header=opt+struct.unpack_from('<H',original,p+20)[0]+37*40
    struct.pack_into('<I',out,header+8,len(code))
    struct.pack_into('<I',out,header+40+8,DATA_SIZE)
    struct.pack_into('<I',out,opt+64,pe.checksum(out,opt+64))
    off=pe.offset(sections,SITES['capture'][0],6)
    ranges=sorted(r['allowed_original_ranges']+[(off,off+6)])
    cursor=0
    for lo,hi in ranges:assert original[cursor:lo]==out[cursor:lo];cursor=hi
    assert original[cursor:]==out[cursor:INPUT_SIZE]
    r.update(version=2,output_sha256=pe.sha(out),code_size=len(code),data_size=DATA_SIZE,
             entries=entries,allowed_original_ranges=ranges,
             native_anchors=[{'va':hex(va),'size':size,'sha256':sha} for va,size,sha in ANCHORS],
             dispositions={'unknown':0,'eligible':1,'denied':2,'invalid_bypass':3})
    return out,r


def main():
    ap=argparse.ArgumentParser();ap.add_argument('--input',type=Path,required=True)
    ap.add_argument('--output-dir',type=Path,required=True);a=ap.parse_args()
    output=a.output_dir/'DNF-r195-readiness-v2.exe';receipt=a.output_dir/'build-v2.json'
    if output.exists() or receipt.exists():raise SystemExit('new outputs required')
    out,r=build(a.input.read_bytes());a.output_dir.mkdir(parents=True,exist_ok=True)
    output.write_bytes(out);receipt.write_text(json.dumps(r,indent=2)+'\n',encoding='utf-8')
    print(json.dumps({'sha256':r['output_sha256'],'code_size':r['code_size'],'data_size':DATA_SIZE}))

if __name__=='__main__':main()
