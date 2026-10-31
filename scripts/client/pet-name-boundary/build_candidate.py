"""Build an offline candidate; never install, start, publish or alter the input."""
from pathlib import Path
import argparse, hashlib, json, os, struct, subprocess
import pefile

INPUT_SHA = 'b965e2d1d04b4679eba1ba00de6bbceacdc1ccab8789ea22bef98a28f61283d5'
HOOK = 0x329e74e
ORIGINAL = bytes.fromhex('8b168b52706a0d8d45d4508bceffd2')
def sha(b): return hashlib.sha256(b).hexdigest()
def align(n,a): return (n+a-1)//a*a

def build(source, output, vcvars):
    original=source.read_bytes()
    assert sha(original)==INPUT_SHA, 'candidate requires exact cumulative input'
    pe=pefile.PE(data=original);base=pe.OPTIONAL_HEADER.ImageBase
    assert pe.FILE_HEADER.Machine==0x14c and base==0x400000
    assert not pe.OPTIONAL_HEADER.DllCharacteristics & 0x40
    assert not pe.OPTIONAL_HEADER.DATA_DIRECTORY[4].VirtualAddress
    assert pe.get_overlay_data_start_offset() is None
    assert pe.get_data(HOOK-base,len(ORIGINAL))==ORIGINAL
    output.mkdir(parents=True,exist_ok=False)
    rva=align(max(s.VirtualAddress+max(s.Misc_VirtualSize,s.SizeOfRawData) for s in pe.sections),pe.OPTIONAL_HEADER.SectionAlignment)
    payloadBase=0x10000000
    src=Path(__file__).with_name('pet_name.cpp')
    (output/'pet_name.cpp').write_bytes(src.read_bytes())
    # This is a local compiler entrypoint, with no deployment operations.
    bat='\r\n'.join(['@echo off','setlocal',f'call "{vcvars}" >nul','if errorlevel 1 exit /b 1',
        'cl /nologo /c /O1 /Oi- /Zl /GS- /GR- /EHs-c- /std:c++17 /arch:IA32 /Fopet_name.obj pet_name.cpp',
        'if errorlevel 1 exit /b 1',
        f'link /nologo /DLL /NOENTRY /NODEFAULTLIB /MACHINE:X86 /BASE:0x{payloadBase:x} /FIXED:NO /DYNAMICBASE:NO /NXCOMPAT /SAFESEH:NO /OPT:REF /MAP:pet_name.map /OUT:pet_name.payload pet_name.obj',
        'exit /b %errorlevel%',''])
    (output/'build.cmd').write_text(bat,'utf8')
    p=subprocess.run(['cmd.exe','/d','/c','build.cmd'],cwd=output,capture_output=True)
    (output/'compile.log').write_bytes(p.stdout+p.stderr)
    assert p.returncode==0, 'see compile.log'
    payload=pefile.PE(str(output/'pet_name.payload'))
    assert not getattr(payload,'DIRECTORY_ENTRY_IMPORT',None)
    codeSection=next(s for s in payload.sections if s.Name.rstrip(b'\0')==b'.text')
    assert codeSection.VirtualAddress==0x1000
    assert not any(s.Misc_VirtualSize for s in payload.sections if s.Name.rstrip(b'\0') not in (b'.text',b'.rdata',b'.reloc'))
    # The helper has only relative internal calls and numeric native addresses.
    # Refuse a compiler result that requires relocating any instruction.
    assert not any(e.type and codeSection.VirtualAddress <= e.rva < codeSection.VirtualAddress+codeSection.Misc_VirtualSize
        for block in getattr(payload,'DIRECTORY_ENTRY_BASERELOC',[]) for e in block.entries)
    exports={x.name.decode().lstrip('_').split('@')[0]:base+rva+x.address-codeSection.VirtualAddress for x in payload.DIRECTORY_ENTRY_EXPORT.symbols}
    assert set(exports)=={'ReadPeerPetName','PeerPetNameGate'}
    code=codeSection.get_data()[:codeSection.Misc_VirtualSize]
    header=pe.sections[-1].get_file_offset()+40
    assert header+40<=min(s.PointerToRawData for s in pe.sections if s.SizeOfRawData)
    assert original[header:header+40]==bytes(40)
    raw=align(len(original),pe.OPTIONAL_HEADER.FileAlignment);rawSize=align(len(code),pe.OPTIONAL_HEADER.FileAlignment)
    data=bytearray(original)+bytearray(raw+rawSize-len(original))
    data[raw:raw+len(code)]=code
    struct.pack_into('<8sIIIIIIHHI',data,header,b'.petnm\0\0',len(code),rva,rawSize,raw,0,0,0,0,0x60000020)
    updates=[(pe.FILE_HEADER.get_field_absolute_offset('NumberOfSections'),2,len(pe.sections)+1),
        (pe.OPTIONAL_HEADER.get_field_absolute_offset('SizeOfCode'),4,pe.OPTIONAL_HEADER.SizeOfCode+rawSize),
        (pe.OPTIONAL_HEADER.get_field_absolute_offset('SizeOfImage'),4,align(rva+len(code),pe.OPTIONAL_HEADER.SectionAlignment))]
    for off,size,value in updates:struct.pack_into('<H' if size==2 else '<I',data,off,value)
    hook=pe.get_offset_from_rva(HOOK-base)
    data[hook:hook+len(ORIGINAL)]=b'\xe9'+struct.pack('<i',exports['PeerPetNameGate']-HOOK-5)+b'\x90'*(len(ORIGINAL)-5)
    check=pe.OPTIONAL_HEADER.get_field_absolute_offset('CheckSum')
    struct.pack_into('<I',data,check,pefile.PE(data=bytes(data)).generate_checksum())
    allowed=sorted([(o,o+n) for o,n,_ in updates]+[(header,header+40),(hook,hook+len(ORIGINAL)),(check,check+4)])
    cursor=0
    for left,right in allowed:assert data[cursor:left]==original[cursor:left];cursor=right
    assert data[cursor:len(original)]==original[cursor:]
    final=pefile.PE(data=bytes(data))
    assert pe.OPTIONAL_HEADER.AddressOfEntryPoint==final.OPTIONAL_HEADER.AddressOfEntryPoint
    assert pe.get_imphash()==final.get_imphash()
    assert pe.DIRECTORY_ENTRY_TLS.struct.dump()==final.DIRECTORY_ENTRY_TLS.struct.dump()
    assert source.read_bytes()==original
    target=output/'DNF-pet-name-candidate.exe';target.write_bytes(data)
    receipt=dict(inputSHA256=INPUT_SHA,outputSHA256=sha(data),inputBytes=len(original),outputBytes=len(data),
        hookVA=hex(HOOK),originalHook=ORIGINAL.hex(),exports=exports,helperSHA256=sha(code),
        sectionRVA=rva,sectionRaw=raw,sectionBytes=len(code),sourceSHA256=sha(src.read_bytes()),
        changedExistingRanges=allowed,allOtherExistingBytesPreserved=True,gameExecuted=False,published=False,installed=False)
    (output/'build-receipt.json').write_text(json.dumps(receipt,indent=2),'utf8')
    print(json.dumps(receipt))

if __name__=='__main__':
    p=argparse.ArgumentParser();p.add_argument('--input',type=Path,required=True);p.add_argument('--output',type=Path,required=True)
    p.add_argument('--vcvars',type=Path,required=True);a=p.parse_args();build(a.input,a.output,a.vcvars)
