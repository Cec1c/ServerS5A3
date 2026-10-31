"""Exact-image client candidate: Shift+right-click adds an emblem through native validation and insertion.

The original drag path, grade/cap checks and explicit merge submission remain.
This tool does not start the game, deploy files or modify player assets.
"""
from pathlib import Path
import argparse, json, struct, sys
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import patch_receivers_v128 as pe
OFFICIAL_SHA='54303584115679b9e6964c8c43c77ddf4005eb5fd2548746c6eb95a0ba23f65d'
OFFICIAL_SIZE=112123904
CLICK=0x24a6b0a
TEXTS=(0x29b17e9,0x29b1854,0x29b1875)
def require(ok,message):
 if not ok:raise ValueError(message)
class Code:
 def __init__(self,base):self.base=base;self.data=bytearray();self.labels={};self.fix=[]
 def emit(self,s):self.data.extend(bytes.fromhex(s) if isinstance(s,str) else s)
 def label(self,n):self.labels[n]=self.base+len(self.data)
 def jump(self,op,t):self.emit(op);self.fix.append((len(self.data),t));self.emit(bytes(4))
 def done(self):
  for at,t in self.fix:struct.pack_into('<i',self.data,at,(self.labels[t] if isinstance(t,str) else t)-self.base-at-4)
  return bytes(self.data)
def click(va):
 # This call site is the inventory right-use event, not its left drag/link event.
 # ECX is the native slot view. Preserve caller state; fall back exactly once.
 c=Code(va);c.emit('9c60 8bf1 85f6');c.jump('0f84','fallback')
 c.emit('837e7401');c.jump('0f85','fallback');c.emit('837e7001');c.jump('0f85','fallback')
 c.emit('8b0d98376a05 85c9');c.jump('0f84','fallback')
 c.jump('e8',0x32b42b0);c.emit('85c0');c.jump('0f85','fallback')
 c.emit('8b0d98376a05 68db010000');c.jump('e8',0x32b54b0);c.emit('84c0');c.jump('0f84','fallback')
 c.emit('8b0d98376a05 6a00 68db010000');c.jump('e8',0x32b5c90);c.emit('85c0');c.jump('0f84','fallback');c.emit('813884019f04');c.jump('0f85','fallback');c.emit('8be8')
 c.emit('6a10 ff1548855804 66a90080');c.jump('0f84','fallback')
 c.emit('a188887b05 85c0');c.jump('0f84','fallback');c.emit('80785800');c.jump('0f85','end')
 c.emit('8b9ee8000000 85db');c.jump('0f88','fallback');c.emit('035e28');c.jump('0f88','fallback')
 c.emit('6a00');c.jump('e8',0x2531a80);c.emit('83c404 85c0');c.jump('0f84','fallback');c.emit('3b4624');c.jump('0f85','fallback')
 c.emit('53 8bc8');c.jump('e8',0x25eb680);c.emit('85c0');c.jump('0f84','fallback');c.emit('3b8694000000');c.jump('0f85','fallback')
 # Same action 0x24 validator as native drag; invalid/non-emblem stays native.
 c.emit('83ec04 8bd4 c702ffffffff 6a00 6a00 52 6a00 50 6a24 8b0d98376a05');c.jump('e8',0x32c9fc0);c.emit('83c404 83f802');c.jump('0f85','fallback')
 c.emit('33ff');c.label('slot');c.emit('57');c.jump('e8',0x29b08a0);c.emit('83c404 85c0');c.jump('0f84','add');c.emit('47 83ff05');c.jump('0f8c','slot');c.jump('e9','end')
 c.label('add');c.emit('53');c.jump('e8',0x29af3f0);c.emit('8bf0 83fe04');c.jump('0f85','maximum');c.emit('83ff02');c.jump('0f8d','end')
 c.label('maximum');c.jump('e8',0x29afdd0);c.emit('85c0');c.jump('0f84','end');c.emit('56 8bc8');c.jump('e8',0x3dd0240);c.emit('3bf8');c.jump('0f8d','end')
 # Original insert retains grade checks, source-count bookkeeping and filtering.
 c.emit('53 57');c.jump('e8',0x29b0e60);c.emit('83c408 8bcd');c.jump('e8',0x29b2020)
 c.label('end');c.emit('8b442418 c680f200000001 619dc3')
 c.label('fallback');c.emit('619d');c.jump('e9',0x249c1e0)
 return c.done()
def text(va,empty,limit):
 c=Code(va);c.emit('817c24049b810000');c.jump('0f85','empty');c.emit(b'\xb8'+struct.pack('<I',limit)+b'\xc3');c.label('empty');c.emit(b'\xb8'+struct.pack('<I',empty)+b'\xc3');return c.done()

def layout(va):
 c=Code(va);c.emit('9c60 8bf1 813e4cfad404');c.jump('0f85','end')
 c.emit('837e6408');c.jump('0f85','resize');c.emit('837e6863');c.jump('0f85','resize');c.emit('817e6cd6000000');c.jump('0f84','end')
 c.label('resize');c.emit('6a0c 68d6000000 8bce');c.jump('e8',0x38271c0);c.emit('6a63 6a08 8bce');c.jump('e8',0x37ea190)
 c.label('end');c.emit('619d 8b118b4208');c.jump('e9',0x29b172f);return c.done()
