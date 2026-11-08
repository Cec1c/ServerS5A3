"""Build an offline, exact-base EXE candidate; never installs or runs it."""
import argparse, hashlib, json, struct
from pathlib import Path
import pefile

BASE_SHA='686eaa069ba3cc374520d249628efc9de89bf0892ad45ad1e4bd2d2206b08dc5'
def sha(b):return hashlib.sha256(b).hexdigest()
def require(ok,msg):
    if not ok:raise ValueError(msg)
def align(n,a=4096):return (n+a-1)//a*a
def build(base,payload):
    require(sha(base)==BASE_SHA,'exact G211 private cumulative baseline required')
    old=pefile.PE(data=base);lib=pefile.PE(data=payload);image=old.OPTIONAL_HEADER.ImageBase
    require(image==0x400000 and old.FILE_HEADER.Machine==0x14c,'PE32 contract')
    require(not old.get_overlay() and not old.OPTIONAL_HEADER.DATA_DIRECTORY[4].Size,'unexpected overlay/signature')
    require(not getattr(lib,'DIRECTORY_ENTRY_IMPORT',None),'payload must not add imports')
    require(not lib.OPTIONAL_HEADER.DATA_DIRECTORY[5].Size,'payload must be fixed at its compiled addresses')
    require(len(lib.sections)==2 and [s.Name.rstrip(b'\0') for s in lib.sections]==[b'.text',b'.data'],'payload must have separate RX and RW sections')
    exports={s.name.decode():lib.OPTIONAL_HEADER.ImageBase+s.address for s in lib.DIRECTORY_ENTRY_EXPORT.symbols}
    for decorated in list(exports):
        if decorated.startswith('@'):exports[decorated.split('@')[1]]=exports[decorated]
    out=bytearray(base);edits=[]
    def change(va,before,after,label):
        at=old.get_offset_from_rva(va-image);require(bytes(out[at:at+len(before)])==before,label+' input mismatch')
        require(len(before)==len(after),label+' unexpected size change');out[at:at+len(after)]=after
        edits.append(dict(label=label,va=va,offset=at,before=before.hex(),after=after.hex()))
    def call(va,target,name):
        change(va,b'\xe8'+struct.pack('<i',target-va-5),b'\xe8'+struct.pack('<i',exports[name]-va-5),name)
    # These shared calls are also exact guards and temporary hook ownership
    # contracts in the cumulative statistics/ranking/battle-pass module.
    # Keep them byte-identical; extending only the predecessor dispatchers
    # lets that module install and restore its existing hooks unchanged.
    for va,target in [(0x32becc0,0x6ef3fd1),(0x29bbee3,0x6ef40fa)]:
        require(old.get_data(va-image,5)==b'\xe8'+struct.pack('<i',target-va-5),'shared predecessor call changed')
    for va,before,name in [(0x6ef3fd1,bytes.fromhex('558bec83ec0c'),'Factory'),
                           (0x6ef40fa,bytes.fromhex('803d1822f00600'),'Layout')]:
        change(va,before,b'\xe9'+struct.pack('<i',exports[name]-va-5)+b'\x90'*(len(before)-5),name+'Dispatch')
    call(0x6e8c273,0x3840a30,'InputGate')
    for va,target,name in [(0x472d5d8,0x1228760,'MenuEvent')]:
        change(va,struct.pack('<I',target),struct.pack('<I',exports[name]),name)
    call(0x122502d,0x12249e0,'MenuEnabled')
    call(0x1225d3a,0x12249e0,'MenuEnabled')
    call(0x1226e6f,0x242c910,'MenuRows');call(0x1228733,0x1226370,'MenuSave')
    for va,before,name in [(0x2284490,bytes.fromhex('558bec8b4508'),'MenuSelected'),
                           (0x1227ca4,bytes.fromhex('837b3c2f7c45'),'MenuCheckGate'),
                           (0x1228c09,bytes.fromhex('837b3c2f0f8da0020000'),'MenuReflowGate')]:
        change(va,before,b'\xe9'+struct.pack('<i',exports[name]-va-5)+b'\x90'*(len(before)-5),name)
    # The original 93 headers fill its 4KiB header page. Merge only three
    # adjacent, identical-protection tail sections; their bytes and RVAs stay
    # exactly in place. Reuse two header slots for the new RX and RW sections.
    tail=old.sections[-3:]
    require([s.Name.rstrip(b'\0') for s in tail]==[b'.p28null',b'.sfnull',b'.ctnull'],'unexpected tail section ownership')
    for i,s in enumerate(tail):
        require(s.Characteristics==0x60000020 and s.SizeOfRawData==4096 and s.Misc_VirtualSize<=4096,'tail protection/size mismatch')
        require(s.PointerToRelocations==s.PointerToLinenumbers==s.NumberOfRelocations==s.NumberOfLinenumbers==0,'unsupported COFF tail metadata')
        if i:require(s.VirtualAddress==tail[i-1].VirtualAddress+4096 and s.PointerToRawData==tail[i-1].PointerToRawData+4096,'tail must be contiguous in file AND memory')
    # LINK requires a 64KiB module base. Account for the resulting alignment
    # gap as zero-filled virtual tail, so Windows sees contiguous section RVAs.
    first_payload_rva=lib.OPTIONAL_HEADER.ImageBase+lib.sections[0].VirtualAddress-image
    merged=bytearray(tail[0].__pack__());struct.pack_into('<I',merged,8,first_payload_rva-tail[0].VirtualAddress);struct.pack_into('<I',merged,16,12288)
    table=tail[0].get_file_offset();out[table:table+40]=merged
    newSections=[]
    for i,s in enumerate(lib.sections):
        va=lib.OPTIONAL_HEADER.ImageBase+s.VirtualAddress;rva=va-image
        require(va>=image+tail[-1].VirtualAddress+4096,'payload overlaps original game')
        raw=align(len(out));out.extend(b'\0'*(raw-len(out)))
        content=s.get_data();rawsize=align(max(len(content),1));out.extend(content);out.extend(b'\0'*(rawsize-len(content)))
        name=b'.krtext' if i==0 else b'.krdata';flags=0x60000020 if i==0 else 0xc0000040
        header=struct.pack('<8sIIIIIIHHI',name,s.Misc_VirtualSize,rva,rawsize,raw,0,0,0,0,flags)
        out[table+40*(i+1):table+40*(i+2)]=header
        newSections.append(dict(name=name.decode(),va=va,rva=rva,virtualSize=s.Misc_VirtualSize,rawOffset=raw,rawSize=rawsize,characteristics=flags))
    opt=old.OPTIONAL_HEADER.get_file_offset()
    struct.pack_into('<I',out,opt+56,align(newSections[-1]['rva']+max(newSections[-1]['virtualSize'],newSections[-1]['rawSize'])))
    struct.pack_into('<I',out,opt+4,old.OPTIONAL_HEADER.SizeOfCode+newSections[0]['rawSize'])
    struct.pack_into('<I',out,opt+8,old.OPTIONAL_HEADER.SizeOfInitializedData+newSections[1]['rawSize'])
    struct.pack_into('<I',out,opt+64,0)
    candidate=pefile.PE(data=bytes(out));struct.pack_into('<I',out,opt+64,candidate.generate_checksum())
    candidate=pefile.PE(data=bytes(out))
    require(candidate.FILE_HEADER.NumberOfSections==old.FILE_HEADER.NumberOfSections,'section count changed')
    # Check all original section bytes at the same virtual addresses, allowing
    # only the explicitly reviewed hook edits. This covers all cumulative client fixes.
    for sec in old.sections:
        expected=bytearray(sec.get_data())
        for edit in edits:
            start=edit['va']-image-sec.VirtualAddress
            if 0<=start<len(expected):expected[start:start+len(bytes.fromhex(edit['after']))]=bytes.fromhex(edit['after'])
        actual=candidate.get_data(sec.VirtualAddress,len(expected))
        require(actual==expected,'original mapped bytes changed: '+sec.Name.decode())
    for sec in lib.sections:
        rva=lib.OPTIONAL_HEADER.ImageBase+sec.VirtualAddress-image
        require(candidate.get_data(rva,sec.SizeOfRawData)==sec.get_data(),'compiled payload mapping mismatch')
    require(candidate.OPTIONAL_HEADER.AddressOfEntryPoint==old.OPTIONAL_HEADER.AddressOfEntryPoint,'entrypoint changed')
    require(candidate.OPTIONAL_HEADER.DATA_DIRECTORY[1].__pack__()==old.OPTIONAL_HEADER.DATA_DIRECTORY[1].__pack__(),'import directory changed')
    return bytes(out),dict(schema=1,baseSha256=sha(base),outputSha256=sha(out),outputBytes=len(out),payloadSha256=sha(payload),
       exports=exports,edits=edits,newSections=newSections,mergedTailHeaders=[s.Name.rstrip(b'\0').decode() for s in tail],
       originalMappedBytesPreservedExceptListedHooks=True,originalEntryPointAndImportsPreserved=True,
       existingSectionPageProtectionsPreserved=True,pvfChanged=False,npkChanged=False,indexesRegenerated=False,
       newRequiredAsset="ImagePacks2/sprite_interface_privatekeyrepeat002.NPK",gameExecuted=False,deployed=False)
if __name__=='__main__':
    p=argparse.ArgumentParser();p.add_argument('base',type=Path);p.add_argument('payload',type=Path);p.add_argument('output',type=Path);p.add_argument('receipt',type=Path)
    a=p.parse_args();data,result=build(a.base.read_bytes(),a.payload.read_bytes());a.output.parent.mkdir(parents=True,exist_ok=True)
    with a.output.open('xb') as f:f.write(data)
    with a.receipt.open('x',encoding='utf8') as f:json.dump(result,f,ensure_ascii=False,indent=2)
    print(json.dumps(result,ensure_ascii=False,indent=2))
