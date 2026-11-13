"""Exact-image additive build. This does not install or execute the game."""
from pathlib import Path
import sys,struct,hashlib,json

import pefile
S=Path(__file__).parent
import argparse
parser=argparse.ArgumentParser()
parser.add_argument('--base',type=Path,required=True)
parser.add_argument('--output',type=Path,required=True)
args=parser.parse_args()
BASE_SHA='a28601e349a65a1ec0356995b693a326620d81887031c17b76b5b73daaeb5216'
def sha(b):return hashlib.sha256(b).hexdigest()
def align(x):return (x+4095)//4096*4096
def build():
 base=args.base.read_bytes();assert sha(base)==BASE_SHA
 libbytes=(S/'build/client.dll').read_bytes();lib=pefile.PE(data=libbytes);old=pefile.PE(data=base)
 assert not getattr(lib,'DIRECTORY_ENTRY_IMPORT',None) and not lib.OPTIONAL_HEADER.DATA_DIRECTORY[5].Size
 assert len(lib.sections)==2 and lib.OPTIONAL_HEADER.ImageBase==0x7120000
 ex={s.name.decode():lib.OPTIONAL_HEADER.ImageBase+s.address for s in lib.DIRECTORY_ENTRY_EXPORT.symbols}
 for n in list(ex):
  if n.startswith('@'):ex[n.split('@')[1]]=ex[n]
 out=bytearray(base);edits=[];image=old.OPTIONAL_HEADER.ImageBase
 def patch(va,before,after,label):
  off=old.get_offset_from_rva(va-image);assert out[off:off+len(before)]==before,(label,hex(va));assert len(before)==len(after)
  out[off:off+len(after)]=after;edits.append(dict(va=va,offset=off,before=before.hex(),after=after.hex(),label=label))
 def hook(va,n,label,opcode=0xe9):
  patch(va,old.get_data(va-image,n),bytes([opcode])+struct.pack('<i',ex[label]-va-5)+b'\x90'*(n-5),label)
 for va,n,name in [(0x6ef3fd1,6,'Factory'),(0x6ef40fa,7,'Layout'),(0x2284490,6,'MenuSelected'),(0x1227ca4,6,'MenuCheckGate'),(0x1228c09,9,'MenuReflowGate'),(0x6ea8000,5,'FeatureGate')]:hook(va,n,name)
 for va,name in [(0x6e8c273,'InputGate'),(0x122502d,'MenuEnabled'),(0x1225d3a,'MenuEnabled'),(0x1226e6f,'MenuRows'),(0x1228733,'MenuSave'),(0x227323a,'TickGetter')]:
  assert old.get_data(va-image,1)==b'\xe8';hook(va,5,name,0xe8)
 patch(0x472d5d8,old.get_data(0x472d5d8-image,4),struct.pack('<I',ex['MenuEvent']),'MenuEvent')
 # The old side-quest coupon callback sends obsolete op1427 (now card-pick).
 # It has the same retained item pointer at +0x2c as the achievement callback.
 # Reuse that native callback/confirmation and the proven action-bearing op515.
 patch(0x49c0434,struct.pack('<I',0x24f86c0),struct.pack('<I',0x24f33b0),'SideQuestCouponNativeSender')
 assert old.get_data(0x1edc0a0-image,8)==bytes.fromhex('558beca13cd46705');hook(0x1edc0a0,8,'DamageGate')
 assert old.get_data(0x287cf50-image,8)==bytes.fromhex('558bec51568b7508');hook(0x287cf50,8,'PickupDistance')
 # Observe the fully formatted native peer stat immediately before its
 # common text-draw path. All arithmetic, equipment/passives and formatting
 # still execute in the original inspected-character renderer.
 assert old.get_data(0x349efaf-image,6)==bytes.fromhex('8d85f0f7ffff');hook(0x349efaf,6,'PeerStatGate')
 assert old.get_data(0x349ca6a-image,6)==bytes.fromhex('8db5f0f7ffff');hook(0x349ca6a,6,'PeerIndependentStatGate')
 assert old.get_data(0x2161f00-image,8)==bytes.fromhex('558bec6aff68d180');hook(0x2161f00,5,'CouponConfirm')
 # A 13-byte adapter fits wholly inside the RDAR function's verified dead
 # alignment padding. Native X pickup's return-origin guard stays unchanged.
 bridge=0x1edc163
 body=bytes.fromhex('585a8704245052')+b'\xe8'+struct.pack('<i',0x25f4970-(bridge+7)-5)+b'\xc3'
 assert len(body)==13
 patch(bridge,b'\xcc'*13,body,'NativePickupBridge')
 # Reuse two section-header slots, keeping all mapped old bytes and protection.
 sections=[];i=0;merged=[]
 while i<len(old.sections):
  sec=old.sections[i]
  if sec.Name.rstrip(b'\0')==b'.mailrg':
   group=old.sections[i:i+3];assert [x.Name.rstrip(b'\0') for x in group]==[b'.mailrg',b'.emright',b'.wk855']
   for a,b in zip(group,group[1:]):
    assert a.Characteristics==b.Characteristics==0x60000020
    assert a.Misc_VirtualSize<=a.SizeOfRawData and b.Misc_VirtualSize<=b.SizeOfRawData
    assert a.VirtualAddress+a.SizeOfRawData==b.VirtualAddress and a.PointerToRawData+a.SizeOfRawData==b.PointerToRawData
   h=bytearray(sec.__pack__());struct.pack_into('<I',h,8,sum(x.SizeOfRawData for x in group));struct.pack_into('<I',h,16,sum(x.SizeOfRawData for x in group));sections.append(h);merged=[x.Name.rstrip(b'\0').decode() for x in group];i+=3
  else:sections.append(bytearray(sec.__pack__()));i+=1
 assert len(sections)==len(old.sections)-2
 first=lib.OPTIONAL_HEADER.ImageBase+lib.sections[0].VirtualAddress-image
 last=old.sections[-1];assert last.Name.rstrip(b'\0')==b'.rack'
 struct.pack_into('<I',sections[-1],8,first-last.VirtualAddress)
 new=[]
 for i,sec in enumerate(lib.sections):
  raw=align(len(out));out.extend(b'\0'*(raw-len(out)));content=sec.get_data();size=align(len(content));out.extend(content);out.extend(b'\0'*(size-len(content)))
  va=lib.OPTIONAL_HEADER.ImageBase+sec.VirtualAddress;rva=va-image;name=b'.cftext' if i==0 else b'.cfdata';flags=0x60000020 if i==0 else 0xc0000040
  sections.append(bytearray(struct.pack('<8sIIIIIIHHI',name,sec.Misc_VirtualSize,rva,size,raw,0,0,0,0,flags)))
  new.append(dict(name=name.decode(),va=va,rva=rva,rawOffset=raw,rawSize=size,virtualSize=sec.Misc_VirtualSize,flags=flags))
 table=old.sections[0].get_file_offset()
 for i,h in enumerate(sections):out[table+40*i:table+40*(i+1)]=h
 opt=old.OPTIONAL_HEADER.get_file_offset();struct.pack_into('<I',out,opt+56,align(new[-1]['rva']+max(new[-1]['virtualSize'],new[-1]['rawSize'])))
 struct.pack_into('<I',out,opt+4,old.OPTIONAL_HEADER.SizeOfCode+new[0]['rawSize']);struct.pack_into('<I',out,opt+8,old.OPTIONAL_HEADER.SizeOfInitializedData+new[1]['rawSize']);struct.pack_into('<I',out,opt+64,0)
 candidate=pefile.PE(data=bytes(out));struct.pack_into('<I',out,opt+64,candidate.generate_checksum());candidate=pefile.PE(data=bytes(out))
 for sec in old.sections:
  expected=bytearray(sec.get_data())
  for e in edits:
   k=e['va']-image-sec.VirtualAddress
   if 0<=k<len(expected):expected[k:k+len(bytes.fromhex(e['after']))]=bytes.fromhex(e['after'])
  assert candidate.get_data(sec.VirtualAddress,len(expected))==expected,sec.Name
 for sec in lib.sections:assert candidate.get_data(lib.OPTIONAL_HEADER.ImageBase+sec.VirtualAddress-image,sec.SizeOfRawData)==sec.get_data()
 for va,n in [(0x32becc0,5),(0x29bbee3,5),(0x276d5f6,6),(0x277cf74,6)]:assert candidate.get_data(va-image,n)==old.get_data(va-image,n)
 assert candidate.OPTIONAL_HEADER.AddressOfEntryPoint==old.OPTIONAL_HEADER.AddressOfEntryPoint
 for i in [1,9,12]:assert candidate.OPTIONAL_HEADER.DATA_DIRECTORY[i].__pack__()==old.OPTIONAL_HEADER.DATA_DIRECTORY[i].__pack__()
 receipt=dict(baseSHA256=sha(base),outputSHA256=sha(out),payloadSHA256=sha(libbytes),exports=ex,edits=edits,newSections=new,mergedHeaders=merged,originalMappedBytesPreservedExceptListedHooks=True,gameExecuted=False,deployed=False)
 dest=args.output;dest.mkdir(parents=True,exist_ok=True);(dest/'DNF.exe').write_bytes(out);(dest/'build.json').write_text(json.dumps(receipt,ensure_ascii=False,indent=2),'utf8')
 print(json.dumps({k:v for k,v in receipt.items() if k not in ('edits','exports')},indent=2))
if __name__=='__main__':build()
