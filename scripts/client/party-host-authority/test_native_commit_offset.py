#!/usr/bin/env python3
"""Execute fixed EXE's NOTI9 manager commit slice, without a Python offset model.

Runs 0x2018090 through the native 0xB39D20 and 0x2974CD0 store at
0x2974CDF. Stops before unrelated downstream UI calls. Synthetic addresses
and distinct +0x48/+0x4C sentinels make the offset result observable.
"""
import argparse
import hashlib
import json
from pathlib import Path
import struct
import sys

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--input", type=Path, required=True)
parser.add_argument("--deps", type=Path, required=True)
parser.add_argument("--output", type=Path, required=True)
args = parser.parse_args()
sys.path.insert(0,str(args.deps.resolve()))
import pefile
from unicorn import Uc, UC_ARCH_X86, UC_MODE_32
from unicorn.x86_const import (UC_X86_REG_EAX,UC_X86_REG_ECX,
                                UC_X86_REG_EDX,UC_X86_REG_EBP,UC_X86_REG_EIP,
                                UC_X86_REG_ESP)

INPUT_SHA = "0e4a99cb278bb4ec37fc90c027903417eb2f790915941000538fcf2209b81872"
data=args.input.read_bytes()
assert hashlib.sha256(data).hexdigest()==INPUT_SHA
pe=pefile.PE(data=data,fast_load=True)
base=pe.OPTIONAL_HEADER.ImageBase
assert pe.get_data(0xB39D20-base,4)==bytes.fromhex("8d4104c3")
assert pe.get_data(0x201809C-base,8)==bytes.fromhex("e87f1cb2fe8b5048")
assert pe.get_data(0x2974CDF-base,3)==bytes.fromhex("897768")
rows=[]
for own_slot,leader_slot in ((1,3),(5,2),(0,7)):
    uc=Uc(UC_ARCH_X86,UC_MODE_32)
    for address,size in ((0x2018000,0x1000),(0xB39000,0x1000),
                         (0x2974000,0x1000),(0x56A3000,0x1000),
                         (0x700000,0x1000),(0x800000,0x1000),
                         (0x900000,0x1000)):
        uc.mem_map(address,size)
    for va,n in ((0x2018090,0x25),(0xB39D20,4),(0x2974CD0,0x20)):
        uc.mem_write(va,pe.get_data(va-base,n))
    record=0x700000; frame=0x800800; manager=0x900000
    uc.mem_write(record+0x48,struct.pack("<I",own_slot))
    uc.mem_write(record+0x4C,struct.pack("<I",leader_slot))
    uc.mem_write(frame-0x398,struct.pack("<I",record))
    uc.mem_write(0x56A3768,struct.pack("<I",manager))
    uc.mem_write(manager+0x68,struct.pack("<I",0xDEADBEEF))
    uc.reg_write(UC_X86_REG_EBP,frame)
    uc.reg_write(UC_X86_REG_ESP,0x800F00)
    uc.reg_write(UC_X86_REG_EAX,0xABCD1234)
    uc.emu_start(0x2018090,0x2974CE2,count=100)
    assert uc.reg_read(UC_X86_REG_EIP)==0x2974CE2
    committed=struct.unpack("<I",uc.mem_read(manager+0x68,4))[0]
    assert committed==leader_slot and committed!=own_slot
    assert uc.reg_read(UC_X86_REG_EDX)==leader_slot
    rows.append({"synthetic_own_slot_record_plus48":own_slot,
                 "synthetic_leader_slot_record_plus4c":leader_slot,
                 "observed_manager_plus68":committed,
                 "native_data_pointer":hex(record+4)})
result={"input_sha256":INPUT_SHA,
        "native_slice":"0x2018090 -> 0xB39D20 -> 0x20180A1 -> 0x2974CDF",
        "stopped_after_manager_store":"0x2974CE2",
        "rows":rows,"pass":"manager_plus68_commits_record_plus4c"}
with args.output.open("x",encoding="utf-8") as stream:
    json.dump(result,stream,indent=2)
    stream.write("\n")
print(json.dumps(result))
