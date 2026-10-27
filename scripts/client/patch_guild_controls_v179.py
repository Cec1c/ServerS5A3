"""Enable guild placement move/remove acknowledgements on official V179."""
import argparse
import json
import struct
from pathlib import Path

import patch_receivers_v128 as pe
from patch_at5t_input_priority import Code

INPUT_SHA256 = '72254abd5214bc24f8bcb76a16184e07105dde33f4e762b4c71c2e264bc7789c'
INPUT_SIZE = 112005120
CALL_VA, PREVIOUS_GATE = 0x2523A13, 0x6EAD900
GATE_VA = 0x6EADA00
SENDERS = ((0x1193F21, 0x6EADA80, bytes.fromhex('8d4dcc6a2151')),
           (0x1079B75, 0x6EADB00, bytes.fromhex('8d45cc6a2150')))

def gate():
    c = Code(GATE_VA)
    c.emit('803f01')
    c.branch('0f85', 'fallback')
    c.emit('0fb7442404 3d46030000')
    c.branch('0f84', 'allow')
    c.emit('3d47030000')
    c.branch('0f85', 'fallback')
    c.label('allow')
    c.emit('b801000000 c20400')
    c.label('fallback')
    c.branch('e9', PREVIOUS_GATE)
    return c.finish()

def prefix(sender, destination, original):
    c = Code(destination)
    c.emit('9c 50 31c0 8945cc 8945d0 8945d4 8845d8 58 9d')
    c.data.extend(original)
    c.branch('e9', sender+len(original))
    return c.finish()

def build(data):
    if len(data) != INPUT_SIZE or pe.sha(data) != INPUT_SHA256:
        raise ValueError('exact official V179 input required')
    opt, sections = pe.headers(data)
    table = opt+struct.unpack_from('<H',data,opt-4)[0]
    headers = [table+i*40 for i in range(len(sections)) if data[table+i*40:table+i*40+8] == b'.r195rx\0']
    if len(headers) != 1: raise ValueError('receive section missing')
    h = headers[0]
    vsize,rva,rawsize,raw = struct.unpack_from('<IIII',data,h+8)
    if (vsize != 0x99d or rva+pe.IMAGE_BASE != 0x6EAD000 or rawsize != 4096
        or struct.unpack_from('<I',data,h+36)[0] != 0x60000020
        or any(data[raw+vsize:raw+rawsize]) or any(struct.unpack_from('<II',data,opt+128))):
        raise ValueError('receive section/free range contract changed')
    call = pe.offset(sections,CALL_VA,5)
    if data[call:call+5] != b'\xe8'+struct.pack('<i',PREVIOUS_GATE-CALL_VA-5):
        raise ValueError('receive chain changed')
    out = bytearray(data)
    ranges = [(call,call+5),(h+8,h+12),(opt+64,opt+68)]
    blocks = [(GATE_VA,gate())]
    for sender,destination,original in SENDERS:
        at = pe.offset(sections,sender,len(original))
        if data[at:at+len(original)] != original: raise ValueError('sender changed')
        out[at:at+len(original)] = b'\xe9'+struct.pack('<i',destination-sender-5)+b'\x90'
        ranges.append((at,at+len(original)))
        blocks.append((destination,prefix(sender,destination,original)))
    for va,code in blocks:
        at = pe.offset(sections,va,len(code))
        out[at:at+len(code)] = code
        ranges.append((at,at+len(code)))
    out[call:call+5] = b'\xe8'+struct.pack('<i',GATE_VA-CALL_VA-5)
    struct.pack_into('<I',out,h+8,max(va+len(code) for va,code in blocks)-pe.IMAGE_BASE-rva)
    struct.pack_into('<I',out,opt+64,pe.checksum(out,opt+64))
    return bytes(out),dict(inputSha256=INPUT_SHA256,outputSha256=pe.sha(out),size=len(out),
        added=[dict(packetClass=1,opcode=x) for x in (838,839)],changedRanges=sorted(ranges),
        gateVA=hex(GATE_VA),fallbackVA=hex(PREVIOUS_GATE),nativeReceiversChanged=False)

def write_candidate(source,destination):
    source,destination = Path(source).resolve(),Path(destination).resolve()
    if source == destination or destination.exists(): raise ValueError('separate new output required')
    out,receipt = build(source.read_bytes())
    destination.parent.mkdir(parents=True,exist_ok=True)
    with destination.open('xb') as stream: stream.write(out)
    if pe.sha(source.read_bytes()) != INPUT_SHA256 or pe.sha(destination.read_bytes()) != receipt['outputSha256']:
        raise RuntimeError('file readback mismatch')
    return receipt

if __name__ == '__main__':
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--input',required=True,type=Path)
    p.add_argument('--output',required=True,type=Path)
    a=p.parse_args()
    print(json.dumps(write_candidate(a.input,a.output),indent=2))
