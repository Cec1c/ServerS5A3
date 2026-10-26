"""Independent PE and byte boundary checks, without importing the patch builder."""
from pathlib import Path
import sys,struct,hashlib,json
import argparse
parser=argparse.ArgumentParser(description=__doc__)
for name in ['baseline','candidate','output']:
    parser.add_argument('--'+name,type=Path,required=True)
parser.add_argument('--python-deps')
args=parser.parse_args()
if args.python_deps:sys.path.insert(0,args.python_deps)
if args.output.exists():parser.error('new output required')
import pefile
a=args.baseline.read_bytes()
b=args.candidate.read_bytes()
assert len(a)==111849472 and hashlib.sha256(a).hexdigest()=='44aecee7c48bc1c89582ef1cd95694950569951caeef3b22ec3478c917b8ce7e'
assert len(b)==111853568 and hashlib.sha256(b).hexdigest()=='aac86cd74eb2a0155f025e92c394124ffaaeceecf50479fe26e178fe2b04d661'
old,new=pefile.PE(data=a),pefile.PE(data=b)
assert old.FILE_HEADER.NumberOfSections==33 and new.FILE_HEADER.NumberOfSections==34
assert new.OPTIONAL_HEADER.CheckSum==new.generate_checksum()
assert new.OPTIONAL_HEADER.AddressOfEntryPoint==old.OPTIONAL_HEADER.AddressOfEntryPoint
assert new.OPTIONAL_HEADER.DllCharacteristics==old.OPTIONAL_HEADER.DllCharacteristics
assert new.OPTIONAL_HEADER.ImageBase==old.OPTIONAL_HEADER.ImageBase==0x400000
site=old.get_offset_from_rva(0x1872744-0x400000)
assert a[site:site+7]==bytes.fromhex('0fb68bac000000')
assert b[site]==0xe9 and b[site+5:site+7]==b'\x90'*2
target=0x1872744+5+struct.unpack_from('<i',b,site+1)[0]
s=new.sections[-1]
assert s.Name.rstrip(b'\0')==b'.antbar' and s.Characteristics==0x60000020
assert target==s.VirtualAddress+0x400000
assert s.PointerToRawData==len(a) and s.SizeOfRawData==4096
assert s.Misc_VirtualSize==53
assert new.OPTIONAL_HEADER.SizeOfImage==old.OPTIONAL_HEADER.SizeOfImage+4096
assert not any(b[len(a)+s.Misc_VirtualSize:])
allowed=[(old.FILE_HEADER.get_field_absolute_offset('NumberOfSections'),2),
    (old.OPTIONAL_HEADER.get_field_absolute_offset('SizeOfCode'),4),
    (old.OPTIONAL_HEADER.get_field_absolute_offset('SizeOfImage'),4),
    (old.OPTIONAL_HEADER.get_field_absolute_offset('CheckSum'),4),
    (s.get_file_offset(),40),(site,7)]
allowed_bytes=set(k for start,size in allowed for k in range(start,start+size))
changes=[i for i in range(len(a)) if a[i]!=b[i]]
assert set(changes)<=allowed_bytes
for n in range(33):assert old.sections[n].__pack__()==new.sections[n].__pack__()
# Existing class-specific receiver chain, including the three v152 job books,
# is outside the only modified native instruction. Whole baseline diff proves
# preservation; also identify these windows explicitly for the receipt.
for va,size in [(0x2523a05,0x20),(0x6eaa000,74),(0x6ea9000,0x100),(0x6e83610,0x24),(0x6e92300,0xc0)]:
    off=old.get_offset_from_rva(va-0x400000)
    assert a[off:off+size]==b[off:off+size]
result=dict(passed=True,changed_original_bytes=len(changes),new_section_virtual_size=s.Misc_VirtualSize,
    checksum=new.OPTIONAL_HEADER.CheckSum,receiver_chain_unchanged=True,input_sha256=hashlib.sha256(a).hexdigest(),output_sha256=hashlib.sha256(b).hexdigest())
args.output.write_text(json.dumps(result,indent=2),'utf8')
print(json.dumps(result))
