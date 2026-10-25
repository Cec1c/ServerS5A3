"""Restore merchant upgrade responses and initialize the native request prefix."""
import argparse
import json
from pathlib import Path
import struct

import patch_receivers_v128 as pe
from patch_at5t_input_priority import Code
import patch_guild_hongbao_v173 as hongbao

INPUT_SHA256 = '27c2241aae6d7b8c6b043a2bbe9e2eccfdb18d6969912055694195b972fcad67'
INPUT_SIZE = 111919104
CALL_VA, PREVIOUS_GATE = 0x2523A13, 0x6EAD600
GATE_VA, PREFIX_VA = 0x6EAD700, 0x6EAD780
SENDER_VA = 0x1193D75
SENDER_BYTES = bytes.fromhex('8d55d4 6a19')


def gate():
    c = Code(GATE_VA)
    c.emit('803f01')
    c.branch('0f85', 'notice')
    c.emit('0fb7442404 3d48030000')
    c.branch('0f84', 'allow')
    c.branch('e9', 'fallback')
    c.label('notice')
    c.emit('803f00')
    c.branch('0f85', 'fallback')
    c.emit('0fb7442404 3d0a030000')
    c.branch('0f85', 'fallback')
    c.label('allow')
    c.emit('b801000000 c20400')
    c.label('fallback')
    c.branch('e9', PREVIOUS_GATE)
    return c.finish()


def prefix():
    c = Code(PREFIX_VA)
    # Only the unused 13-byte prefix; the three initialized u32 fields follow.
    c.emit('9c 50 31c0 8945d4 8945d8 8945dc 8845e0 58 9d')
    c.data.extend(SENDER_BYTES)
    c.branch('e9', SENDER_VA + len(SENDER_BYTES))
    return c.finish()


def build(data):
    if len(data) != INPUT_SIZE or pe.sha(data) != INPUT_SHA256:
        raise ValueError('exact cumulative V174 input required')
    opt, sections = pe.headers(data)
    table = opt + struct.unpack_from('<H', data, opt - 4)[0]
    headers = [table+i*40 for i in range(len(sections)) if data[table+i*40:table+i*40+8] == b'.r195rx\0']
    if len(headers) != 1:
        raise ValueError('receive section missing')
    h = headers[0]
    vsize, rva, rawsize, raw = struct.unpack_from('<IIII', data, h+8)
    if (vsize != 0x600+len(hongbao.helper()) or rva+pe.IMAGE_BASE != 0x6EAD000 or rawsize != 4096
            or struct.unpack_from('<I',data,h+36)[0] != 0x60000020 or any(data[raw+vsize:raw+rawsize])
            or any(struct.unpack_from('<II',data,opt+128))):
        raise ValueError('receive section/free range contract changed')
    call = pe.offset(sections,CALL_VA,5)
    sender = pe.offset(sections,SENDER_VA,5)
    if data[call:call+5] != b'\xe8'+struct.pack('<i',PREVIOUS_GATE-CALL_VA-5):
        raise ValueError('receive chain changed')
    if data[sender:sender+5] != SENDER_BYTES:
        raise ValueError('sender fingerprint changed')
    out = bytearray(data)
    ranges = [(call,call+5),(sender,sender+5),(h+8,h+12),(opt+64,opt+68)]
    for va, code in ((GATE_VA,gate()),(PREFIX_VA,prefix())):
        at = pe.offset(sections,va,len(code))
        out[at:at+len(code)] = code
        ranges.append((at,at+len(code)))
    out[call:call+5] = b'\xe8'+struct.pack('<i',GATE_VA-CALL_VA-5)
    out[sender:sender+5] = b'\xe9'+struct.pack('<i',PREFIX_VA-SENDER_VA-5)
    struct.pack_into('<I',out,h+8,PREFIX_VA+len(prefix())-pe.IMAGE_BASE-rva)
    struct.pack_into('<I',out,opt+64,pe.checksum(out,opt+64))
    return bytes(out), dict(inputSha256=INPUT_SHA256,outputSha256=pe.sha(out),size=len(out),
        added=[dict(packetClass=1,opcode=840),dict(packetClass=0,opcode=778)],
        changedRanges=sorted(ranges),gateVA=hex(GATE_VA),prefixVA=hex(PREFIX_VA),
        fallbackVA=hex(PREVIOUS_GATE),nativeReceiversChanged=False)


def write_candidate(source,destination):
    source,destination = Path(source).resolve(),Path(destination).resolve()
    if source==destination or destination.exists():
        raise ValueError('separate new output required')
    out,receipt=build(source.read_bytes())
    destination.parent.mkdir(parents=True,exist_ok=True)
    with destination.open('xb') as stream: stream.write(out)
    if pe.sha(source.read_bytes())!=INPUT_SHA256 or pe.sha(destination.read_bytes())!=receipt['outputSha256']:
        raise RuntimeError('file readback mismatch')
    return receipt


if __name__=='__main__':
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input',required=True,type=Path)
    parser.add_argument('--output',required=True,type=Path)
    args=parser.parse_args()
    print(json.dumps(write_candidate(args.input,args.output),indent=2))
