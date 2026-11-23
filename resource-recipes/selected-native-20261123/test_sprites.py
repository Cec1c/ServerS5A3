"""Run both installed x86 code blocks up to the unchanged native Unlock, with guard bytes."""
import hashlib,json,random,struct,sys
from pathlib import Path
import pefile
root=Path(__file__).resolve().parent

from unicorn import Uc,UC_ARCH_X86,UC_MODE_32
from unicorn.x86_const import *
before=pefile.PE(sys.argv[1],fast_load=True)
after=pefile.PE(sys.argv[2],fast_load=True)
info={'ranges':[(0x387eb61,0x387ecef),(0x387ee0d,0x387ef9b)]}
pointer=0x65d14e8
def cpu(pe,begin,end):
    base=pe.OPTIONAL_HEADER.ImageBase
    u=Uc(UC_ARCH_X86,UC_MODE_32)
    for pg in {begin&~4095,(end-1)&~4095,0x45b9000,0x5016000,pointer&~4095}:
        u.mem_map(pg,4096);u.mem_write(pg,pe.get_data(pg-base,4096))
    u.mem_map(0x100000,0x3000)
    return u
regs=[UC_X86_REG_EAX,UC_X86_REG_EBX,UC_X86_REG_ECX,UC_X86_REG_EDX,UC_X86_REG_ESI,UC_X86_REG_EDI,UC_X86_REG_ESP,UC_X86_REG_EBP,UC_X86_REG_EFLAGS,UC_X86_REG_MXCSR]+[UC_X86_REG_XMM0+i for i in range(8)]
rng=random.Random(20261122)
for begin,end in info['ranges']:
    a,b=cpu(before,begin,end),cpu(after,begin,end)
    for case in range(1024):
        window=rng.randbytes(256);stack=rng.randbytes(128)
        dest=0x101020+case%32
        seeds=[rng.getrandbits(32) for i in range(6)]+[0x102300,0x102400,0x202,(0x1f80,0x9fc0,0x3f80,0x5f80)[case%4]]
        seeds.extend(rng.getrandbits(128) for i in range(8))
        for u in [a,b]:
            u.mem_write(0x101000,window);u.mem_write(0x102380,stack);u.mem_write(pointer,struct.pack('<I',dest))
            for reg,val in zip(regs,seeds):u.reg_write(reg,val)
            u.emu_start(begin,end,count=150)
            assert u.reg_read(UC_X86_REG_EIP)==end
            result=bytes(u.mem_read(0x101000,256))
            start=dest-0x101000
            assert result[:start]==window[:start] and result[start+112:]==window[start+112:]
        assert a.mem_read(dest,112)==b.mem_read(dest,112)
        assert all(a.reg_read(r)==b.reg_read(r) for r in regs)
record=dict(passed=True,paths=2,casesPerPath=1024,bytesPerQuad=112,mxcsrModes=4,registersAndGuardBytes=True)
Path(sys.argv[2]).with_suffix('.quad-checks.json').write_text(json.dumps(record,indent=2));print(record)
