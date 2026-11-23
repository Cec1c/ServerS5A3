"""Execute slider open/drag, text, persistence bounds and frame-loop branches on candidate bytes."""
from pathlib import Path
import hashlib, json, struct, sys
import pefile
root=Path(__file__).resolve().parent

from unicorn import Uc, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE
from unicorn.x86_const import *
info=json.loads(Path(sys.argv[1]).with_suffix('.json').read_text())
b=Path(sys.argv[1]).read_bytes()
assert hashlib.sha256(b).hexdigest()==info['exe']
pe=pefile.PE(data=b,fast_load=True);base=pe.OPTIONAL_HEADER.ImageBase;syms=info['symbols']
sp,frame,obj,state,exitva=0x100800,0x101c00,0x100c00,0x101400,0x100500
u=Uc(UC_ARCH_X86,UC_MODE_32)
for page in {a&~4095 for a in [*syms.values(),0x5016078,0x5016100,0x3846c20,0x38467de,0x381b4e0,0x381b940,0x39a6620,0x125c657,0x1266731,0x125ed16,0x12667bb,0x125c62d,0x1261883,0x37040f7,0x225966d,0x125a0c5,0x1265e45,0x1265f6c,0x12685e4,0x12618f4,0x225eaa0,0x225f6c0,0x29783f0,0x3847640,0x5667b98]}:
    u.mem_map(page,4096);u.mem_write(page,pe.get_data(page-base,4096))
u.mem_map(0x100000,0x2000)
def write(a,n):u.mem_write(a,struct.pack('<I',n&0xffffffff))
def read(a):return int.from_bytes(u.mem_read(a,4),'little')
def prepare():
    u.reg_write(UC_X86_REG_ESP,sp);u.reg_write(UC_X86_REG_EBP,frame);u.reg_write(UC_X86_REG_ECX,obj)
    write(obj+0x3bc,state);write(sp,exitva)
def run(a,stop):
    u.emu_start(a,stop,count=120)
    assert u.reg_read(UC_X86_REG_EIP)==stop,(hex(a),hex(u.reg_read(UC_X86_REG_EIP)),hex(stop))
def stub(a,code):u.mem_write(a,bytes.fromhex(code))
# Isolate the native slider's UI repaint from the real value clamp and write.
for off in (16,24,32):write(state+off,obj)
stub(0x381b630,'c3')
for profile in [60,120,240,299,300,301,309,310,319,320]:
    prepare();write(sp,profile);write(state+8,300)
    run(0x125c657,0x125c65c)
    expected=profile if profile<=300 or profile==320 else 300
    assert read(state+4)==expected and read(state+8)==320
for raw in range(60,321):
    prepare();write(state+4,raw)
    run(0x1266731,0x1266736)
    assert u.reg_read(UC_X86_REG_EAX)==(raw if raw<=300 else 300 if raw<310 else 320)
# Numeric strings still use the native formatter; unlimited resolves the same
# UTF-16 label on open and while dragging.
for site in [0x125ed16,0x12667bb]:
 for val in (120,300,320):
    prepare();write(sp,0x6230);write(sp+4,val)
    run(site,site+5 if val==320 else 0x39a6620)
    if val==320:assert u.mem_read(u.reg_read(UC_X86_REG_EAX),6).decode('utf-16le')=='不限\0'
for value in (0,59,60,90,120,144,240,299,300,301,319,320,321):
    prepare();write(sp+4,value);run(0x3846c20,exitva)
    numeric=max(60,min(value,300))
    assert read(obj+0x30)==(320 if value==320 else numeric)
    assert struct.unpack('<d',u.mem_read(obj+0x38,8))[0]==(0 if value==320 else 1000/numeric)
    for vsync in (0,1):
        prepare();u.reg_write(UC_X86_REG_ESI,obj);u.reg_write(UC_X86_REG_EBX,0x100400)
        u.mem_write(obj+0x2c,bytes([vsync]));u.mem_write(obj+0x40,bytes(8))
        u.reg_write(UC_X86_REG_XMM1,0)
        run(0x38467de,0x3846834 if value==320 else 0x100400)
        if value!=320: assert read(u.reg_read(UC_X86_REG_ESP)+4)==int(1000/numeric+0.5)
# Run the original save path through the actual controller and getter. Capture
# the value at the profile-store boundary, then feed it to the next open.
stub(0x3847640,'b8 00 0c 10 00 c3');stub(0x225f6c0,'b8 00 0c 10 00 c2 04 00')
for value in [60,120,240,300,320]:
    prepare();write(state+4,value);run(0x12618f4,0x225eaa0)
    saved=read(u.reg_read(UC_X86_REG_ESP)+4);assert saved==value
    prepare();write(sp,saved);run(0x125c657,0x125c65c);assert read(state+4)==saved
# Actual validator registration argument order and unchanged default.
prepare();run(0x225966d,0x2259676)
assert [read(sp-i) for i in (4,8,12)]==[320,60,120]
for site,target in [(0x125c62d,0x125c63c),(0x1261883,0x1261889),(0x37040f7,0x3704100)]:
 for flags in (2,0x42):
    prepare();u.reg_write(UC_X86_REG_EFLAGS,flags);run(site,target)
for site in [0x125a0c5,0x1265e45,0x1265f6c,0x12685e4]:
    prepare();write(obj,state);write(state+12,0x100250);run(site,0x100250)
    assert read(u.reg_read(UC_X86_REG_ESP)+4)==1
result=dict(passed=True,rawSliderValues=261,persistedOpenCases=10,saveReopenCases=5,budgetCases=13,waitCases=26,enableCallbacks=4,labelPaths=2,profileRange=[60,320],default=120,gameplayAcceptance='user')
Path(sys.argv[1]).with_suffix('.slider-checks.json').write_text(json.dumps(result,indent=2));print(result)
