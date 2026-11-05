"""Execute the current client adventure EXP percentage arithmetic (no renderer)."""
from pathlib import Path
import json,struct,hashlib,sys
import pefile
from unicorn import Uc,UC_ARCH_X86,UC_MODE_32,UC_HOOK_CODE
from unicorn.x86_const import *
p=Path(sys.argv[1]);b=p.read_bytes();sha=hashlib.sha256(b).hexdigest();assert sha=='e142a7e6d530a0e369c722cb7fcc90c18f2d18884a343096d88336aed29f75e4'
pe=pefile.PE(data=b,fast_load=True);rows=[]
for current,required in [(0,0),(0,500000000),(1,500000000),(250000000,500000000),(499999999,500000000),(4000000000,8000000000)]:
 u=Uc(UC_ARCH_X86,UC_MODE_32)
 for page in [0xbdf000,0x3e74000,0x3e77000,0x10000000,0x11000000,0x12000000]:u.mem_map(page,4096)
 for va,size in [(0xbdf424,0x4b),(0x3e740b0,0x100),(0x3e77430,0x180)]:
  off=pe.get_offset_from_rva(va-pe.OPTIONAL_HEADER.ImageBase);u.mem_write(va,b[off:off+size])
 u.mem_write(0x110003f0,struct.pack('<QQ',current,required));u.mem_write(0x11000800,struct.pack('<I',0x11000c00));u.mem_write(0x11000ce0,struct.pack('<I',0x12000100));u.mem_write(0x12000100,b'\xc2\x08\x00')
 u.reg_write(UC_X86_REG_ESI,0x11000000);u.reg_write(UC_X86_REG_EBX,0x11000800);u.reg_write(UC_X86_REG_ESP,0x10000f00)
 captured=[]
 def hook(uc,va,size,_):
  if va==0x12000100:
   esp=uc.reg_read(UC_X86_REG_ESP);captured.append(struct.unpack('<II',uc.mem_read(esp+4,8)))
  if va==0xbdf46f:uc.emu_stop()
 u.hook_add(UC_HOOK_CODE,hook);u.emu_start(0xbdf424,0,count=1000)
 want=0 if required==0 else current*100//required
 assert captured==[(want,100)],captured
 assert u.reg_read(UC_X86_REG_ESP)==0x10000f00
 rows.append(dict(currentLevelExperience=current,nextLevelCost=required,percent=want))
result=dict(passed=True,sha256=sha,scenarios=len(rows),rows=rows,fixtures=['UI object fields','vtable progress sink'],native=['BDF424..BDF46F percentage computation','3E740B0 uint64 multiply','3E77430 uint64 divide'],limitations=['no renderer','next-level cost table lookup not executed','not GUI acceptance'])
Path(sys.argv[2]).open('x',encoding='utf8').write(json.dumps(result,indent=2));print(json.dumps(result))
