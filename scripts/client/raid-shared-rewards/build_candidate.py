"""Append a read-only per-owner completion gate to the exact installed image."""
from pathlib import Path
import argparse, hashlib, importlib.util, json, struct, subprocess
import pefile
HERE=Path(__file__).resolve().parent
INPUT='71571e228d1cfde78fd2b4d75c7315ab1c7d7755865a1b95e7dcf1bd98a1b6f5'
SITE=0x8ed5bc
BEFORE=bytes.fromhex('8bcb c745ec00000000')
def sha(b): return hashlib.sha256(b).hexdigest()
def main():
 ap=argparse.ArgumentParser(description=__doc__);ap.add_argument('--input',type=Path,required=True);ap.add_argument('--out',type=Path,required=True);a=ap.parse_args()
 data=a.input.read_bytes();assert sha(data)==INPUT
 p=pefile.PE(data=data);tail=p.sections[-1]
 assert tail.Name.rstrip(b'\0')==b'.rack' and tail.Characteristics==0x60000020
 assert tail.PointerToRawData+tail.SizeOfRawData==len(data) and not p.get_overlay()
 assert tail.SizeOfRawData==0x1000 and tail.Misc_VirtualSize<=0x1000
 assert p.get_data(SITE-0x400000,len(BEFORE))==BEFORE
 a.out.mkdir(parents=True,exist_ok=False);d=a.out.resolve()
 (d/'completion.cpp').write_bytes((HERE/'completion.cpp').read_bytes())
 cmd='''@echo off
call "C:\\Program Files\\Microsoft Visual Studio\\18\\Community\\VC\\Auxiliary\\Build\\vcvars32.bat" >nul
if errorlevel 1 exit /b 1
cd /d "%~dp0"
cl /nologo /c /O1 /Oi- /Zl /GS- /GR- /EHs-c- /std:c++17 /arch:IA32 /Brepro completion.cpp
if errorlevel 1 exit /b 1
link /nologo /DLL /NOENTRY /NODEFAULTLIB /MACHINE:X86 /BASE:0x07110000 /ALIGN:8192 /FIXED /DYNAMICBASE:NO /NXCOMPAT /SAFESEH:NO /OPT:REF /Brepro /MERGE:.rdata=.text /OUT:completion.payload completion.obj
'''
 (d/'build.cmd').write_text(cmd,encoding='utf-8',newline='\r\n')
 proc=subprocess.run(['cmd','/c',str(d/'build.cmd')],cwd=d,capture_output=True,creationflags=0x08000000)
 (d/'compile.log').write_bytes(proc.stdout+proc.stderr);assert proc.returncode==0,proc.stdout+proc.stderr
 lib=pefile.PE(str(d/'completion.payload'));assert len(lib.sections)==1
 assert not lib.OPTIONAL_HEADER.DATA_DIRECTORY[5].Size and not getattr(lib,'DIRECTORY_ENTRY_IMPORT',None)
 sec=lib.sections[0];base=lib.OPTIONAL_HEADER.ImageBase+sec.VirtualAddress
 assert base==0x7112000==0x400000+tail.VirtualAddress+tail.SizeOfRawData
 exports={s.name.decode().lstrip('_'):lib.OPTIONAL_HEADER.ImageBase+s.address for s in lib.DIRECTORY_ENTRY_EXPORT.symbols}
 assert sec.Misc_VirtualSize<0x800
 spec=importlib.util.spec_from_file_location('emitter',HERE.parent/'party-reward-current-group/build_candidate.py');e=importlib.util.module_from_spec(spec);spec.loader.exec_module(e)
 stub=base+0x800;c=e.Code(stub)
 c.raw('53');c.call(exports['OwnRootComplete']);c.raw('83c404 83f802');c.je('fallback')
 c.raw('84c0');c.jne(0x8ed63b);c.raw('c645f300');c.jump(0x8ed63b)
 c.label('fallback');c.raw(BEFORE.hex());c.jump(SITE+len(BEFORE));code=c.finish()
 block=bytearray(0x1000);block[:sec.Misc_VirtualSize]=sec.get_data()[:sec.Misc_VirtualSize];block[0x800:0x800+len(code)]=code
 out=bytearray(data)+block
 at=p.get_offset_from_rva(SITE-0x400000);after=b'\xe9'+struct.pack('<i',stub-SITE-5)+b'\x90'*4;out[at:at+9]=after
 head=tail.get_file_offset();struct.pack_into('<I',out,head+8,0x2000);struct.pack_into('<I',out,head+16,0x2000)
 opt=p.OPTIONAL_HEADER.get_file_offset();struct.pack_into('<I',out,opt+56,p.OPTIONAL_HEADER.SizeOfImage+0x1000);struct.pack_into('<I',out,opt+4,p.OPTIONAL_HEADER.SizeOfCode+0x1000)
 struct.pack_into('<I',out,opt+64,0);struct.pack_into('<I',out,opt+64,pefile.PE(data=bytes(out)).generate_checksum())
 q=pefile.PE(data=bytes(out))
 for old in p.sections:
  expected=bytearray(old.get_data());relative=SITE-0x400000-old.VirtualAddress
  if 0<=relative<len(expected):expected[relative:relative+9]=after
  assert q.get_data(old.VirtualAddress,len(expected))==expected,old.Name
 assert q.OPTIONAL_HEADER.AddressOfEntryPoint==p.OPTIONAL_HEADER.AddressOfEntryPoint
 for n in (1,9):assert q.OPTIONAL_HEADER.DATA_DIRECTORY[n].__pack__()==p.OPTIONAL_HEADER.DATA_DIRECTORY[n].__pack__()
 (d/'DNF.exe').write_bytes(out)
 record=dict(inputSHA256=INPUT,outputSHA256=sha(out),bytes=len(out),payloadVA=base,payloadSize=len(block),exports=exports,hook=dict(va=SITE,before=BEFORE.hex(),after=after.hex(),stub=stub),originalMappedBytesPreservedExceptHook=True,entryImportsTLSPreserved=True,noMutableState=True,gameAccepted=False,installed=False)
 (d/'build.json').write_text(json.dumps(record,indent=2)+'\n',encoding='utf-8');print(json.dumps(record))
if __name__=='__main__':main()
