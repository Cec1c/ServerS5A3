"""Execute the complete native Buff row consumer with explicit leaf fixtures."""
from pathlib import Path
import argparse
import itertools
import json
import struct
import pefile
from unicorn import Uc, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE
from unicorn.x86_const import *
from build_candidate import build, HELPER, helper


def replay(data, local, busy, present, rows, repeats=1):
    pe = pefile.PE(data=data, fast_load=True)
    u = Uc(UC_ARCH_X86, UC_MODE_32)
    pages = set()
    def put(va, value):
        for page in range(va & ~4095, (va+len(value)+4095) & ~4095, 4096):
            if page not in pages:
                u.mem_map(page, 4096)
                pages.add(page)
        u.mem_write(va, value)
    def word(va, value):
        put(va, struct.pack("<I", value))
    def read(va):
        return struct.unpack("<I", u.mem_read(va, 4))[0]
    for va, size in [(0x22C35E0,0x501),(0x2531A80,0x110),(0x25EA310,7),(0x29610E0,15),(HELPER,len(helper()))]:
        put(va, pe.get_data(va-pe.OPTIONAL_HEADER.ImageBase,size))
    put(0, bytes(4096))
    put(0x10000000, bytes(0x10000))
    put(0x11000000, bytes(0x10000))
    stop, actor, localActor, bag, item, vtable = 0x12000000,0x11000000,0x11001000,0x11002000,0x11003000,0x11004000
    put(stop,b"\x90")
    if local:
        localActor = actor
    word(0x57B6FB4,0)
    word(0x57B6FB0,localActor)
    word(0x56A379C,bag if present else 0)
    put(bag+0xCC,bytes([busy]))
    word(item,vtable)
    for off,fn in [(0,0x12001000),(0x114,0x12001010)]:
        word(vtable+off,fn)
    stream=struct.pack("<HB",14,rows)
    for n in range(rows):
        entry=struct.pack("<HII",54+n,101040493+n,1)+bytes(109)
        stream += struct.pack("<BBH",12+n,0,54+n)+entry
    cursor=0
    applied=[]
    cleared=[]
    selected=[]
    allocated=0x11005000
    def finish(clean=0, value=None):
        sp=u.reg_read(UC_X86_REG_ESP)
        dest=read(sp)
        u.reg_write(UC_X86_REG_ESP,sp+4+clean)
        if value is not None:
            u.reg_write(UC_X86_REG_EAX,value)
        u.reg_write(UC_X86_REG_EIP,dest)
    def hook(uc,addr,size,_):
        nonlocal cursor
        sp=uc.reg_read(UC_X86_REG_ESP)
        ecx=uc.reg_read(UC_X86_REG_ECX)
        if addr==0x37DE240: finish(value=0x11006000)
        elif addr in [0x37DD6C0,0x3E70882,0x3E72CE4]: finish()
        elif addr==0x9982B0: finish(value=0x11007000)
        elif addr==0x997C30:
            cleared.append(read(sp+4)); finish(4)
        elif addr==0x998530:
            assert read(sp+8)==0 and read(sp+16)==0
            selected.append((read(sp+4),read(sp+12)));finish(16)
        elif addr in [0x37E8350,0x37E82E0,0x37E8430]:
            count=2 if addr==0x37E8350 else read(sp+8)
            if cursor+count>len(stream):
                raise AssertionError("native reader overrun")
            put(read(sp+4),stream[cursor:cursor+count]);cursor+=count;finish()
        elif addr==0x8A9480: finish(value=allocated)
        elif addr==0x985DF0:
            put(ecx,bytes(119));finish(value=ecx)
        elif addr in [0x3B50770,0x3B507A0,0x994E00]:
            put(ecx,bytes({0x3B50770:20,0x3B507A0:4,0x994E00:8}[addr]));finish(value=ecx)
        elif addr==0x1FA2790:finish(value=0x11008000)
        elif addr in [0x251E2A0,0x251E2F0]:finish(value=0)
        elif addr==0x251E650:
            put(read(sp+4),bytes(20));finish()
        elif addr==0x242D4A0:finish()
        elif addr==0x2436FE0:
            put(ecx,bytes(0x78));finish(4,value=ecx)
        elif addr==0x2A32B30:finish(value=item)
        elif addr==0x25EB680:finish(4,value=item)
        elif addr==0x25F04E0:finish(16)
        elif addr==0x985650:finish()
        elif addr==0x12001000:finish(value=5)
        elif addr==0x1D444F0:finish(4)
        elif addr==0x12001010:finish(4)
        elif addr==0x9983F0:
            applied.append((read(sp+4),read(sp+8),read(sp+12),read(sp+16),read(sp+20),read(sp+24),read(sp+28)))
            finish(28)
        elif addr in [0x3E738B1,0x3E736A4]:raise AssertionError("native error path")
    leaves=[0x37DE240,0x37DD6C0,0x3E70882,0x3E72CE4,0x9982B0,0x997C30,0x998530,
            0x37E8350,0x37E82E0,0x37E8430,0x8A9480,0x985DF0,0x3B50770,0x3B507A0,
            0x994E00,0x1FA2790,0x251E2A0,0x251E2F0,0x251E650,0x242D4A0,0x2436FE0,
            0x2A32B30,0x25EB680,0x25F04E0,0x985650,0x12001000,0x1D444F0,0x12001010,0x9983F0,0x3E738B1,0x3E736A4]
    for va in leaves:put(va,b"\xc3")
    word(0x55B3BDC,0x11223344)
    sp=0x1000F000
    word(sp,stop);word(sp+4,actor)
    sentinels={UC_X86_REG_EBX:0x44332211,UC_X86_REG_ESI:0x55667788,UC_X86_REG_EDI:0x88776655,UC_X86_REG_EBP:0x1000FF00}
    for reg,value in sentinels.items():u.reg_write(reg,value)
    u.reg_write(UC_X86_REG_ESP,sp)
    u.hook_add(UC_HOOK_CODE,hook)
    for _ in range(repeats):
        cursor=0
        word(sp,stop);word(sp+4,actor)
        u.reg_write(UC_X86_REG_ESP,sp)
        u.emu_start(0x22C35E0,stop,count=20000)
        assert cursor==len(stream) and u.reg_read(UC_X86_REG_ESP)==sp+4
        assert all(u.reg_read(reg)==value for reg,value in sentinels.items())
    assert cleared==[actor]*repeats and selected==[(actor,14)]*repeats,(cleared,selected,local,busy,present,rows)
    for n,row in enumerate(applied):
        assert row==(0,actor,0,0,54+n%rows,item,0),row
    return len(applied)


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input",type=Path,required=True)
    parser.add_argument("--candidate",type=Path,required=True)
    parser.add_argument("--output",type=Path,required=True)
    args=parser.parse_args()
    old=args.input.read_bytes();new=args.candidate.read_bytes()
    expected,receipt=build(old)
    assert new==expected
    for bad in (old[:-1], old[:-1]+bytes([old[-1]^1]), new):
        try: build(bad)
        except ValueError: pass
        else: raise AssertionError("unsupported input accepted")
    cases=[]
    for local,busy,present,rows in itertools.product([False,True],[0,1],[False,True],[0,1,2]):
        before=replay(old,local,busy,present,rows)
        after=replay(new,local,busy,present,rows)
        assert before==(0 if present and busy else rows)
        assert after==(0 if local and present and busy else rows)
        cases.append(dict(localOwner=local,busy=busy,bagPresent=present,rows=rows,before=before,after=after))
    repeated=[]
    for local,busy in itertools.product([False,True],[0,1]):
        before=replay(old,local,busy,True,2,repeats=2)
        after=replay(new,local,busy,True,2,repeats=2)
        assert before==(0 if busy else 4)
        assert after==(0 if local and busy else 4)
        repeated.append(dict(localOwner=local,busy=busy,repeats=2,before=before,after=after))
    result=dict(passed=True,cases=len(cases),executions=2*len(cases),build=receipt,rows=cases,
        fixtures=["packet readers", "item factory and item vtable leaves", "Buff manager updates", "allocation", "UI/debug leaves"],
        nativeExecuted=["complete 22C35E0 consumer", "2531A80 bag selector", "25EA310 busy getter", "29610E0 local actor getter", "candidate helper"],
        repeated=repeated,unsupportedInputsRejected=3,
        guiValidated=False,incidentBusyFlagObserved=False)
    with args.output.open("x",encoding="utf-8") as f:json.dump(result,f,indent=2)
    print(json.dumps({k:v for k,v in result.items() if k not in ["rows","build"]}))


if __name__=="__main__":main()
