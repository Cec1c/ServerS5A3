"""Extend the cumulative client with paired op39 absolute-count handling."""
from pathlib import Path
import argparse,hashlib,json,struct

INPUT_SHA256='21a9ed0b0c86e8e3a95f7b6c84270025c91219d3df3e9143bdd6629264c9166e'
HOOK=0x202ec89
ORIGINAL=bytes.fromhex('8b8dd8fdffff')

def build(data):
 assert hashlib.sha256(data).hexdigest()==INPUT_SHA256,'unexpected cumulative client'
 out=bytearray(data);pe=struct.unpack_from('<I',data,0x3c)[0];opt=pe+24
 count=struct.unpack_from('<H',data,pe+6)[0];table=opt+struct.unpack_from('<H',data,pe+20)[0]
 base=struct.unpack_from('<I',data,opt+28)[0];section_align,file_align=struct.unpack_from('<II',data,opt+32)
 sections=[]
 for i in range(count):
  h=table+i*40;vsize,rva,size,raw=struct.unpack_from('<IIII',data,h+8);sections.append((h,vsize,rva,size,raw))
 def offset(va):
  for _,_,rva,size,raw in sections:
   if 0<=va-base-rva<size:return raw+va-base-rva
  raise ValueError(hex(va))
 h,vsize,rva,size,raw=sections[-1]
 assert raw+size==len(data) and size>=vsize and struct.unpack_from('<I',data,h+36)[0]&0x20000000
 assert all(rva>=x[2] for x in sections)
 assert data[offset(HOOK):offset(HOOK)+6]==ORIGINAL
 # All section-header space is occupied. Grow only the final executable
 # section, leaving every existing RVA, raw offset, resource and export intact.
 extension=(max(vsize,size)+section_align-1)//section_align*section_align
 cave=base+rva+extension
 code=bytearray(bytes.fromhex('f685e3fdffff80')) # test op39 tail byte, 0x80
 code+=b'\x0f\x85'+struct.pack('<i',0x202ec99-(cave+len(code)+6))
 code+=ORIGINAL
 code+=b'\xe9'+struct.pack('<i',0x202ec8f-(cave+len(code)+5))
 used=extension+len(code);newsize=(used+file_align-1)//file_align*file_align
 out.extend(bytes(newsize-size));out[raw+extension:raw+extension+len(code)]=code
 struct.pack_into('<I',out,h+8,used);struct.pack_into('<I',out,h+16,newsize)
 struct.pack_into('<I',out,opt+56,(rva+used+section_align-1)//section_align*section_align)
 struct.pack_into('<I',out,opt+4,struct.unpack_from('<I',out,opt+4)[0]+newsize-size)
 out[offset(HOOK):offset(HOOK)+6]=b'\xe9'+struct.pack('<i',cave-HOOK-5)+b'\x90'
 check=opt+64;struct.pack_into('<I',out,check,0)
 total=sum(memoryview(out)[::2])+(sum(memoryview(out)[1::2])<<8)
 while total>>16:total=(total&0xffff)+(total>>16)
 struct.pack_into('<I',out,check,total+len(out))
 return out,{'sourceSHA256':INPUT_SHA256,'candidateSHA256':hashlib.sha256(out).hexdigest(),'hookVA':hex(HOOK),'extensionVA':hex(cave),'marker':128,'preservedFiendwarAndMileage':True,'gameplayVerified':False}

if __name__=='__main__':
 p=argparse.ArgumentParser();p.add_argument('source',type=Path);p.add_argument('output',type=Path);a=p.parse_args()
 data,receipt=build(a.source.read_bytes())
 with a.output.open('xb') as f:f.write(data)
 a.output.with_suffix('.json').write_text(json.dumps(receipt,indent=2)+'\n')
 print(json.dumps(receipt))
