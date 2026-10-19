"""Exact patch bounds and native caller/helper ABI matrix (UI not executed)."""
import argparse,ctypes,hashlib,json,struct
from pathlib import Path
import patch_party_clear_context_v140 as patch
from unicorn import Uc,UC_ARCH_X86,UC_MODE_32,UC_HOOK_CODE
from unicorn.x86_const import *

def validate(parent,candidate,receipt):
    expected,r=patch.build(parent)
    assert candidate==expected and receipt==r
    opt,sections=patch.pe.headers(candidate)
    allowed=set(range(r['patch_offset'],r['patch_offset']+5))
    allowed.update(range(r['helper_file_offset'],r['helper_file_offset']+r['helper_size']))
    allowed.update(range(r['section_virtual_size_offset'],r['section_virtual_size_offset']+4))
    allowed.update(range(r['checksum_offset'],r['checksum_offset']+4))
    assert all(a==b or i in allowed for i,(a,b) in enumerate(zip(parent,candidate)))
    try:patch.build(candidate);raise AssertionError('second patch allowed')
    except ValueError:pass
    helper_va=int(r['helper_va'],16);entry=0x2018b7b;continuation=0x2018b97
    uc=Uc(UC_ARCH_X86,UC_MODE_32)
    for address,length in ((entry,0x100),(helper_va,r['helper_size'])):
        page=address&~4095;uc.mem_map(page,4096)
        offset=patch.pe.offset(sections,page,4096);uc.mem_write(page,candidate[offset:offset+4096])
    # The callee is intercepted before any native mutation. All caller/helper
    # instructions execute as packaged. Only callee behavior is a fixture.
    uc.mem_map(patch.REMOVE_TARGET&~4095,8192)
    uc.mem_write(patch.REMOVE_TARGET,b'\xc3')
    uc.mem_map(0x7e000000,0x20000);uc.mem_map(0x56a3000,4096)
    manager=0x13579bdf;uc.mem_write(0x56a3768,struct.pack('<I',manager))
    bp=0x7e018000;sp=0x7e008000;observed={}
    def dw(a):return struct.unpack('<I',uc.mem_read(a,4))[0]
    def hook(uc,pc,n,_):
        if pc==patch.REMOVE_TARGET:
            top=uc.reg_read(UC_X86_REG_ESP)
            observed.update(context=dw(top+4),key=dw(top+8),manager=uc.reg_read(UC_X86_REG_ECX))
            uc.reg_write(UC_X86_REG_EIP,dw(top));uc.reg_write(UC_X86_REG_ESP,top+12)
        elif pc==continuation:uc.emu_stop()
    uc.hook_add(UC_HOOK_CODE,hook)
    cases=0
    for route in range(256):
        for owner in range(256):
            observed.clear();key=((route*257+owner)&65535) or 1
            uc.mem_write(bp-0x351,b'\x03');uc.mem_write(bp-0x38e,bytes([route]));uc.mem_write(bp-0x38a,bytes([owner]))
            uc.mem_write(bp-0x3a0,struct.pack('<I',key))
            for reg,value in ((UC_X86_REG_EBP,bp),(UC_X86_REG_ESP,sp),(UC_X86_REG_EBX,0x11223344),(UC_X86_REG_ESI,0x55667788),(UC_X86_REG_EDI,0x12345678)):
                uc.reg_write(reg,value)
            try:uc.emu_start(entry,continuation,count=50)
            except Exception as exc:raise AssertionError((route,owner,hex(uc.reg_read(UC_X86_REG_EIP)),observed)) from exc
            assert observed==dict(context=(route<<16)|owner,key=key,manager=manager)
            assert uc.reg_read(UC_X86_REG_ESP)==sp and uc.reg_read(UC_X86_REG_EBP)==bp
            assert [uc.reg_read(x) for x in (UC_X86_REG_EBX,UC_X86_REG_ESI,UC_X86_REG_EDI)]==[0x11223344,0x55667788,0x12345678]
            cases+=1
    for kind in (0,1,2,4,5,6):
        observed.clear();uc.mem_write(bp-0x351,bytes([kind]));uc.reg_write(UC_X86_REG_ESP,sp)
        uc.emu_start(entry,continuation,count=10)
        assert not observed and uc.reg_read(UC_X86_REG_ESP)==sp;cases+=1
    # Independently ask Windows ImageHlp to verify the finished PE checksum.
    imagehlp=ctypes.WinDLL('imagehlp');fn=imagehlp.CheckSumMappedFile
    fn.argtypes=[ctypes.c_void_p,ctypes.c_uint32,ctypes.POINTER(ctypes.c_uint32),ctypes.POINTER(ctypes.c_uint32)];fn.restype=ctypes.c_void_p
    buf=ctypes.create_string_buffer(candidate);stored=ctypes.c_uint32();computed=ctypes.c_uint32()
    assert fn(buf,len(candidate),ctypes.byref(stored),ctypes.byref(computed)) and stored.value==computed.value
    return dict(passed=True,native_executions=cases,all_u8_context_pairs=True,non_removal_kinds_unchanged=True,
        callee_saved_registers_preserved=True,stack_balanced=True,exact_delta_only=True,windows_checksum_valid=True,
        input_sha256=patch.INPUT_SHA256,output_sha256=r['output_sha256'],full_ui_acceptance=False,
        limitations=['Native caller and helper execute; erase callee is a fixture. Does not test renderer, portraits, or actual UI menu.'])

def main():
    p=argparse.ArgumentParser();p.add_argument('parent',type=Path);p.add_argument('candidate',type=Path);p.add_argument('--receipt',type=Path,required=True);p.add_argument('--output',type=Path,required=True);a=p.parse_args()
    result=validate(a.parent.read_bytes(),a.candidate.read_bytes(),json.loads(a.receipt.read_text()))
    a.output.write_text(json.dumps(result,indent=2),encoding='utf-8');print(json.dumps(result))

if __name__=='__main__':main()
