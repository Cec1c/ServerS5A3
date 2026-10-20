"""Build a class1/op1342 receiver candidate from the exact v150 client."""
from pathlib import Path
import hashlib,struct,json,argparse
import patch_receivers_v128 as pe

INPUT_SHA256='f781c423eaad8ccdb0327a864d525f0ee0436663399eb809a09aa08bf390e61b'
CALL_VA=0x2523a13
PREVIOUS=0x6ea8000
OPCODE=1342
SECTION=b'.aikrx'

def helper(va):
 code=bytearray.fromhex('0fb7442404803f010f85')+bytes(4)
 first=len(code)-4
 code+=b'\x3d'+struct.pack('<I',OPCODE)+bytes.fromhex('0f85')+bytes(4)
 second=len(code)-4
 code+=bytes.fromhex('b801000000c20400')
 for at in [first,second]:struct.pack_into('<i',code,at,PREVIOUS-(va+at+4))
 return bytes(code)

def build(data):
 if hashlib.sha256(data).hexdigest()!=INPUT_SHA256:raise ValueError('exact v150 required')
 opt,sections=pe.headers(data);pe_at=opt-24
 count=struct.unpack_from('<H',data,pe_at+6)[0]
 table=opt+struct.unpack_from('<H',data,pe_at+20)[0];header=table+40*count
 size=len(data);align=struct.unpack_from('<II',data,opt+32)
 if count!=31 or align!=(4096,4096) or size!=111841280 or struct.unpack_from('<I',data,opt+56)[0]!=size:raise ValueError('unexpected layout')
 if header+40>4096 or any(data[header:header+40]):raise ValueError('no clean section header')
 call=pe.offset(sections,CALL_VA,5)
 if data[call:call+5]!=b'\xe8'+struct.pack('<i',PREVIOUS-CALL_VA-5):raise ValueError('dispatcher fingerprint mismatch')
 regva=0x43b5aa0;reg=bytes.fromhex('6a0068a072e600683e050000')
 at=pe.offset(sections,regva,len(reg))
 if data[at:at+len(reg)]!=reg:raise ValueError('1342 callback registration mismatch')
 out=bytearray(data);newva=pe.IMAGE_BASE+size;code=helper(newva)
 out+=code+bytes(4096-len(code))
 struct.pack_into('<i',out,call+1,newva-CALL_VA-5)
 struct.pack_into('<H',out,pe_at+6,count+1)
 struct.pack_into('<I',out,opt+4,struct.unpack_from('<I',data,opt+4)[0]+4096)
 struct.pack_into('<I',out,opt+56,size+4096)
 struct.pack_into('<8sIIIIIIHHI',out,header,SECTION,len(code),size,4096,size,0,0,0,0,0x60000020)
 struct.pack_into('<I',out,opt+64,pe.checksum(out,opt+64))
 ranges=[(call+1,call+5),(pe_at+6,pe_at+8),(opt+4,opt+8),(opt+56,opt+60),(opt+64,opt+68),(header,header+40)]
 assert all(x==y or any(lo<=i<hi for lo,hi in ranges) for i,(x,y) in enumerate(zip(data,out)))
 result=bytes(out)
 return result,dict(input_sha256=INPUT_SHA256,output_sha256=hashlib.sha256(result).hexdigest(),section_va=hex(newva),helper_bytes=len(code),added_admissions=[dict(packet_class=1,opcode=1342)],fallback=hex(PREVIOUS),checksum=struct.unpack_from('<I',out,opt+64)[0],published=False,deployed=False,full_gui=False)

if __name__=='__main__':
 p=argparse.ArgumentParser();p.add_argument('--input',type=Path,required=True);p.add_argument('--output',type=Path,required=True);p.add_argument('--receipt',type=Path,required=True);a=p.parse_args()
 if len({x.resolve() for x in [a.input,a.output,a.receipt]})!=3 or a.output.exists() or a.receipt.exists():p.error('immutable input and new outputs required')
 result,r=build(a.input.read_bytes());a.output.parent.mkdir(parents=True,exist_ok=True);a.output.write_bytes(result);a.receipt.write_text(json.dumps(r,indent=2),'utf-8');print(json.dumps(r))
