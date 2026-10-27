"""Independent pefile contract for the exact cumulative PartyFace candidate."""
from pathlib import Path
import argparse, hashlib, json, struct, sys
sys.dont_write_bytecode=True
parser=argparse.ArgumentParser()
for name in ('baseline','candidate','output'):parser.add_argument('--'+name,required=True,type=Path)
parser.add_argument('--python-deps',type=Path)
args=parser.parse_args()
if args.python_deps:sys.path.insert(0,str(args.python_deps))
if args.output.exists():raise ValueError('new output path required')
import pefile
base=args.baseline.read_bytes();data=args.candidate.read_bytes()
sha=lambda b:hashlib.sha256(b).hexdigest()
assert sha(base)=='aac86cd74eb2a0155f025e92c394124ffaaeceecf50479fe26e178fe2b04d661'
assert sha(data)=='c7ba1bfe81d7283c97e33c6a3449f729b63e79a916357aa7ea69fb7754709bc8'
old=pefile.PE(data=base);new=pefile.PE(data=data)
assert len(base)==111853568 and len(data)==111857664
assert len(old.sections)==34 and len(new.sections)==35
assert old.OPTIONAL_HEADER.AddressOfEntryPoint==new.OPTIONAL_HEADER.AddressOfEntryPoint
assert old.OPTIONAL_HEADER.ImageBase==new.OPTIONAL_HEADER.ImageBase==0x400000
assert new.OPTIONAL_HEADER.SizeOfImage==len(data)
assert new.OPTIONAL_HEADER.CheckSum==new.generate_checksum()
for a,b in zip(old.OPTIONAL_HEADER.DATA_DIRECTORY,new.OPTIONAL_HEADER.DATA_DIRECTORY):
    assert (a.VirtualAddress,a.Size)==(b.VirtualAddress,b.Size)
for a,b in zip(old.sections,new.sections):assert a.__pack__()==b.__pack__()
section=new.sections[-1]
assert section.Name.rstrip(b'\0')==b'.rface'
assert (section.VirtualAddress,section.PointerToRawData,section.Misc_VirtualSize,section.SizeOfRawData,section.Characteristics)==(len(base),len(base),28,4096,0x60000020)
assert not any(data[len(base)+28:])
site=new.get_offset_from_rva(0x1df054f-new.OPTIONAL_HEADER.ImageBase)
assert data[site]==0xe9 and data[site+5:site+9]==b'\x90'*4
dest=0x1df054f+5+struct.unpack_from('<i',data,site+1)[0]
assert dest==new.OPTIONAL_HEADER.ImageBase+section.VirtualAddress
assert base[site:site+9]==bytes.fromhex('5353518b8d6cffffff')
pe=struct.unpack_from('<I',base,0x3c)[0];opt=pe+24
ranges=sorted([(pe+6,pe+8),(opt+4,opt+8),(opt+56,opt+60),(opt+64,opt+68),(section.get_file_offset(),section.get_file_offset()+40),(site,site+9)])
cursor=0
for lo,hi in ranges:
    assert base[cursor:lo]==data[cursor:lo]
    cursor=hi
assert base[cursor:]==data[cursor:len(base)]
result=dict(passed=True,candidate_sha256=sha(data),size=len(data),sections=len(new.sections),entry_point_unchanged=True,directories_unchanged=True,existing_sections_unchanged=True,checksum_valid=True,only_declared_bytes_changed=True)
with args.output.open('x',encoding='utf8') as f:json.dump(result,f,indent=2)
print(json.dumps(result))
