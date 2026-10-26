"""Local candidate: derive monster barrier argument from current symbol111.

Exact v152 input only. Never edits the input or an installed client. No event
replay or controller mutation: existing AC is fallback if no manager exists.
"""
from pathlib import Path
import sys,struct,json
sys.dont_write_bytecode=True
import argparse
import pe_support as pe
HASH='44aecee7c48bc1c89582ef1cd95694950569951caeef3b22ec3478c917b8ce7e'
SIZE=111849472
SITE=0x1872744
ORIGINAL=bytes.fromhex('0fb68bac000000')
NEXT=SITE+len(ORIGINAL)
GETTER=0x1a57b40

def helper(va):
    code=bytearray(ORIGINAL)
    code.extend(bytes.fromhex('9c60 8b0d78376a05 85c9 0f84'))
    null=len(code);code.extend(bytes(4))
    code.extend(bytes.fromhex('6a6f e8'))
    code.extend(struct.pack('<i',GETTER-(va+len(code)+4)))
    code.extend(bytes.fromhex('83f801 0f87'))
    other=len(code);code.extend(bytes(4))
    code.extend(bytes.fromhex('83f001 89442418'))
    # saved ECX at pushad+24 receives 1-symbol111; no object field changes.
    done=len(code)
    code.extend(bytes.fromhex('619d e9'))
    code.extend(struct.pack('<i',NEXT-(va+len(code)+4)))
    for jump in [null,other]:struct.pack_into('<i',code,jump,done-jump-4)
    return bytes(code)

def build(data):
    if len(data)!=SIZE or pe.sha(data)!=HASH:raise ValueError('exact published v152 required')
    opt,sections=pe.headers(data);header=opt+struct.unpack_from('<H',data,opt-4)[0]+len(sections)*40
    assert len(sections)==33 and header+40<=4096 and not any(data[header:header+40])
    assert struct.unpack_from('<II',data,opt+32)==(4096,4096)
    assert struct.unpack_from('<I',data,opt+56)[0]==SIZE
    assert not any(struct.unpack_from('<II',data,opt+128))
    site=pe.offset(sections,SITE,len(ORIGINAL))
    assert data[site:site+len(ORIGINAL)]==ORIGINAL
    va=pe.IMAGE_BASE+SIZE;code=helper(va)
    out=bytearray(data);out.extend(code+bytes(4096-len(code)))
    out[site:site+len(ORIGINAL)]=b'\xe9'+struct.pack('<i',va-SITE-5)+b'\x90'*2
    struct.pack_into('<H',out,opt-18,len(sections)+1)
    struct.pack_into('<I',out,opt+4,struct.unpack_from('<I',data,opt+4)[0]+4096)
    struct.pack_into('<I',out,opt+56,SIZE+4096)
    struct.pack_into('<8sIIIIIIHHI',out,header,b'.antbar',len(code),SIZE,4096,SIZE,0,0,0,0,0x60000020)
    struct.pack_into('<I',out,opt+64,pe.checksum(out,opt+64))
    ranges=sorted([(site,site+len(ORIGINAL)),(opt-18,opt-16),(opt+4,opt+8),(opt+56,opt+60),(opt+64,opt+68),(header,header+40)])
    cursor=0
    for lo,hi in ranges:
        assert data[cursor:lo]==out[cursor:lo]
        cursor=hi
    assert data[cursor:]==out[cursor:SIZE]
    receipt=dict(input_sha256=HASH,output_sha256=pe.sha(out),size=len(out),site=hex(SITE),helper_va=hex(va),helper_size=len(code),allowed_original_ranges=ranges,published=False,deployed=False,native_gui_accepted=False)
    return bytes(out),receipt

if __name__=='__main__':
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input',type=Path,required=True)
    parser.add_argument('--output',type=Path,required=True)
    parser.add_argument('--receipt',type=Path,required=True)
    args=parser.parse_args()
    paths=[args.input.resolve(),args.output.resolve(),args.receipt.resolve()]
    if len(set(paths))!=3 or args.output.exists() or args.receipt.exists():
        parser.error('immutable input and distinct new outputs required')
    out,receipt=build(args.input.read_bytes())
    with args.output.open('xb') as f:f.write(out)
    with args.receipt.open('x',encoding='utf8') as f:json.dump(receipt,f,indent=2)
    print(json.dumps(receipt))
