"""Offline current-image game-thread registration/receipt candidate builder."""
from pathlib import Path
import argparse,hashlib,importlib.util,json,struct,subprocess,pefile
INPUT='7970c5ddb4253d79d99bcc958a51b2e3a7e0f3bdf9efdcc63f5322f646e52760'
HERE=Path(__file__).resolve().parent
def align(n):return (n+4095)&~4095
def digest(b):return hashlib.sha256(b).hexdigest()
def main():
 a=argparse.ArgumentParser(description=__doc__);a.add_argument('--input',type=Path,required=True);a.add_argument('--out',type=Path,required=True);args=a.parse_args()
 data=args.input.read_bytes();assert digest(data)==INPUT
 args.out.mkdir(exist_ok=False,parents=True);d=args.out.resolve();p=pefile.PE(data=data)
 spec=importlib.util.spec_from_file_location('lifecycle',HERE.parent/'party-room-lifecycle-registration/build_candidate.py');old=importlib.util.module_from_spec(spec);spec.loader.exec_module(old)
 bindings={'STATE_ADDRESS':0x7105b00,'PROCESS_SLOT':old.process_slot_address()}
 hooks=[(0x227323a,'Getter',0xe8,'OLD_GETTER'),(0x6f202d3,'Dispatch',0xe8,'OLD_DISPATCH'),(0x2523a13,'Gate',0xe8,'OLD_GATE'),(0x288abf4,'RegisteredHook',0xe9,'OLD_REG_HOOK')]
 for va,name,opcode,key in hooks:
  b=p.get_data(va-0x400000,5);assert b[0]==opcode
  bindings[key]=va+5+struct.unpack_from('<i',b,1)[0]
 (d/'binding.h').write_text(''.join(f'#define {k} 0x{v:x}\n' for k,v in bindings.items()),encoding='utf-8')
 (d/'registration.cpp').write_bytes((HERE/'registration.cpp').read_bytes())
 cmd='''@echo off
setlocal
call "C:\\Program Files\\Microsoft Visual Studio\\18\\Community\\VC\\Auxiliary\\Build\\vcvars32.bat" >nul
if errorlevel 1 exit /b 1
cd /d "%~dp0"
cl /nologo /c /O1 /Oi- /Zl /GS- /GR- /EHs-c- /utf-8 /std:c++17 /arch:IA32 /Brepro /Foregistration.obj registration.cpp
if errorlevel 1 exit /b 1
link /nologo /DLL /NOENTRY /NODEFAULTLIB /MACHINE:X86 /BASE:0x07110000 /FIXED /DYNAMICBASE:NO /NXCOMPAT /SAFESEH:NO /OPT:REF /OPT:ICF /Brepro /MERGE:.rdata=.text /MAP:registration.map /OUT:registration.payload registration.obj
'''
 (d/'build.cmd').write_text(cmd,encoding='utf-8',newline='\r\n')
 proc=subprocess.run(['cmd','/c',str(d/'build.cmd')],cwd=d,capture_output=True,creationflags=0x08000000)
 (d/'compile.log').write_bytes(proc.stdout+proc.stderr)
 if proc.returncode:print((proc.stdout+proc.stderr).decode('gbk',errors='replace').encode('ascii','backslashreplace').decode());raise SystemExit(proc.returncode)
 lib=pefile.PE(str(d/'registration.payload'))
 assert not p.get_overlay() and not lib.OPTIONAL_HEADER.DATA_DIRECTORY[5].Size and not getattr(lib,'DIRECTORY_ENTRY_IMPORT',None)
 assert len(lib.sections)==1 and lib.sections[0].Name.rstrip(b'\0')==b'.text'
 exports={s.name.decode().lstrip('_'):lib.OPTIONAL_HEADER.ImageBase+s.address for s in lib.DIRECTORY_ENTRY_EXPORT.symbols}
 out=bytearray(data);secs=[bytearray(s.__pack__()) for s in p.sections]
 i=next(i for i,s in enumerate(p.sections) if s.Name.rstrip(b'\0')==b'.gtprev');first,last=p.sections[i:i+2]
 assert last.Name.rstrip(b'\0')==b'.krtext' and first.Characteristics==last.Characteristics==0x60000020
 gap=last.VirtualAddress-first.VirtualAddress-first.SizeOfRawData
 assert gap==0x5000 and first.PointerToRawData+first.SizeOfRawData==last.PointerToRawData
 # Preserve the old virtual zero gap when combining two RX headers. Simply
 # summing their raw sizes would silently relocate every byte of .krtext.
 cut=last.PointerToRawData;out[cut:cut]=bytes(gap)
 for j,s in enumerate(p.sections):
  if s.PointerToRawData>=cut:struct.pack_into('<I',secs[j],20,s.PointerToRawData+gap)
 merged=secs[i];merged[:8]=b'.rprior\0'
 struct.pack_into('<I',merged,8,last.VirtualAddress+last.Misc_VirtualSize-first.VirtualAddress)
 struct.pack_into('<I',merged,16,first.SizeOfRawData+gap+last.SizeOfRawData)
 secs[i:i+2]=[merged]
 rx=lib.sections[0];rva=lib.OPTIONAL_HEADER.ImageBase+rx.VirtualAddress-0x400000
 tail=p.sections[-1];assert tail.Name.rstrip(b'\0')==b'.gtdata'
 assert tail.VirtualAddress+align(max(tail.Misc_VirtualSize,tail.SizeOfRawData))<=bindings['STATE_ADDRESS']-0x400000+0x500
 assert bindings['STATE_ADDRESS']>=0x400000+tail.VirtualAddress+tail.Misc_VirtualSize
 assert bindings['STATE_ADDRESS']+0x3000<=0x400000+rva
 struct.pack_into('<I',secs[-1],8,rva-tail.VirtualAddress)
 raw=align(len(out));out.extend(bytes(raw-len(out)));code=rx.get_data();out.extend(code);out.extend(bytes(align(len(code))-len(code)))
 secs.append(bytearray(struct.pack('<8sIIIIIIHHI',b'.rack',rx.Misc_VirtualSize,rva,align(len(code)),raw,0,0,0,0,0x60000020)))
 assert len(secs)==len(p.sections)
 table=p.sections[0].get_file_offset();out[table:table+40*len(secs)]=b''.join(secs)
 edits=[]
 for va,name,opcode,key in hooks:
  off=p.get_offset_from_rva(va-0x400000);at=off+(gap if off>=cut else 0)
  before=data[off:off+5];after=bytes([opcode])+struct.pack('<i',exports[name]-va-5)
  out[at:at+5]=after;edits.append(dict(va=va,name=name,before=before.hex(),after=after.hex()))
 opt=p.OPTIONAL_HEADER.get_file_offset()
 struct.pack_into('<I',out,opt+56,align(rva+max(rx.Misc_VirtualSize,rx.SizeOfRawData)))
 struct.pack_into('<I',out,opt+4,p.OPTIONAL_HEADER.SizeOfCode+gap+align(len(code)))
 struct.pack_into('<I',out,opt+64,0);struct.pack_into('<I',out,opt+64,pefile.PE(data=bytes(out)).generate_checksum())
 candidate=pefile.PE(data=bytes(out))
 for original in p.sections:
  expected=bytearray(original.get_data())
  for e in edits:
   at=e['va']-0x400000-original.VirtualAddress
   if 0<=at<len(expected):expected[at:at+5]=bytes.fromhex(e['after'])
  assert candidate.get_data(original.VirtualAddress,len(expected))==expected,original.Name
 for x,y in zip(candidate.sections,candidate.sections[1:]):assert x.VirtualAddress+align(max(x.Misc_VirtualSize,x.SizeOfRawData))==y.VirtualAddress
 assert candidate.OPTIONAL_HEADER.AddressOfEntryPoint==p.OPTIONAL_HEADER.AddressOfEntryPoint
 for n in [1,9]:assert candidate.OPTIONAL_HEADER.DATA_DIRECTORY[n].__pack__()==p.OPTIONAL_HEADER.DATA_DIRECTORY[n].__pack__()
 (d/'DNF.exe').write_bytes(out)
 receipt=dict(inputSHA256=INPUT,outputSHA256=digest(out),bytes=len(out),bindings=bindings,exports=exports,hooks=edits,payloadSHA256=digest((d/'registration.payload').read_bytes()),originalMappedBytesPreservedExceptHooks=True,zeroGapPreserved=gap,entryImportsTLSPreserved=True,gameThreadOnly=True,maximumMilliseconds=15000,maximumAttempts=60,packetReplay=False,monsterCreation=False,skillReplay=False,gameAccepted=False,installed=False)
 (d/'build.json').write_text(json.dumps(receipt,indent=2)+'\n',encoding='utf-8');print(json.dumps(receipt))
if __name__=='__main__':main()
