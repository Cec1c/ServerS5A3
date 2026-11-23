import struct
import pefile
from capstone import Cs, CS_ARCH_X86, CS_MODE_32

def build(source):
    p=pefile.PE(data=source,fast_load=True)
    base=p.OPTIONAL_HEADER.ImageBase
    cs=Cs(CS_ARCH_X86,CS_MODE_32)
    ranges=[(0x387eb61,0x387ecef),(0x387ee0d,0x387ef9b)]
    def read(a,n):return p.get_data(a-base,n)
    # Verify both stock success paths enter the same writer. The common failure
    # target and the unlock routines are outside the replacements.
    a,b=ranges[0]
    stock=read(a,b-a)
    assert len(stock)==398 and read(ranges[1][0],398)==stock
    assert all(read(a-8,4)==bytes.fromhex('85c00f85') and read(b,5)==bytes.fromhex('a1ac0b5906') for a,b in ranges)
    fun_start,fun_end=0x387e790,0x387f050
    for i in cs.disasm(read(fun_start,fun_end-fun_start),fun_start):
        if i.mnemonic.startswith('j') and i.op_str.startswith('0x'):
            target=int(i.op_str,16)
            assert not any(a<target<b for a,b in ranges),hex(target)
    cs.detail=True
    inputs=0
    cached=set()
    body=bytearray()
    for i in cs.disasm(stock,ranges[0][0]):
        assert i.mnemonic in ('mov','subss','movss'),i.mnemonic
        dst,src=i.op_str.split(', ')
        if src=='dword ptr [0x65d14e8]':
            inputs+=1
            if dst not in cached:
                if not cached:assert dst=='ecx'
                body.extend({'ecx':i.bytes,'edx':b'\x89\xca','eax':b'\x89\xc8'}[dst])
                cached.add(dst)
        else:
            assert not cached.intersection(i.reg_name(r) for r in i.regs_access()[1])
            body.extend(i.bytes)
    assert inputs==28 and len(body)==249 and cached=={'eax','ecx','edx'}
    candidate=bytearray(source)
    for a,b in ranges:
        at=p.get_offset_from_rva(a-base)
        jump=b'\xe9'+struct.pack('<i',b-(a+len(body)+5))
        candidate[at:at+b-a]=(body+jump).ljust(b-a,b'\x90')
    checksum=p.OPTIONAL_HEADER.get_field_absolute_offset('CheckSum')
    struct.pack_into('<I',candidate,checksum,0)
    struct.pack_into('<I',candidate,checksum,pefile.PE(data=candidate,fast_load=True).generate_checksum())
    v=pefile.PE(data=candidate,fast_load=True)
    assert v.verify_checksum()
    restored=bytearray(candidate)
    for a,b in ranges:
        at=p.get_offset_from_rva(a-base)
        restored[at:at+b-a]=stock
    restored[checksum:checksum+4]=source[checksum:checksum+4]
    assert restored==source
    return bytes(candidate)
