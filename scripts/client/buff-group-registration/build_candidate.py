"""Build a paired local-test client, preserving every byte outside pinned hooks/padding."""
from pathlib import Path
import struct,hashlib,json,importlib.util
B=Path(__file__).parent
SCRIPTS=B.parent
s=importlib.util.spec_from_file_location('p',SCRIPTS/'quiet-profile-refresh/build_candidate.py');p=importlib.util.module_from_spec(s);s.loader.exec_module(p)
BASE='9276348410bbf955f41a4d9e407f5288db883ff6edd68e66f30c83f56dfe3250'
class Asm:
 def __init__(self,base):self.base=base;self.b=bytearray();self.labels={};self.fix=[]
 def raw(self,x):self.b.extend(bytes.fromhex(x))
 def call(self,va):self.b.extend(b'\xe8'+struct.pack('<i',va-(self.base+len(self.b)+5)))
 def jmp(self,va):self.b.extend(b'\xe9'+struct.pack('<i',va-(self.base+len(self.b)+5)))
 def push(self,v):self.b.extend(b'\x68'+struct.pack('<I',v))
 def label(self,n):self.labels[n]=len(self.b)
 def branch(self,op,n):self.b.extend(bytes.fromhex(op));at=len(self.b);self.b.extend(bytes(4));self.fix.append((at,n))
 def finish(self):
  for at,n in self.fix:struct.pack_into('<i',self.b,at,self.labels[n]-at-4)
  return bytes(self.b)
def payload(base):
 sender=Asm(base)
 sender.raw('55 8bec')
 def write(value,fn):
  if isinstance(value,int):sender.push(value)
  else:sender.raw(value)
  sender.call(0x374ea10);sender.raw('8bc8');sender.call(fn)
 write(1426,0x374f280);write(0xb7,0x374f390);write('ff7508',0x374f390)
 write('ff750c',0x374f3c0);write('ff7510',0x374f390);write('ff7514',0x374f390);write('ff7518',0x374f3c0)
 sender.call(0x3750ac0);sender.raw('5d c21800')
 headerbase=base+0x100;h=Asm(headerbase)
 # Forward the original read(out,1), leaving the caller's cdecl args intact.
 h.raw('ff742408 ff742408');h.call(0x37e82e0);h.raw('83c408 60 c745cc00000000')
 h.raw('807de8fe');h.branch('0f85','done')
 # Header FE,group:u8,skill:u16 is followed by the unmodified row body.
 h.raw('6a01 8d45cc 50');h.call(0x37e82e0);h.raw('83c408')
 h.raw('8d45c8 50');h.call(0x37e8350);h.raw('83c404')
 h.raw('6a01 8d45e8 50');h.call(0x37e82e0);h.raw('83c408')
 h.raw('837dcc01');h.branch('0f87','reject')
 h.raw('807de821');h.branch('0f87','reject')
 h.raw('8b0d086c6505 85c9');h.branch('0f85','manager_ready')
 h.push(0x7b4);h.call(0x8a9480);h.raw('83c404 85c0');h.branch('0f84','reject')
 h.raw('8bc8');h.call(0x9981f0);h.raw('a3086c6505')
 h.label('manager_ready')
 h.raw('a1b06f7b05 85c0');h.branch('0f84','reject')
 h.raw('50 8bd4 52 8b0d086c6505');h.call(0x997a10);h.raw('83c404')
 h.raw('8b4dcc 0fb755c8 891488');h.branch('e9','done')
 h.label('reject');h.raw('c645e800')
 h.label('done');h.raw('61 c3')
 rowsbase=base+0x200;r=Asm(rowsbase)
 r.raw('ff75cc 660fd64008');r.jmp(0x9987e3)
 return [(base,sender.finish()),(headerbase,h.finish()),(rowsbase,r.finish())]
def build(raw):
 assert hashlib.sha256(raw).hexdigest()==BASE
 pe=p.PE(raw);pe.require_contiguous()
 # Select a single verified zero-filled RX tail large enough for all helpers.
 candidates=[]
 for s in pe.sections:
  start=p.align(s['virtual_size'],16)
  if s['characteristics']==0x60000020 and s['raw_size']-start>=0x400:
   pos=s['raw']+start
   if raw[pos:pos+0x400]==bytes(0x400):candidates.append((s,start))
 assert candidates,'No unused RX padding'
 section,delta=candidates[-1];base=pe.base+section['rva']+delta
 chunks=payload(base);out=bytearray(raw);ranges=[];hooks=[]
 def patch(va,expected,value):
  assert pe.read(va,len(expected))==expected,(hex(va),pe.read(va,len(expected)).hex())
  at=pe.offset(va,len(expected));out[at:at+len(expected)]=value;ranges.append((at,at+len(expected)));hooks.append(dict(va=hex(va),before=expected.hex(),after=value.hex()))
 patch(0x994d20,bytes.fromhex('558bec6892050000'),b'\xe9'+struct.pack('<i',base-0x994d20-5)+b'\x90'*3)
 patch(0x998601,bytes.fromhex('83ec24'),bytes.fromhex('83ec2c'))
 patch(0x99861e,bytes.fromhex('e8bdfce402'),b'\xe8'+struct.pack('<i',base+0x100-0x99861e-5))
 # push esi; movq [eax+8],xmm0. group now comes from this packet's stack local.
 expected=pe.read(0x9987dd,6);assert expected==bytes.fromhex('56660fd64008'),expected.hex()
 patch(0x9987dd,expected,b'\xe9'+struct.pack('<i',base+0x200-0x9987dd-5)+b'\x90')
 for va,code in chunks:
  at=pe.offset(va,len(code));assert raw[at:at+len(code)]==bytes(len(code));out[at:at+len(code)]=code;ranges.append((at,at+len(code)))
 header=pe.st+section['index']*40+8;end=delta+max(va-base+len(c) for va,c in chunks)
 struct.pack_into('<I',out,header,end);ranges.append((header,header+4))
 struct.pack_into('<I',out,pe.opt+64,p.pe_checksum(out,pe.opt+64));ranges.append((pe.opt+64,pe.opt+68))
 restored=bytearray(out)
 for a,b in ranges:restored[a:b]=raw[a:b]
 assert restored==raw
 final=bytes(out);p.PE(final).require_contiguous()
 return final,dict(inputSHA256=BASE,outputSHA256=hashlib.sha256(final).hexdigest(),base=base,section=section['name'],hooks=hooks,ranges=ranges,chunks=[dict(va=va,hex=c.hex()) for va,c in chunks],installed=False,localTestOnly=True)
if __name__=='__main__':
 import argparse
 cli=argparse.ArgumentParser(description=__doc__)
 cli.add_argument('--input',type=Path,required=True)
 cli.add_argument('--output-dir',type=Path,required=True)
 args=cli.parse_args()
 out,receipt=build(args.input.read_bytes())
 assert receipt['outputSHA256']=='0618b0db618224079a3d28fadff448b7c35cc463788554810861e6781be3264f'
 D=args.output_dir;D.mkdir(parents=True,exist_ok=True)
 (D/'DNF.exe').write_bytes(out);(D/'build.json').write_text(json.dumps(receipt,indent=2),'utf8')
 print(json.dumps({k:receipt[k] for k in ['outputSHA256','base','section','installed']}))
