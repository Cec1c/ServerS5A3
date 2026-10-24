"""Exact-input shared raid display readiness candidate; no installation or live process access."""
import argparse, hashlib, json, struct, sys
from pathlib import Path
sys.dont_write_bytecode=True
import build_readiness as old
import build_readiness_v2 as v2
pe=old.pe
SHA='9327087d147576314dd95c6c0b0a638b1525bca48734658fe4f9b97dc33f82ea'
SIZE=111865856
SITES={
    'capture':(0x201DCBF,bytes.fromhex('8b0dd0376a05')),
    'mark':(0x202FCC8,bytes.fromhex('8b0d98376a05')),
    'gate':(0x192F6ED,bytes.fromhex('e8aef9ffff')),
    'phase_clear':(0x2001517,bytes.fromhex('8975fc8d4de4')),
    'zero_clear':(0x20012D7,bytes.fromhex('c745fcffffffff')),
    'leave':(0x1A64B8D,bytes.fromhex('897e04c7460800000000')),
}
# Reuse v2's private 44-byte layout; active slots 0..12/20 remain reserved.
# +12 mode+1, +16 fresh model, +24 manager, +28 raid, +32 key, +36 result phase, +40 disposition.
def clear(c):
    for off in range(0,44,4):
        c.raw('c70300000000' if off==0 else f'c743{off:02x}00000000')

def current_result(c,fail):
    c.raw('8b0d78376a05 85c9');c.je(fail)
    c.raw('3b4b18');c.jne(fail)
    c.raw('837904ff');c.je(fail)
    c.raw('8b5104 3b5320');c.jne(fail)
    c.raw('8b4108 85c0');c.je(fail)
    c.raw('3b431c');c.jne(fail)
    c.raw('8b5024 42 3b530c');c.jne(fail)

def helpers(code_va,data_va):
    c=old.Code(code_va)
    c.label('capture');c.raw('9c60');c.imm('bb',data_va);clear(c)
    c.raw('8b3d78376a05 85ff');c.je('capture_done')
    c.raw('8b7704 83feff');c.je('capture_done')
    c.raw('8bcf');c.call(0x1A553E0)
    c.raw('85c0');c.je('capture_done')
    c.raw('3b3d78376a05');c.jne('capture_done')
    c.raw('3b7704');c.jne('capture_done')
    c.raw('3b4708');c.jne('capture_done')
    c.raw('8b4824 83f902');c.branch('0f87','capture_done')
    c.raw('41 894b0c')
    c.raw('897b18 89431c 897320 0fb655a5 895324')
    c.raw('837d9c00');c.jne('capture_invalid')
    c.raw('83fa01');c.branch('0f87','capture_invalid')
    c.raw('0fb645af 83f801');c.branch('0f87','capture_invalid')
    c.raw('40 894328');c.jump('capture_done')
    c.label('capture_invalid');c.raw('c7432803000000')
    c.label('capture_done');c.raw('619d 8b0dd0376a05');c.jump(0x201DCC5)

    c.label('mark');c.raw('9c60');c.imm('bb',data_va)
    c.raw('807dcf03');c.jne('mark_done')
    c.raw('807dce00');c.je('mark_done')
    c.raw('8b7db0 85ff');c.je('mark_done')
    current_result(c,'mark_done')
    c.raw('8b4704 3b430c');c.jne('mark_done')
    c.raw('837b2801');c.jne('mark_done')
    c.raw('897b10')
    c.label('mark_done');c.raw('619d 8b0d98376a05');c.jump(0x202FCCE)

    c.label('gate');c.raw('9c60');c.imm('bb',data_va)
    c.raw('837c242804');c.jne('pass')
    c.raw('8b742418 85f6');c.je('pass')
    c.raw('83be2004000003');c.jne('pass')
    c.raw('80be2804000000');c.jne('pass')
    c.raw('8b7dec 85ff');c.je('pass')
    c.raw('8b4704 48 83f802');c.branch('0f87','pass')
    c.raw('8b0d98376a05 85c9');c.je('pass')
    c.raw('3bb1ccc30000');c.jne('pass')
    c.raw('8b0d78376a05 85c9');c.je('pass')
    c.raw('837904ff');c.je('pass')
    c.raw('8b4108 85c0');c.je('pass')
    c.raw('8b5024 83fa02');c.branch('0f87','pass')
    c.raw('42 3b5704');c.jne('pass')
    # Even a window opened before network587 must wait for its own result.
    current_result(c,'wait')
    c.raw('837b2802');c.je('pass')
    c.raw('837b2803');c.je('pass')
    c.raw('837b2801');c.jne('wait')
    c.raw('3b7b10');c.jne('wait')
    c.raw('6a00 6a02 8bcf');c.call(0x192AD70)
    c.raw('85c0');c.je('wait')
    c.label('pass');c.raw('619d');c.jump(0x192F0A0)
    c.label('wait');c.raw('619d c20400')

    c.label('phase_clear');c.raw('9c60');c.imm('bb',data_va);clear(c)
    c.raw('619d 8975fc 8d4de4');c.jump(0x200151D)
    c.label('zero_clear');c.raw('9c60');c.imm('bb',data_va);clear(c)
    c.raw('619d c745fcffffffff');c.jump(0x20012DE)
    c.label('leave');c.raw('9c60');c.imm('bb',data_va)
    c.raw('3b3578376a05');c.jne('leave_done')
    clear(c)
    c.label('leave_done');c.raw('619d 897e04 c7460800000000');c.jump(0x1A64B97)
    return c.done(),{name:code_va+c.labels[name] for name in SITES}

