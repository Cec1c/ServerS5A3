"""Execute both op39 count paths without launching a game client."""
import sys
from pathlib import Path
import pefile
from unicorn import Uc, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE
from unicorn.x86_const import UC_X86_REG_EBP, UC_X86_REG_ECX
import struct

data = Path(sys.argv[1]).read_bytes()
pe = pefile.PE(data=data, fast_load=True)
base = pe.OPTIONAL_HEADER.ImageBase
hook = 0x202ec89
raw = pe.get_offset_from_rva(hook-base)
assert data[raw] == 0xe9
cave = hook+5+struct.unpack_from('<i', data, raw+1)[0]
for marker in (0, 1, 0x7f, 0x80, 0x81, 0xff):
    uc = Uc(UC_ARCH_X86, UC_MODE_32)
    for addr in {hook & ~4095, cave & ~4095}:
        uc.mem_map(addr, 4096)
        off = pe.get_offset_from_rva(addr-base)
        uc.mem_write(addr, data[off:off+4096])
    uc.mem_map(0x100000, 0x10000)
    ebp = 0x108000
    uc.reg_write(UC_X86_REG_EBP, ebp)
    uc.reg_write(UC_X86_REG_ECX, 0xabcdef01)
    uc.mem_write(ebp-552, struct.pack('<I', 456))
    uc.mem_write(ebp-541, bytes([marker]))
    reached = []
    def stop(cpu, address, size, _):
        if address in (0x202ec8f, 0x202ec99):
            reached.append(address)
            cpu.emu_stop()
    uc.hook_add(UC_HOOK_CODE, stop)
    uc.emu_start(hook, hook+32, count=20)
    assert reached == [0x202ec99 if marker & 0x80 else 0x202ec8f]
    assert uc.reg_read(UC_X86_REG_ECX) == (0xabcdef01 if marker & 0x80 else 456)
print('passed: six native/absolute pickup marker cases')
