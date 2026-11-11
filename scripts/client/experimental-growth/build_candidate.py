from pathlib import Path
import hashlib,json,struct,pefile,argparse
D=Path(__file__).parent
BASE_SHA256='69ea3a8b38d70af340baed28ec03e88162e7d3e434f5f99c9d5b71dafc12f544'
def sha(b):return hashlib.sha256(b).hexdigest()
def align(x,a=4096):return (x+a-1)//a*a
def build(base,payload):
 assert sha(base)==BASE_SHA256
 p=pefile.PE(data=base);lib=pefile.PE(data=payload);out=bytearray(base)
 assert not p.get_overlay() and not lib.OPTIONAL_HEADER.DATA_DIRECTORY[5].Size and not getattr(lib,'DIRECTORY_ENTRY_IMPORT',None)
 assert [s.Name.rstrip(b'\0') for s in lib.sections]==[b'.text',b'.data']
 exports={s.name.decode().lstrip('@').split('@')[0]:lib.OPTIONAL_HEADER.ImageBase+s.address for s in lib.DIRECTORY_ENTRY_EXPORT.symbols}
 edits=[]
 for va,name,op,size in [(0x227323a,'Getter',0xe8,5),(0x6ea8000,'Gate',0xe9,5),(0x32c9fc0,'Validate',0xe9,5),(0x32d2080,'Action',0xe9,9),(0x1d4c3bb,'HoverGate',0xe9,8),(0x25eca10,'FindSource',0xe9,6),(0x25fdab0,'NativeSend',0xe9,5)]:
  at=p.get_offset_from_rva(va-0x400000);before=bytes(out[at:at+size]);assert before.hex()=={0x227323a:'e8430ec804',0x6ea8000:'e95fc30400',0x32c9fc0:'558bec6aff',0x32d2080:'558bec81ec20020000',0x1d4c3bb:'8d4dc8e8ad905601',0x25eca10:'558bec83ec0c',0x25fdab0:'558bec6aff'}[va]
  after=bytes([op])+struct.pack('<i',exports[name]-va-5)+b'\x90'*(size-5);out[at:at+size]=after
  edits.append(dict(va=va,offset=at,before=before.hex(),after=after.hex(),name=name))
 secs=[bytearray(s.__pack__()) for s in p.sections];i=next(i for i,s in enumerate(p.sections) if s.Name.rstrip(b'\0')==b'.petnm');group=p.sections[i:i+3]
 assert [s.Name.rstrip(b'\0') for s in group]==[b'.petnm',b'.gld127',b'.p28null']
 for a,b in zip(group,group[1:]):
  assert a.Characteristics==b.Characteristics==0x60000020 and a.PointerToRawData+a.SizeOfRawData==b.PointerToRawData and a.VirtualAddress+align(max(a.Misc_VirtualSize,a.SizeOfRawData))==b.VirtualAddress
 merged=secs[i];merged[:8]=b'.gtprev\0';struct.pack_into('<I',merged,8,group[-1].VirtualAddress+group[-1].Misc_VirtualSize-group[0].VirtualAddress);struct.pack_into('<I',merged,16,sum(s.SizeOfRawData for s in group))
 secs[i:i+3]=[merged]
 firstRVA=lib.OPTIONAL_HEADER.ImageBase+lib.sections[0].VirtualAddress-0x400000
 last=p.sections[-1];assert last.Name.rstrip(b'\0')==b'.krdata' and firstRVA>last.VirtualAddress+align(max(last.Misc_VirtualSize,last.SizeOfRawData))
 struct.pack_into('<I',secs[-1],8,firstRVA-last.VirtualAddress)
 added=[]
 for j,s in enumerate(lib.sections):
  rva=lib.OPTIONAL_HEADER.ImageBase+s.VirtualAddress-0x400000;raw=align(len(out));out.extend(bytes(raw-len(out)));data=s.get_data();size=align(len(data));out.extend(data);out.extend(bytes(size-len(data)))
  name=b'.gttext' if j==0 else b'.gtdata';flags=0x60000020 if j==0 else 0xc0000040
  secs.append(bytearray(struct.pack('<8sIIIIIIHHI',name,s.Misc_VirtualSize,rva,size,raw,0,0,0,0,flags)))
  added.append(dict(name=name.decode(),rva=rva,raw=raw,size=size,virtualSize=s.Misc_VirtualSize))
 assert len(secs)==len(p.sections)
 table=p.sections[0].get_file_offset();out[table:table+40*len(secs)]=b''.join(secs)
 opt=p.OPTIONAL_HEADER.get_file_offset();struct.pack_into('<I',out,opt+56,align(added[-1]['rva']+max(added[-1]['size'],added[-1]['virtualSize'])))
 struct.pack_into('<I',out,opt+4,p.OPTIONAL_HEADER.SizeOfCode+added[0]['size']);struct.pack_into('<I',out,opt+8,p.OPTIONAL_HEADER.SizeOfInitializedData+added[1]['size']);struct.pack_into('<I',out,opt+64,0)
 struct.pack_into('<I',out,opt+64,pefile.PE(data=bytes(out)).generate_checksum());candidate=pefile.PE(data=bytes(out))
 for old in p.sections:
  expected=bytearray(old.get_data())
  for e in edits:
   at=e['va']-0x400000-old.VirtualAddress
   if 0<=at<len(expected):expected[at:at+len(bytes.fromhex(e['after']))]=bytes.fromhex(e['after'])
  assert candidate.get_data(old.VirtualAddress,len(expected))==expected,old.Name
 for a,b in zip(candidate.sections,candidate.sections[1:]):assert a.VirtualAddress+align(max(a.Misc_VirtualSize,a.SizeOfRawData))==b.VirtualAddress
 assert candidate.OPTIONAL_HEADER.AddressOfEntryPoint==p.OPTIONAL_HEADER.AddressOfEntryPoint
 for n in [1,9]:assert candidate.OPTIONAL_HEADER.DATA_DIRECTORY[n].__pack__()==p.OPTIONAL_HEADER.DATA_DIRECTORY[n].__pack__()
 return bytes(out),dict(inputSHA256=sha(base),outputSHA256=sha(out),payloadSHA256=sha(payload),exports=exports,edits=edits,addedSections=added,originalMappedBytesPreservedExceptHooks=True,contiguousSections=True,entryImportsTLSPreserved=True,gameExecuted=False,deployed=False)
def main():
 p=argparse.ArgumentParser(description='Reproduce the accepted no-diagnostic unified growth-ticket client (base 69ea3a8b, output 250d78d2).')
 p.add_argument('--base',required=True,type=Path,help='exact 69ea3a8b client base DNF.exe')
 p.add_argument('--payload',required=True,type=Path,help='growth.payload produced by build.cmd')
 p.add_argument('--out',required=True,type=Path,help='new output DNF.exe path (must not exist)')
 a=p.parse_args()
 if a.out.exists():p.error('output must be a new file')
 if a.out.resolve()==a.base.resolve():p.error('must not overwrite base')
 b,r=build(a.base.read_bytes(),a.payload.read_bytes())
 with a.out.open('xb') as f:f.write(b)
 receipt=Path(str(a.out)+'.json')
 with receipt.open('x',encoding='utf8') as f:json.dump(r,f,indent=2)
 print(json.dumps({k:r[k] for k in ['inputSHA256','outputSHA256','payloadSHA256','originalMappedBytesPreservedExceptHooks','contiguousSections','entryImportsTLSPreserved']}))
if __name__=='__main__':main()
