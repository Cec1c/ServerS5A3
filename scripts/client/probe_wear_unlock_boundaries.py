"""Execute the patched guard over every byte-valued source/destination family."""
import argparse,json,struct
from pathlib import Path
import patch_wear_unlock_v132 as patch


def run(image):
    from unicorn import Uc,UC_ARCH_X86,UC_MODE_32
    from unicorn.x86_const import (UC_X86_REG_EAX,UC_X86_REG_EBX,UC_X86_REG_ECX,
        UC_X86_REG_EDX,UC_X86_REG_ESI,UC_X86_REG_EDI,UC_X86_REG_EBP,UC_X86_REG_ESP,
        UC_X86_REG_EFLAGS,UC_X86_REG_EIP)
    u=Uc(UC_ARCH_X86,UC_MODE_32)
    opt,sections=patch.pe.headers(image)
    size=struct.unpack_from('<I',image,opt+56)[0]
    u.mem_map(0x400000,(size+4095)//4096*4096)
    for va,n,raw in sections:
        if n:u.mem_write(va,image[raw:raw+n])
    u.mem_map(0x10000000,0x40000)
    u.mem_map(0x20000000,0x10000)
    objects=[0x20001000,0x20002000]
    vectors=[0x20003000,0x20004000]
    def put(a,v):u.mem_write(a,struct.pack('<I',v))
    put(0x56a379c,objects[0]);put(0x56a37b0,objects[1])
    regs=[UC_X86_REG_EAX,UC_X86_REG_EBX,UC_X86_REG_ECX,UC_X86_REG_EDX,UC_X86_REG_ESI,UC_X86_REG_EDI,UC_X86_REG_EBP,UC_X86_REG_ESP]
    initial=[0x11111111,0x22222222,0x33333333,0x44444444,0x55555555,0x66666666,0x10020000,0x10010000]
    cases=[(src,3,0) for src in range(256)]+[(0,dst,0) for dst in range(256)]
    cases += [(src,3,count) for src in (0,1) for count in (1,2,0x7fffffff,0x80000000,0xffffffff)]
    for src,dst,count in cases:
        for reg,value in zip(regs,initial):u.reg_write(reg,value)
        u.reg_write(UC_X86_REG_EFLAGS,0x246)
        for obj,vec in zip(objects,vectors):
            put(obj+0x6c,vec);put(obj+0x70,vec+4);put(vec,7)
        put(initial[6]-0xf4,count)
        u.mem_write(initial[6]-0xe0,bytes([src]));u.mem_write(initial[6]-0xd5,bytes([dst]))
        match=count==0 and src<=1 and dst==3
        target=patch.FINISH if match else patch.CONTINUE
        u.emu_start(patch.SITE,target,count=200)
        assert u.reg_read(UC_X86_REG_EIP)==target
        expected=initial[:]
        if match:expected[-1]+=28
        else:expected[3]=count
        assert [u.reg_read(r) for r in regs]==expected,(src,dst,count)
        assert u.reg_read(UC_X86_REG_EFLAGS)==0x246
        for i,(obj,vec) in enumerate(zip(objects,vectors)):
            end=struct.unpack('<I',u.mem_read(obj+0x70,4))[0]
            assert end==vec+(0 if match and i==src else 4)
            assert struct.unpack('<I',u.mem_read(vec,4))[0]==7
    return dict(passed=True,cases=len(cases),all_byte_source_and_destination_values=True,
                fallback_registers_flags_and_stack_preserved=True,receive_gates_executed=False)


if __name__=='__main__':
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--image',type=Path,required=True);p.add_argument('--output',type=Path,required=True)
    a=p.parse_args();data=a.image.read_bytes()
    assert patch.pe.sha(data)=='29a3f7f141841483f492e63834aac6c3f0eeb50baf373c0100ba6a3e33ec32b8'
    r=run(data);r['image_sha256']=patch.pe.sha(data)
    a.output.write_text(json.dumps(r,indent=2),encoding='utf-8');print(json.dumps(r))
