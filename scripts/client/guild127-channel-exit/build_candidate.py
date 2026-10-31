"""Build a separate, offline NOTI127 null-role guard candidate."""
from pathlib import Path
import argparse, hashlib, json, struct
import pefile

INPUTS = {
    'b965e2d1d04b4679eba1ba00de6bbceacdc1ccab8789ea22bef98a28f61283d5',
    '58482cc5102cb737067c42c2b3c61a987a76d1b1611c04bb909c8fb87bbbaa1f',
}
HOOK = 0x02038539
ORIGINAL = bytes.fromhex('8bc8e86039be01')
RETURN = 0x02038540
EMPTY = 0x020385b7

def sha(b): return hashlib.sha256(b).hexdigest()
def align(n,a): return (n+a-1)//a*a
def rel(op, at, target): return bytes([op])+struct.pack('<i',target-at-5)

def build(source, output):
    original=source.read_bytes(); digest=sha(original)
    assert digest in INPUTS, 'requires a verified cumulative input'
    p=pefile.PE(data=original); base=p.OPTIONAL_HEADER.ImageBase
    assert p.FILE_HEADER.Machine==0x14c and base==0x400000
    assert not p.OPTIONAL_HEADER.DllCharacteristics & 0x40
    assert not p.OPTIONAL_HEADER.DATA_DIRECTORY[4].VirtualAddress
    assert p.get_overlay_data_start_offset() is None
    assert p.get_data(HOOK-base,len(ORIGINAL))==ORIGINAL
    assert p.get_data(EMPTY-base,4)==bytes.fromhex('8be55dc3')
    assert p.get_data(0x3c1bea0-base,4)==bytes.fromhex('8d4130c3')
    output.mkdir(parents=True,exist_ok=False)
    rva=align(max(s.VirtualAddress+max(s.Misc_VirtualSize,s.SizeOfRawData) for s in p.sections),p.OPTIONAL_HEADER.SectionAlignment)
    va=base+rva
    # Save flags as well as nonvolatile registers. Valid objects follow the
    # original accessor; absent objects return through the callback epilogue.
    code=bytearray(bytes.fromhex('9c85c0740d9d8bc8'))
    code+=rel(0xe8,va+len(code),0x3c1bea0)
    code+=rel(0xe9,va+len(code),RETURN)
    assert len(code)==18
    code+=b'\x9d'+rel(0xe9,va+19,EMPTY)
    header=p.sections[-1].get_file_offset()+40
    assert header+40<=min(s.PointerToRawData for s in p.sections if s.SizeOfRawData)
    assert original[header:header+40]==bytes(40)
    raw=align(len(original),p.OPTIONAL_HEADER.FileAlignment)
    rawsize=align(len(code),p.OPTIONAL_HEADER.FileAlignment)
    data=bytearray(original)+bytearray(raw+rawsize-len(original));data[raw:raw+len(code)]=code
    struct.pack_into('<8sIIIIIIHHI',data,header,b'.gld127\0',len(code),rva,rawsize,raw,0,0,0,0,0x60000020)
    updates=[(p.FILE_HEADER.get_field_absolute_offset('NumberOfSections'),2,len(p.sections)+1),
        (p.OPTIONAL_HEADER.get_field_absolute_offset('SizeOfCode'),4,p.OPTIONAL_HEADER.SizeOfCode+rawsize),
        (p.OPTIONAL_HEADER.get_field_absolute_offset('SizeOfImage'),4,align(rva+len(code),p.OPTIONAL_HEADER.SectionAlignment))]
    for off,size,value in updates:struct.pack_into('<H' if size==2 else '<I',data,off,value)
    hook=p.get_offset_from_rva(HOOK-base)
    data[hook:hook+len(ORIGINAL)]=rel(0xe9,HOOK,va)+b'\x90'*2
    check=p.OPTIONAL_HEADER.get_field_absolute_offset('CheckSum')
    struct.pack_into('<I',data,check,pefile.PE(data=bytes(data)).generate_checksum())
    allowed=sorted([(o,o+n) for o,n,_ in updates]+[(header,header+40),(hook,hook+len(ORIGINAL)),(check,check+4)])
    cursor=0
    for left,right in allowed:assert data[cursor:left]==original[cursor:left];cursor=right
    assert data[cursor:len(original)]==original[cursor:]
    final=pefile.PE(data=bytes(data))
    assert p.OPTIONAL_HEADER.AddressOfEntryPoint==final.OPTIONAL_HEADER.AddressOfEntryPoint
    assert p.get_imphash()==final.get_imphash()
    assert p.DIRECTORY_ENTRY_TLS.struct.dump()==final.DIRECTORY_ENTRY_TLS.struct.dump()
    assert source.read_bytes()==original
    target=output/'DNF-guild127-candidate.exe';target.write_bytes(data)
    receipt=dict(inputSHA256=digest,outputSHA256=sha(data),inputBytes=len(original),outputBytes=len(data),
        hookVA=hex(HOOK),originalHook=ORIGINAL.hex(),sectionVA=hex(va),sectionRaw=raw,sectionBytes=len(code),
        sourceSHA256=sha(Path(__file__).read_bytes()),changedExistingRanges=allowed,
        allOtherExistingBytesPreserved=True,gameExecuted=False,published=False,installed=False)
    (output/'build-receipt.json').write_text(json.dumps(receipt,indent=2),'utf8')
    print(json.dumps(receipt))

if __name__=='__main__':
    p=argparse.ArgumentParser();p.add_argument('--input',type=Path,required=True);p.add_argument('--output',type=Path,required=True)
    a=p.parse_args();build(a.input,a.output)
