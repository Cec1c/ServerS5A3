import struct
import pefile
from keystone import Ks, KS_ARCH_X86, KS_MODE_32

def build(stock):
    pe=pefile.PE(data=stock,fast_load=True)
    base=pe.OPTIONAL_HEADER.ImageBase
    sec=pe.sections[-1]
    assert not pe.OPTIONAL_HEADER.DllCharacteristics & 0x40
    offset=lambda va: pe.get_offset_from_rva(va-base)
    sectionVA=base+sec.VirtualAddress
    out=bytearray(stock);asm=Ks(KS_ARCH_X86,KS_MODE_32)
    def assemble(source,addr):return bytes(asm.asm(source,addr)[0])
    pos=(sec.Misc_VirtualSize+15)&~15
    symbols={}
    def append(name,payload):
        nonlocal pos
        va=sectionVA+pos
        data=assemble(payload,va) if isinstance(payload,str) else payload
        i=offset(va)
        assert pos+len(data)<=sec.SizeOfRawData and out[i:i+len(data)]==bytes(len(data))
        out[i:i+len(data)]=data
        pos+=len(data)
        symbols[name]=va
        return va
    def replace(va,original,code):
        oldbytes=assemble(original,va) if isinstance(original,str) else original
        newbytes=assemble(code,va)
        i=offset(va)
        assert len(newbytes)<=len(oldbytes) and out[i:i+len(oldbytes)]==oldbytes,(hex(va),oldbytes.hex(),out[i:i+len(oldbytes)].hex())
        out[i:i+len(oldbytes)]=newbytes.ljust(len(oldbytes),b'\x90')
    label=append('wide_label',('不限\0').encode('utf-16le'))
    # Read back all old numeric profiles unchanged. Noncanonical 301..319 reads as 300.
    open_slider=append('open_slider',"""
        mov edx, [ecx+0x3bc]
        mov dword ptr [edx+8], 320
        mov eax, [esp+4]
        cmp eax, 320
        je done
        cmp eax, 300
        jle done
        mov dword ptr [esp+4], 300
    done: jmp 0x381b940
    """)
    replace(0x125c657,'call 0x381b940',f'call {open_slider}')
    # The slider has fewer screen pixels than values. Reserve a reachable last
    # notch and let 300 snap separately. The original path enforces minimum 60.
    drag=append('drag_value',"""
        call 0x381b4e0
        cmp eax, 300
        jle done
        cmp eax, 310
        mov eax, 300
        jl done
        mov eax, 320
    done: ret
    """)
    replace(0x1266731,'call 0x381b4e0',f'call {drag}')
    fmt=append('format_value',f"""
        cmp dword ptr [esp+8],320
        jne numeric
        mov eax,{label}
        ret
    numeric: jmp 0x39a6620
    """)
    for site in [0x125ed16,0x12667bb]:replace(site,'call 0x39a6620',f'call {fmt}')
    # Register the real profile key with its original minimum and default.
    replace(0x225966d,'push 300','push 320')
    # Stock initialization/save/event paths can disable the slider or substitute
    # 300 based on the VSync/window mode. Keep those settings independent.
    for site in [0x125a0c5,0x1265e45,0x1265f6c,0x12685e4]:
        replace(site,'mov edx,[ecx]; push eax; mov eax,[edx+12]; call eax','mov edx,[ecx]; push 1; call dword ptr [edx+12]')
    replace(0x125c62d,'je 0x125c63c','jmp 0x125c63c')
    replace(0x1261883,bytes.fromhex('0f85d2000000'),'nop')
    replace(0x37040f7,'je 0x3704100','jmp 0x3704100')
    # The legacy setter clamps any numeric argument into 60..300. Intercept only
    # the unique persisted unlimited value and keep the compatibility field intact.
    select=append('select_budget',"""
        mov eax,[ebp+8]
        cmp eax,320
        jne numeric
        xorpd xmm1,xmm1
        jmp 0x3846c51
    numeric: cmp eax,300
        jmp 0x3846c2b
    """)
    replace(0x3846c23,'mov eax,[ebp+8]; cmp eax,300',f'jmp {select}')
    wait=append('select_wait',"""
        cmp dword ptr [esi+0x30],320
        je unlocked
        subsd xmm1,[esi+0x40]
        jmp 0x38467e3
    unlocked: jmp 0x3846834
    """)
    replace(0x38467de,'subsd xmm1,[esi+0x40]',f'jmp {wait}')
    # Numeric software cap remains effective when VSync is selected as well.
    replace(0x384682b,'je 0x3846834','nop')
    struct.pack_into('<I',out,sec.get_field_absolute_offset('Misc_VirtualSize'),pos)
    check=pe.OPTIONAL_HEADER.get_field_absolute_offset('CheckSum')
    struct.pack_into('<I',out,check,0)
    struct.pack_into('<I',out,check,pefile.PE(data=out,fast_load=True).generate_checksum())
    candidate=pefile.PE(data=out,fast_load=True)
    assert candidate.verify_checksum()
    for f in (pe,candidate):f.parse_data_directories(directories=[1])
    assert [(m.dll,[(i.name,i.address) for i in m.imports]) for m in pe.DIRECTORY_ENTRY_IMPORT]==[(m.dll,[(i.name,i.address) for i in m.imports]) for m in candidate.DIRECTORY_ENTRY_IMPORT]
    return bytes(out), symbols
