"""Read-only replay of the current item-effect packet and native time conversion.

The packet readers and row loop execute original instructions. Item allocation,
destruction and the effect call are isolated fixture boundaries; this does not
claim rendered UI validation. No client image or production packet is included.
"""
import argparse
import hashlib
import json
from pathlib import Path
import struct

import pefile
from unicorn import Uc, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE
from unicorn.x86_const import UC_X86_REG_EAX, UC_X86_REG_EBP, UC_X86_REG_EBX, UC_X86_REG_ECX, UC_X86_REG_EDX, UC_X86_REG_EDI, UC_X86_REG_ESI, UC_X86_REG_ESP, UC_X86_REG_EIP, UC_X86_REG_EFLAGS

CLIENT_SHA = "eca5ae9017176fb159ae22d6498b2a6805b8b93295042363a90f37f2daae7a03"
BP, PACKET, OBJECT, VTABLE = 0x20010000, 0x20020000, 0x20030000, 0x20031000
CURSOR = 0x57f9ad0


def run(path):
    raw = Path(path).read_bytes()
    assert hashlib.sha256(raw).hexdigest() == CLIENT_SHA, "client identity"
    pe = pefile.PE(data=raw, fast_load=True)
    read = lambda va,n: pe.get_data(va-0x400000,n)
    assert read(0x2031cfc,12).hex() == "6a00688097020268ae000000", "registered op174 reader"
    vm = Uc(UC_ARCH_X86, UC_MODE_32)
    vm.mem_map(0x400000,(pe.OPTIONAL_HEADER.SizeOfImage+4095)&~4095)
    vm.mem_write(0x400000,pe.get_memory_mapped_image())
    vm.mem_map(0x20000000,0x50000)
    rd=lambda a: struct.unpack("<I",vm.mem_read(a,4))[0]
    def wr(a,*v): vm.mem_write(a,struct.pack("<"+"I"*len(v),*v))
    def ret(value=0,clean=0):
        sp=vm.reg_read(UC_X86_REG_ESP)
        target=rd(sp)
        vm.reg_write(UC_X86_REG_EAX,value)
        vm.reg_write(UC_X86_REG_ESP,sp+4+clean)
        vm.reg_write(UC_X86_REG_EIP,target)
    observed=[]; item=[0]
    def hook(vm,addr,size,data):
        if addr in (0x242c910,0x1c6d840,0x2430fb0,0x985650): ret(OBJECT)
        elif addr==0x2a32b30:
            item[0]=rd(vm.reg_read(UC_X86_REG_ESP)+4);ret(OBJECT)
        elif addr==0x20032000: ret(1,4)
        elif addr==0x20032010:
            observed.append([item[0],rd(vm.reg_read(UC_X86_REG_ESP)+4)]);ret(0,4)
        elif addr==0x202988d: vm.reg_write(UC_X86_REG_EIP,0x2029953)
        elif addr==0x20032020: ret(0,4)
    vm.hook_add(UC_HOOK_CODE,hook)
    wr(OBJECT,VTABLE);wr(VTABLE+0x2c,0x20032000);wr(VTABLE+0xc0,0x20032010);wr(VTABLE+0xc,0x20032020)
    reports=[]
    for rows in ([],[[7440,900]],[[7440,417],[10304122,180]],[[7440,0],[7441,900]],[[7440,2147483]]):
        for alignment in range(4):
            observed.clear()
            body=bytes([len(rows)])+b"".join(struct.pack("<II",*row) for row in rows)
            ptr=PACKET+alignment;vm.mem_write(ptr,body+bytes(32));wr(CURSOR,ptr,len(body),0,0)
            for reg,val in ((UC_X86_REG_EBP,BP),(UC_X86_REG_ESP,BP-0x2000),(UC_X86_REG_EFLAGS,0x202)):vm.reg_write(reg,val)
            vm.emu_start(0x20297ad,0x202996e,count=200000)
            assert observed==rows,(rows,observed)
            assert rd(CURSOR)-ptr==len(body) and rd(CURSOR+4)==0,"complete payload"
            assert vm.reg_read(UC_X86_REG_ESP)==BP-0x2000,"row stack balance"
            reports.append(dict(rows=rows,alignment=alignment,consumed=len(body)))
    conversions=[]
    for seconds in (0,1,417,900,2147483):
        wr(OBJECT+0x4c4,0)
        wr(BP-0x1018,seconds)
        for reg,val in ((UC_X86_REG_EBP,BP),(UC_X86_REG_EBX,OBJECT),(UC_X86_REG_ESP,BP-0x2000),(UC_X86_REG_EDI,0),(UC_X86_REG_EAX,0),(UC_X86_REG_ECX,0),(UC_X86_REG_ESI,0)):
            vm.reg_write(reg,val)
        vm.reg_write(UC_X86_REG_EDX,seconds)
        vm.emu_start(0x25e453c,0x25e4576,count=1000)
        assert vm.reg_read(UC_X86_REG_EDI)==seconds*1000
        conversions.append(dict(seconds=seconds,milliseconds=seconds*1000))
    # Variant 1 changes destruction/drawing; all other virtual methods up to
    # the end of this vtable, including use/effect handlers, stay identical.
    differences=[]
    for offset in range(0,0x158,4):
        a,b=read(0x49d1444+offset,4),read(0x49f60ec+offset,4)
        if a!=b:differences.append(offset)
    assert differences==[0xc,0x88,0xb4],differences
    return dict(passed=True,clientSha256=CLIENT_SHA,packetCases=reports,timeConversions=conversions,etcVariantMethodDifferences=differences,renderedGameTest=False)


if __name__ == "__main__":
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--client",required=True)
    parser.add_argument("--output",required=True)
    args=parser.parse_args()
    result=run(args.client)
    with open(args.output,"x",encoding="utf8") as out:json.dump(result,out,indent=2)
    print("native item effect replay passed",len(result["packetCases"]),len(result["timeConversions"]))