def build(data,profile=None):
 profile=dict(profile or dict(inputSha256=OFFICIAL_SHA,inputSize=OFFICIAL_SIZE))
 require(set(profile)<=set(('inputSha256','inputSize','outputSha256','outputSize')) and {'inputSha256','inputSize'}<=profile.keys(),'invalid exact profile')
 require(pe.sha(data)==profile['inputSha256'] and len(data)==profile['inputSize'],'exact input SHA256/size required')
 opt,sections=pe.headers(data);nt=opt-24;table=opt+struct.unpack_from('<H',data,nt+20)[0];header=table+40*len(sections)
 require(not struct.unpack_from('<H',data,opt+70)[0]&0x40,'ASLR unsupported')
 require(not any(data[opt+128:opt+136]),'certificate table unsupported')
 require(struct.unpack_from('<II',data,opt+32)==(4096,4096),'alignment changed')
 require(header+40<=min(raw for _,size,raw in sections if size) and data[header:header+40]==bytes(40),'no empty section header')
 require(len(data)==max(raw+size for _,size,raw in sections),'overlay unsupported')
 require(struct.unpack_from('<I',data,opt+64)[0]==pe.checksum(data,opt+64),'input checksum mismatch')
 require(all(data[table+i*40:table+i*40+8]!=b'.emright' for i in range(len(sections))),'repeated patch rejected')
 require(data[pe.offset(sections,0x249bb22,5):pe.offset(sections,0x249bb22,5)+5]==b'\xe8'+struct.pack('<i',0x32c7e40-0x249bb22-5),'superseded Shift hook present')
 require(data[pe.offset(sections,0x29b33d9,5):pe.offset(sections,0x29b33d9,5)+5]==b'\xe8'+struct.pack('<i',0x3826970-0x29b33d9-5),'confirm callback changed')
 imageSize=struct.unpack_from('<I',data,opt+56)[0]
 require(imageSize%4096==0 and imageSize>=max(start-pe.IMAGE_BASE+size for start,size,raw in sections),'invalid image extent')
 va=pe.IMAGE_BASE+imageSize;payload=bytearray(4096)
 for off,limit,code in ((0,0x400,click(va)),(0x400,0x500,text(va+0x400,va+0x700,va+0x800)),(0x500,0x600,layout(va+0x500))):
  require(off+len(code)<=limit,'helper overlaps next region');payload[off:off+len(code)]=code
 for off,s in ((0x700,'Shift+右键加入，拖动也可'),(0x800,'Shift+右键加入，最多%d个')):
  b=s.encode('utf-16le')+b'\0\0';require(off+len(b)<=off+0x100,'instruction text too long');payload[off:off+len(b)]=b
 out=bytearray(data);ranges=[]
 for at,target,oldtarget in [(CLICK,va,0x249c1e0)]+[(a,va+0x400,0x39a6620) for a in TEXTS]:
  off=pe.offset(sections,at,5);require(data[off:off+5]==b'\xe8'+struct.pack('<i',oldtarget-at-5),'native hook fingerprint changed');out[off:off+5]=b'\xe8'+struct.pack('<i',target-at-5);ranges.append([off,off+5])
 off=pe.offset(sections,0x29b172a,5)
 require(data[off:off+5]==bytes.fromhex('8b118b4208'),'native layout hook changed')
 out[off:off+5]=b'\xe9'+struct.pack('<i',va+0x500-0x29b172a-5);ranges.append([off,off+5])
 out.extend(payload);struct.pack_into('<8sIIIIIIHHI',out,header,b'.emright',0x900,va-pe.IMAGE_BASE,4096,len(data),0,0,0,0,0x60000020)
 struct.pack_into('<H',out,nt+6,len(sections)+1);struct.pack_into('<I',out,opt+4,struct.unpack_from('<I',data,opt+4)[0]+4096);struct.pack_into('<I',out,opt+56,va-pe.IMAGE_BASE+4096);struct.pack_into('<I',out,opt+64,pe.checksum(out,opt+64))
 ranges.extend([[header,header+40],[nt+6,nt+8],[opt+4,opt+8],[opt+56,opt+60],[opt+64,opt+68]]);ranges.sort();cursor=0
 for lo,hi in ranges:require(data[cursor:lo]==out[cursor:lo],'unrelated bytes changed');cursor=hi
 require(data[cursor:]==out[cursor:len(data)],'unrelated tail changed')
 for key,value in [('outputSha256',pe.sha(out)),('outputSize',len(out))]:
  if key in profile:require(profile[key]==value,'pinned output mismatch')
 return bytes(out),dict(inputSha256=pe.sha(data),outputSha256=pe.sha(out),outputSize=len(out),codeVA=va,clickVA=va,textVA=va+0x400,layoutVA=va+0x500,ranges=ranges,gameplayAccepted=False)
def write_candidate(source,target,profile):
 source,target=Path(source).resolve(),Path(target).resolve()
 require(source!=target and not target.exists(),'separate new output required')
 require({'outputSha256','outputSize'}<=profile.keys(),'write requires pinned output')
 data=source.read_bytes();out,proof=build(data,profile)
 with target.open('xb') as f:f.write(out)
 require(source.read_bytes()==data and target.read_bytes()==out,'source/output readback failed')
 return proof
if __name__=='__main__':
 p=argparse.ArgumentParser(description=__doc__);p.add_argument('--input',type=Path,required=True);p.add_argument('--profile',type=Path);p.add_argument('--output',type=Path);a=p.parse_args()
 profile=json.loads(a.profile.read_text()) if a.profile else None
 if a.output:
  require(profile is not None,'write requires explicit pinned profile');proof=write_candidate(a.input,a.output,profile)
 else:_,proof=build(a.input.read_bytes(),profile)
 print(json.dumps(proof,indent=2))
