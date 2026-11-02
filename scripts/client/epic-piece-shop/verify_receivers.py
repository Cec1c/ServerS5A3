"""Offline execution of the pinned client's epic-shop receivers; no game launch."""
import argparse
import hashlib
import json
import struct
from pathlib import Path

import pefile
from unicorn import Uc, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE
from unicorn.x86_const import UC_X86_REG_EAX, UC_X86_REG_ESP, UC_X86_REG_EIP


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--exe', type=Path, required=True)
    p.add_argument('--receipt', type=Path, required=True)
    args = p.parse_args()
    data = args.exe.read_bytes()
    digest = hashlib.sha256(data).hexdigest()
    assert digest == '5ce2ce3f01dacd05a6ce542d088911cc4fceb0f44ffa85dfa3a0886339e7cb5c'
    pe = pefile.PE(data=data)
    base = pe.OPTIONAL_HEADER.ImageBase
    u = Uc(UC_ARCH_X86, UC_MODE_32)
    u.mem_map(base, (pe.OPTIONAL_HEADER.SizeOfImage + 4095) // 4096 * 4096)
    u.mem_write(base, data[:pe.OPTIONAL_HEADER.SizeOfHeaders])
    for s in pe.sections:
        u.mem_write(base+s.VirtualAddress, s.get_data())
    for a, n in [(0,4096),(0x10000000,0x20000),(0x20000000,0x10000),(0x30000000,4096)]:
        u.mem_map(a,n)
    stack, stop, state, buffer = 0x10010000, 0x30000000, 0x20001000, 0x20003000
    calls = []
    def w(a, v): u.mem_write(a,struct.pack('<I',v & 0xffffffff))
    def rd(a): return struct.unpack('<I',u.mem_read(a,4))[0]
    def ret(pop=0, value=0):
        sp = u.reg_read(UC_X86_REG_ESP)
        target = rd(sp)
        u.reg_write(UC_X86_REG_EAX,value)
        u.reg_write(UC_X86_REG_ESP,sp+4+pop)
        u.reg_write(UC_X86_REG_EIP,target)
    def hook(uc, address, size, user):
        sp = u.reg_read(UC_X86_REG_ESP)
        if address == stop: u.emu_stop(); return
        if address == 0x104fad0: ret(value=state); return
        if address == 0x32b5c90: ret(8); return
        if address == 0x32b55e0: ret(4); return
        if address == 0x3e73cb6: ret(); return
        if address == 0x37e63b0: ret(value=0); return
        if address == 0x37de240: ret(value=0x20005000); return
        if address == 0x37dd6c0: ret(); return
        if address == 0x104fda0:
            calls.append(dict(call='spawn_boundary',enabled=bool(u.mem_read(state+0x20,1)[0])))
            ret(value=1); return
        if address == 0x32bea50:
            calls.append(dict(call='open_window',window=rd(sp+4),argument=rd(sp+12)))
            ret(12); return
        if address == 0x32b94a0:
            calls.append(dict(call='close_window',window=rd(sp+4)))
            ret(12); return
    u.hook_add(UC_HOOK_CODE,hook)
    def run(address, body=b'', status=1):
        calls.clear()
        u.mem_write(buffer,body or b'\0')
        w(0x57f9ad0,buffer); w(0x57f9ad4,len(body))
        w(stack,stop); w(stack+4,0); w(stack+8,status); w(stack+12,19)
        u.reg_write(UC_X86_REG_ESP,stack)
        u.emu_start(address,0,count=20000)
        assert u.reg_read(UC_X86_REG_EIP)==stop,hex(u.reg_read(UC_X86_REG_EIP))
        assert u.reg_read(UC_X86_REG_ESP)==stack+4
        assert rd(0x57f9ad4)==0, (address,rd(0x57f9ad4))
    rows=[]
    for flag in (0,1):
        u.mem_write(state,b'\x44'*0x28)
        run(0x1feaef0,bytes([flag]))
        assert all(rd(state+i*4)==0xffffffff for i in range(7))
        assert rd(state+0x1c)==0 and u.mem_read(state+0x20,2)==bytes([flag,0])
        assert calls==[dict(call='spawn_boundary',enabled=bool(flag))]
        rows.append(dict(receiver='744',flag=flag,passed=True))
    pieces=[10300001,10300002,10300003,10300004,10300005]
    body=bytes([5])+b''.join(struct.pack('<II',x,10) for x in pieces)
    run(0x104fb80,body)
    assert sorted(rd(state+i*4) for i in range(5))==pieces
    rows.append(dict(receiver='745',rows=5,bytes_consumed=len(body),piece_ids_preserved=True,passed=True))
    for status in (0,1):
        w(state+0x1c,2)
        run(0x104fc40,status=status)
        assert rd(state+0x1c)==2+status
        rows.append(dict(receiver='817',status=status,refreshes_after=2+status,passed=True))
    for status in (0,1):
        w(state+0x14,5001); u.mem_write(state+0x21,b'\0')
        run(0x104fc90,status=status)
        assert u.mem_read(state+0x21,1)==bytes([status])
        assert (len(calls)==3)==bool(status)
        if status: assert calls==[dict(call='open_window',window=0x44d,argument=5001),dict(call='close_window',window=0x44c),dict(call='close_window',window=0x44f)]
        rows.append(dict(receiver='818',status=status,calls=list(calls),passed=True))
    result=dict(passed=True,sha256=digest,cases=rows,
        scope='Native 744/745/817/818 bodies, actual packet readers, state reset, material IDs, refresh count and completion flag. Singleton lookup, RNG, GUI lookup/RTTI/open/close, logger/string helper and final NPC spawn boundary are explicit stubs. Common status decoding and full dispatcher are not executed.',
        game_acceptance=False)
    with args.receipt.open('x',encoding='utf8') as f: json.dump(result,f,indent=2)
    print(json.dumps(dict(passed=True,cases=len(rows),sha256=digest)))


if __name__=='__main__': main()
