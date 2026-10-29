"""Render detached character profile profession data without a scene actor."""
import argparse
import json
from pathlib import Path
import struct
import patch_receivers_v128 as pe
from patch_at5t_input_priority import Code

INPUT_SHA256 = '70de273db4f5694de4eeb4686bf33bddb374173245544f0874624b562704bde7'
INPUT_SIZE = 111996928
SITE = 0x349805A
CONTINUE = 0x349808D
FINISH_ROW = 0x3498176
ORIGINAL = bytes.fromhex(
    '57 898568ffffff 8d70ff e8d7a34dff 8b5804 8b4808 2bcb '
    'b8abaaaa2a f7e9 d1fa 8bca c1e91f 03ca 83c40c 3bf1 7205 e824b89d00')


def helper(base):
    c = Code(base)
    # The native scene-level query has returned, leaving two cdecl arguments.
    # The renderer already decoded this target's profession experience.
    c.emit('83c408 898568ffffff 57')
    c.branch('e8', 0x2972440)
    c.emit('83c404 85c0')
    c.branch('0f84', 'missing')
    c.emit('8bf0 8b5804 85db')  # table object, vector begin
    c.branch('0f84', 'missing')
    c.emit('8b4808 3bcb')
    c.branch('0f86', 'missing')
    c.emit('2bcb 8bc1 33d2 b90c000000 f7f1 85d2')
    c.branch('0f85', 'missing')  # complete 12-byte rows only
    c.emit('8bc8 8b8568ffffff 83f8ff')
    c.branch('0f85', 'validate')
    # Only the native missing/unusable actor sentinel needs a fallback.
    # Keep an existing positive result, including its native level cap.
    c.emit('51 ffb578ffffff 8bce')
    c.branch('e8', 0x3D4CF60)  # existing table's experience-to-level method
    c.emit('59')
    c.label('validate')
    c.emit('85c0')
    c.branch('0f8e', 'missing')
    c.emit('898568ffffff 8d70ff 3bf1')
    c.branch('0f83', 'missing')
    c.branch('e9', CONTINUE)
    c.label('missing')
    # Keep the row label but omit an unavailable value. Do not report "none"
    # or fabricate a level when an actual resource/state is absent.
    c.branch('e9', FINISH_ROW)
    return c.finish()


def build(data):
    if len(data) != INPUT_SIZE or pe.sha(data) != INPUT_SHA256:
        raise ValueError('exact published V192 input required')
    opt, sections = pe.headers(data)
    coff = opt - 24
    count = struct.unpack_from('<H', data, coff+6)[0]
    table = opt + struct.unpack_from('<H', data, coff+20)[0]
    header = table + count*40
    if header+40 > struct.unpack_from('<I', data, opt+60)[0] or any(data[header:header+40]):
        raise ValueError('no empty section header')
    if struct.unpack_from('<II', data, opt+32) != (4096,4096) or any(struct.unpack_from('<II', data, opt+128)):
        raise ValueError('unexpected alignment or certificate')
    image_size = struct.unpack_from('<I', data, opt+56)[0]
    end = max(struct.unpack_from('<I', data, table+i*40+12)[0] + max(struct.unpack_from('<I', data, table+i*40+8)[0],struct.unpack_from('<I', data, table+i*40+16)[0]) for i in range(count))
    if image_size != (end+4095)&~4095 or len(data)%4096 or max(raw+size for _,size,raw in sections) != len(data):
        raise ValueError('unexpected image end or overlay')
    if len(ORIGINAL) != CONTINUE-SITE:
        raise AssertionError('instruction span mismatch')
    at = pe.offset(sections,SITE,len(ORIGINAL))
    if data[at:at+len(ORIGINAL)] != ORIGINAL:
        raise ValueError('profile renderer fingerprint changed')
    base = pe.IMAGE_BASE+image_size
    code = helper(base)
    if len(code)>4096: raise ValueError('helper exceeds section')
    out = bytearray(data)+code+bytes(4096-len(code))
    out[at:at+len(ORIGINAL)] = b'\xe9'+struct.pack('<i',base-SITE-5)+b'\x90'*(len(ORIGINAL)-5)
    struct.pack_into('<8sIIIIIIHHI',out,header,b'.prender',len(code),image_size,4096,len(data),0,0,0,0,0x60000020)
    struct.pack_into('<H',out,coff+6,count+1)
    struct.pack_into('<I',out,opt+4,struct.unpack_from('<I',data,opt+4)[0]+4096)
    struct.pack_into('<I',out,opt+56,image_size+4096)
    struct.pack_into('<I',out,opt+64,pe.checksum(out,opt+64))
    ranges=sorted([(coff+6,coff+8),(opt+4,opt+8),(opt+56,opt+60),(opt+64,opt+68),(header,header+40),(at,at+len(ORIGINAL))])
    previous=0
    for start,stop in ranges:
        if previous>start or out[previous:start]!=data[previous:start]:raise AssertionError('undeclared or overlapping edit')
        previous=stop
    if out[previous:len(data)]!=data[previous:]:raise AssertionError('undeclared tail edit')
    return bytes(out),dict(inputSha256=INPUT_SHA256,outputSha256=pe.sha(out),size=len(out),helperVA=hex(base),helperSize=len(code),changedRanges=ranges,appendRange=[len(data),len(out)],noticeBytesPreserved=True,gameAccepted=False)


def write_candidate(source,destination):
    source,destination=Path(source).resolve(),Path(destination).resolve()
    if source==destination or destination.exists():raise ValueError('separate new output required')
    result,receipt=build(source.read_bytes())
    destination.parent.mkdir(parents=True,exist_ok=True)
    with destination.open('xb') as f:f.write(result)
    if pe.sha(source.read_bytes())!=INPUT_SHA256 or pe.sha(destination.read_bytes())!=receipt['outputSha256']:raise RuntimeError('readback differs')
    return receipt


if __name__=='__main__':
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--input',required=True,type=Path);p.add_argument('--output',required=True,type=Path)
    a=p.parse_args();print(json.dumps(write_candidate(a.input,a.output),indent=2))
