"""Execute exported server583 bodies through v152/candidate native storage.

Only byte-reader IO, scene availability and UI lookup are stubs. Vector clear,
insert, lookup, update and candidate barrier argument are actual image code.
"""
from pathlib import Path
import sys,struct,json,hashlib,argparse
sys.dont_write_bytecode=True
parser=argparse.ArgumentParser(description=__doc__)
for name in ['baseline','candidate','input','output']:parser.add_argument('--'+name,type=Path,required=True)
parser.add_argument('--python-deps')
args=parser.parse_args()
if args.python_deps:sys.path.insert(0,args.python_deps)
if args.output.exists():parser.error('new output required')
import pe_support as pe
from unicorn import Uc,UC_ARCH_X86,UC_MODE_32,UC_HOOK_CODE
from unicorn.x86_const import *
scenarios=json.loads(args.input.read_text('utf8'))
images=[(args.baseline,'44aecee7c48bc1c89582ef1cd95694950569951caeef3b22ec3478c917b8ce7e'),(args.candidate,'aac86cd74eb2a0155f025e92c394124ffaaeceecf50479fe26e178fe2b04d661')]
results=[]
for path,want in images:
    data=path.read_bytes();assert hashlib.sha256(data).hexdigest()==want
    _,sections=pe.headers(data)
    u=Uc(UC_ARCH_X86,UC_MODE_32);mapped=set()
    def put(va,blob):
        for page in range(va&~4095,(va+len(blob)+4095)&~4095,4096):
            if page not in mapped:u.mem_map(page,4096);mapped.add(page)
        u.mem_write(va,blob)
    def native(va,size):
        off=pe.offset(sections,va,size);put(va,data[off:off+size])
    for va,size in [(0x1feaac0,0x6e),(0x1a59e10,11),(0x25204a0,0xbf),(0x2520350,0x14a),(0x3a2a290,0x25),(0x1a5bc10,0x178),(0x1a57b40,0x9e),(0x224e5f0,0x1b),(0xa8f6c0,0x1b),(0x3a2d530,0x4d),(0x12f2fb0,0xb8),(0x1872744,7)]:native(va,size)
    if path==args.candidate:native(0x6eab000,53)
    manager,proxy,entries,stack,stop,obj=0x100000,0x110000,0x120000,0x202000,0x300000,0x310000
    for va,size in [(0,4096),(manager,4096),(proxy,4096),(entries,8192),(stack-8192,16384),(stop,4096),(obj,4096)]:put(va,bytes(size))
    def w32(addr,value):put(addr,struct.pack('<I',value))
    def r32(addr):return struct.unpack('<I',u.mem_read(addr,4))[0]
    w32(0x55b3bdc,0x12345678);w32(0x56a3778,manager);w32(0x56a3798,0)
    put(manager+0xb8,struct.pack('<4I',proxy,entries,entries+16,entries+8192))
    w32(proxy,manager+0xb8)
    put(entries,struct.pack('<4I',111,1,9999,123))
    put(0x37e82e0,b'\xc3');put(0x37e83c0,b'\xc3')
    # No current world/UI: execute native storage independently of observers.
    put(0x2531ed0,b'\x31\xc0\xc3');put(0x32b4bc0,b'\x31\xc0\xc3')
    body=b'';cursor=0
    def hook(emu,address,size,_):
        global cursor
        if address in (0x37e82e0,0x37e83c0):
            sp=emu.reg_read(UC_X86_REG_ESP);dest=r32(sp+4)
            count=r32(sp+8) if address==0x37e82e0 else 4
            assert cursor+count<=len(body)
            emu.mem_write(dest,body[cursor:cursor+count]);cursor+=count
    u.hook_add(UC_HOOK_CODE,hook)
    def call(va,args=(),this=manager,end=stop):
        put(stack,struct.pack('<'+'I'*(1+len(args)),stop,*args))
        u.reg_write(UC_X86_REG_ESP,stack);u.reg_write(UC_X86_REG_ECX,this)
        u.emu_start(va,end,count=200000)
        assert u.reg_read(UC_X86_REG_EIP)==end
        return u.reg_read(UC_X86_REG_EAX)
    checks=[]
    for scenario in scenarios:
        for encoded in scenario['bodies']:
            body=bytes.fromhex(encoded);cursor=0
            call(0x1feaac0)
            assert cursor==len(body)
        begin,end=r32(manager+0xbc),r32(manager+0xc0)
        assert begin==entries and begin<=end<=entries+8192
        store=dict(struct.unpack('<II',u.mem_read(pos,8)) for pos in range(begin,end,8))
        assert (111 in store)==scenario['has_open']
        assert store.get(111,0)==scenario['want_open'] and 9999 not in store
        value=call(0x1a57b40,(111,));assert value==scenario['want_open']
        # Run the real original/patched argument site with both old latch values.
        barrier_args=[]
        for latch in (0,1):
            put(obj+0xac,bytes([latch]));u.reg_write(UC_X86_REG_EBX,obj)
            u.reg_write(UC_X86_REG_ESP,stack)
            u.emu_start(0x1872744,0x187274b,count=10000)
            assert u.reg_read(UC_X86_REG_EIP)==0x187274b
            arg=u.reg_read(UC_X86_REG_ECX)
            assert arg==(latch if path==args.baseline else 1-value)
            assert u.mem_read(obj+0xac,1)[0]==latch
            barrier_args.append(arg)
        checks.append(dict(name=scenario['name'],symbol111=store.get(111),getter=value,barrier_arguments=barrier_args))
    results.append(dict(image=str(path),sha256=want,checks=checks))
result=dict(passed=True,results=results,input_sha256=hashlib.sha256(args.input.read_bytes()).hexdigest(),scope=__doc__)
args.output.write_text(json.dumps(result,indent=2),'utf8')
print(json.dumps(result))
