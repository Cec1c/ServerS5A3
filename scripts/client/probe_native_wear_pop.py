"""Execute the native protected wear helper through its early state cleanup.

No external call is stubbed or executed. Stop before the first actor accessor.
This establishes the normal helper's early pop, not the full equipment action.
"""
import argparse
import json
from pathlib import Path
import struct
import patch_receivers_v128 as pe


def probe(image, states):
    from unicorn import Uc, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE, UC_HOOK_MEM_WRITE
    from unicorn.x86_const import UC_X86_REG_ESP, UC_X86_REG_ECX, UC_X86_REG_EIP
    opt, sections = pe.headers(image)
    u = Uc(UC_ARCH_X86, UC_MODE_32)
    size = struct.unpack_from('<I', image, opt+56)[0]
    u.mem_map(0x400000,(size+4095)//4096*4096)
    for va,n,raw in sections:
        if n: u.mem_write(va,image[raw:raw+n])
    for a,n in [(0,4096),(0x10000000,0x40000),(0x20000000,4096),(0x30000000,0x10000)]:
        u.mem_map(a,n)
    obj,vec,sp = 0x30001000,0x30002000,0x10030000
    u.mem_write(obj+0x6c,struct.pack('<II',vec,vec+4*len(states)))
    if states: u.mem_write(vec,struct.pack('<'+'I'*len(states),*states))
    u.mem_write(sp,struct.pack('<III',0x20000000,49,12))
    u.reg_write(UC_X86_REG_ESP,sp)
    u.reg_write(UC_X86_REG_ECX,obj)
    trace,writes = [],[]
    def code(u,address,n,ctx):
        trace.append(hex(address))
        if address == 0x29610e0:
            u.emu_stop()
        elif address < 0x6800000 and address != 0x25f0e80:
            raise AssertionError('unexpected external call before cleanup: '+hex(address))
    def write(u,access,address,n,value,ctx):
        if obj <= address < obj+0x100:
            writes.append(dict(pc=hex(u.reg_read(UC_X86_REG_EIP)),offset=address-obj,size=n,value=value))
    u.hook_add(UC_HOOK_CODE,code)
    u.hook_add(UC_HOOK_MEM_WRITE,write)
    u.emu_start(0x25f0e80,0x20000000,count=10000)
    assert u.reg_read(UC_X86_REG_EIP) == (0x29610e0 if states else 0x20000000)
    end = struct.unpack('<I',u.mem_read(obj+0x70,4))[0]
    assert end == vec+4*max(0,len(states)-1)
    expected = [dict(pc='0x69663d7',offset=0x70,size=4,value=end)] if states else []
    assert writes == expected
    result = list(struct.unpack('<'+'I'*((end-vec)//4),u.mem_read(vec,end-vec))) if end>vec else []
    return dict(before=states,after=result,trace=trace,writes=writes,external_calls_executed=0)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--image',type=Path,required=True)
    parser.add_argument('--output',type=Path,required=True)
    args = parser.parse_args()
    image = args.image.read_bytes()
    assert pe.sha(image) in ('153903d7e76c9e2feed6fd19c053742365f04484d6dadcf24b78d5b58daf0391',
                            '29a3f7f141841483f492e63834aac6c3f0eeb50baf373c0100ba6a3e33ec32b8')
    cases = [probe(image,s) for s in ([],[7],[4],[4,7],[7,4],[7,7])]
    report = dict(passed=True,image_sha256=pe.sha(image),cases=cases,full_ui_acceptance=False)
    args.output.write_text(json.dumps(report,indent=2),encoding='utf-8')
    print(json.dumps(dict(passed=True,cases=len(cases),image_sha256=report['image_sha256'])))
