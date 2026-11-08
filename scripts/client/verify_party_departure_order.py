"""Execute hash-pinned client branches; actor lookup and roster membership are fixtures.
This checks the departure branch, not complete client or live UI acceptance.
"""
import argparse, hashlib, struct, json
from pathlib import Path
import pefile
from unicorn import Uc, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE
from unicorn.x86_const import UC_X86_REG_EAX, UC_X86_REG_EBX, UC_X86_REG_ECX, UC_X86_REG_EBP, UC_X86_REG_ESP, UC_X86_REG_EIP
parser=argparse.ArgumentParser()
parser.add_argument('exe',type=Path)
parser.add_argument('--output',type=Path,required=True)
args=parser.parse_args()
exe=args.exe
raw=exe.read_bytes(); sha=hashlib.sha256(raw).hexdigest()
assert sha=='7861cd3c546e73651ddfd28047686a29e580f5bebd38b1a7fd681d3b36fc24e0'
pe=pefile.PE(data=raw,fast_load=True)
slices=[(0x20166fe,0x2016712),(0x2973588,0x297359a),(0x201782a,0x201786b)]
def run(order):
    u=Uc(UC_ARCH_X86,UC_MODE_32); pages=set()
    for a,b in slices:
        page=a&~4095
        if page not in pages: u.mem_map(page,4096); pages.add(page)
        u.mem_write(a,pe.get_data(a-0x400000,b-a))
    for page in [0x296e000,0x2961000,0x2018000]:
        if page not in pages:u.mem_map(page,4096);pages.add(page)
    u.mem_map(0x10000000,0x10000)
    actor=0x10001000; selfrecord=0x10003000; peerrecord=0x10004000
    u.mem_write(actor+0x4c8,struct.pack('<I',selfrecord))
    u.reg_write(UC_X86_REG_EBP,0x1000f000); u.reg_write(UC_X86_REG_ESP,0x1000e000)
    stops={0x201786b:'teardown',0x2018b7b:'skip',0x2017d7c:'join',0x2017f95:'reconcile'}
    branch=[]
    def hook(u,a,size,data):
        if a in [0x296e590,0x29614d0]:
            value=struct.unpack('<I',u.mem_read(actor+0x4c8,4))[0] if a==0x296e590 else u.reg_read(UC_X86_REG_ECX)+0x4b4
            u.reg_write(UC_X86_REG_EAX,value)
            sp=u.reg_read(UC_X86_REG_ESP);ret=struct.unpack('<I',u.mem_read(sp,4))[0]
            u.reg_write(UC_X86_REG_ESP,sp+4);u.reg_write(UC_X86_REG_EIP,ret)
        elif a in stops:branch.append(stops[a]);u.emu_stop()
    u.hook_add(UC_HOOK_CODE,hook)
    for name in order:
        u.reg_write(UC_X86_REG_EBX,selfrecord if name=='self' else peerrecord)
        u.emu_start(0x20166fe,0x2016712,count=100)
        # Both previous roster records contain the local actor. Execute the
        # native slot-removal tail after fixture lookup has found that actor.
        u.reg_write(UC_X86_REG_EAX,actor)
        u.emu_start(0x2973588,0x297359a,count=100)
        u.emu_start(0x201782a,0,count=100)
    return branch
old=run(['peer','self']);new=run(['self','peer'])
assert old==['skip','skip'],old
assert new==['teardown','skip'],new
result={'exe_sha256':sha,'old_peer_first':old,'candidate_self_first':new,'scope':'native before/after identity and slot-detach branches; lookup fixture, not full client acceptance','slices':[{'start':hex(a),'end':hex(b),'bytes':pe.get_data(a-0x400000,b-a).hex()} for a,b in slices]}
args.output.write_text(json.dumps(result,indent=2),encoding='utf-8')
print(json.dumps({k:v for k,v in result.items() if k!='slices'}))
