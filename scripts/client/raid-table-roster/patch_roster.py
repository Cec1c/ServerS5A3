"""Remove the local scene-actor prerequisite from two raid-table group badges."""
from pathlib import Path
import argparse,json,struct,sys

sys.dont_write_bytecode=True
sys.path.insert(0,str(Path(__file__).resolve().parent.parent/'raid-partyface'))
import pe_support as pe

INPUT_SHA='8e92c29cbbbcc2e643a87cb73e199ec52e9b3beeb4146b3ec16d62404d36064c'
INPUT_SIZE=111861760

def identity_code(address,slot,empty,proceed,register,length):
    # Store the existing NOTI584 participant key in the old temporary actor
    # slot. The following native NOTI591 roster search already owns the badge.
    code=bytearray.fromhex('0fb700' if register=='eax' else '0fb730')
    code.extend(bytes.fromhex('8985' if register=='eax' else '89b5')+struct.pack('<i',slot))
    code.extend(bytes.fromhex('85c0' if register=='eax' else '85f6'))
    short=empty-address-len(code)-2
    if -128<=short<=127:
        code.extend(b'\x74'+struct.pack('<b',short))
    else:
        code.extend(b'\x0f\x84'+struct.pack('<i',empty-address-len(code)-6))
    code.extend(b'\xe9'+struct.pack('<i',proceed-address-len(code)-5))
    assert len(code)<=length
    return bytes(code)+b'\x90'*(length-len(code))

def build(data):
    if len(data)!=INPUT_SIZE or pe.sha(data)!=INPUT_SHA:
        raise ValueError('exact verified R195 seven-pair candidate required')
    opt,sections=pe.headers(data)
    edits=[
        (0x110c254,identity_code(0x110c254,-0x94,0x110c269,0x110c2b1,'eax',21)),
        (0x110c343,bytes.fromhex('8b856cffffff')+b'\x90'*5),
        (0x1875359,identity_code(0x1875359,-0xa8,0x18754e0,0x1875383,'esi',42)),
        (0x1875415,bytes.fromhex('8b8558ffffff')+b'\x90'*5),
    ]
    out=bytearray(data)
    expected=['8b0050e8d414860183c40489856cffffff85c07548','8b8d6cffffffe892518501',
              '8b3056e8cf830f0183c40485c00f847401000056e8be830f0183c404898558ffffff85c00f845d010000','8b8d58ffffffe8c0c00e01']
    ranges=[]
    details=[]
    for (va,replacement),original in zip(edits,expected):
        offset=pe.offset(sections,va,len(replacement))
        if data[offset:offset+len(replacement)]!=bytes.fromhex(original):raise ValueError('instruction boundary mismatch')
        details.append(dict(va=hex(va),before=data[offset:offset+len(replacement)].hex(),after=replacement.hex()))
        out[offset:offset+len(replacement)]=replacement
        ranges.append((offset,offset+len(replacement)))
    struct.pack_into('<I',out,opt+64,pe.checksum(out,opt+64))
    ranges.append((opt+64,opt+68));ranges.sort()
    cursor=0
    for lo,hi in ranges:
        if data[cursor:lo]!=out[cursor:lo]:raise ValueError('unexpected change')
        cursor=hi
    if data[cursor:]!=out[cursor:]:raise ValueError('unexpected trailing change')
    return bytes(out),dict(input_sha256=INPUT_SHA,output_sha256=pe.sha(out),size=len(out),edits=details,edit_ranges=ranges,published=False,deployed=False,gui_accepted=False)

if __name__=='__main__':
    p=argparse.ArgumentParser()
    for arg in ('input','output','receipt'):p.add_argument('--'+arg,type=Path,required=True)
    args=p.parse_args()
    if args.output.exists() or args.receipt.exists():raise ValueError('new output paths required')
    result,receipt=build(args.input.read_bytes())
    with args.output.open('xb') as f:f.write(result)
    with args.receipt.open('x',encoding='utf8') as f:json.dump(receipt,f,indent=2)
    print(json.dumps(receipt))