def build(src):
    available=old.memory_guard()
    assert len(src)==SIZE and pe.sha(src)==SHA,'fixed v154 required'
    opt,sec=pe.headers(src);p=struct.unpack_from('<I',src,0x3c)[0]
    count=struct.unpack_from('<H',src,p+6)[0]
    hdr=opt+struct.unpack_from('<H',src,p+20)[0]+count*40
    assert count==37 and hdr+80<=4096 and not any(src[hdr:hdr+80])
    assert struct.unpack_from('<II',src,opt+32)==(4096,4096)
    assert struct.unpack_from('<I',src,opt+56)[0]==SIZE
    assert not struct.unpack_from('<H',src,opt+70)[0]&0x40
    for va,size,sha in v2.ANCHORS:
        off=pe.offset(sec,va,size);assert pe.sha(src[off:off+size])==sha
    cv=pe.IMAGE_BASE+SIZE;dv=cv+4096;code,entries=helpers(cv,dv)
    assert len(code)<=4096
    out=bytearray(src)+code+bytes(8192-len(code))
    ranges=[(p+6,p+8),(opt+4,opt+12),(opt+56,opt+60),(opt+64,opt+68),(hdr,hdr+80)]
    for name,(va,raw) in SITES.items():
        off=pe.offset(sec,va,len(raw));assert src[off:off+len(raw)]==raw
        out[off:off+len(raw)]=(b'\xe8' if name=='gate' else b'\xe9')+struct.pack('<i',entries[name]-va-5)+b'\x90'*(len(raw)-5)
        ranges.append((off,off+len(raw)))
    struct.pack_into('<H',out,p+6,count+2)
    for field,inc in [(opt+4,4096),(opt+8,4096),(opt+56,8192)]:
        struct.pack_into('<I',out,field,struct.unpack_from('<I',src,field)[0]+inc)
    for i,(name,size,flags) in enumerate([(b'.a4rx',len(code),0x60000020),(b'.a4rw',44,0xC0000040)]):
        struct.pack_into('<8sIIIIIIHHI',out,hdr+i*40,name,size,SIZE+i*4096,4096,SIZE+i*4096,0,0,0,0,flags)
    struct.pack_into('<I',out,opt+64,pe.checksum(out,opt+64))
    last=0
    for lo,hi in sorted(ranges):assert src[last:lo]==out[last:lo];last=hi
    assert src[last:]==out[last:SIZE]
    return out,dict(input_sha256=SHA,output_sha256=pe.sha(out),code_va=cv,data_va=dv,code_size=len(code),entries=entries,allowed_ranges=sorted(ranges),available_before=available,protection_floor_gib=3,status='experimental_not_release',deployed=False)

if __name__=='__main__':
    ap=argparse.ArgumentParser();ap.add_argument('--input',type=Path,required=True);ap.add_argument('--output',type=Path,required=True);a=ap.parse_args()
    assert not a.output.exists(),'new output directory required'
    out,r=build(a.input.read_bytes());a.output.mkdir(parents=True)
    (a.output/'candidate.exe').write_bytes(out)
    (a.output/'build.json').write_text(json.dumps(r,indent=2)+'\n',encoding='utf-8')
    print(json.dumps(r))
