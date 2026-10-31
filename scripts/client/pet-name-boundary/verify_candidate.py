"""Offline execution of the real call site, reader and candidate helper.

Only allocator/free, memcpy, code-page conversion, errno and pointer decoding
are explicit runtime stubs. No process launch or online game replay.
"""
from pathlib import Path
import argparse, hashlib, json, struct
from functools import lru_cache
import pefile
from unicorn import Uc, UcError, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE, UC_HOOK_MEM_INVALID
from unicorn.x86_const import *

HOOK=0x329e74e;RETURN=0x329e75d
@lru_cache(maxsize=2)
def parse(data):
    return pefile.PE(data=data,fast_load=True)

def run(data,raw,expected,patched,declared=None,packet_truncate=0,cursor_offset=0,packet_size_override=None):
    p=parse(data);u=Uc(UC_ARCH_X86,UC_MODE_32);mapped=set()
    def mapregion(va,size):
        for page in range(va&~4095,(va+size+4095)&~4095,4096):
            if page not in mapped:u.mem_map(page,4096);mapped.add(page)
    def original(va,size):
        mapregion(va,size);u.mem_write(va,p.get_data(va-p.OPTIONAL_HEADER.ImageBase,size))
    for va,size in [(0x329e000,0x1000),(0x3295000,0x2000),(0x3e75000,0x1000),(0x3e73000,0x1000),(0x242c000,0x1000),(0x4cf5000,0x1000)]:original(va,size)
    for s in p.sections:
        if s.Name.rstrip(b'\0')==b'.petnm':original(p.OPTIONAL_HEADER.ImageBase+s.VirtualAddress,s.SizeOfRawData)
    for va,size in [(0x10000000,0x20000),(0x20000000,0x220000),(0x21000000,0x10000),(0x22000000,0x10000),
                    (0xc4b000,0x1000),(0x37e0000,0x1000),(0x3e70000,0x1000),(0x3e7e000,0x1000),(0x4588000,0x1000),(0x6750000,0x1000)]:mapregion(va,size)
    put=lambda addr,n:u.mem_write(addr,struct.pack('<I',n&0xffffffff))
    get=lambda addr:struct.unpack('<I',u.mem_read(addr,4))[0]
    reader=0x20000000;packet=0x21000100;ebp=0x10018000;esp=ebp-0x6000;dest=ebp-0x2c
    rawlen=len(raw) if declared is None else declared
    wire=struct.pack('<I',rawlen)+raw+b'\x71\x45\x23\x01\x00\x02'
    packetSize=len(wire)-packet_truncate if packet_size_override is None else packet_size_override
    u.mem_write(packet,wire);put(reader,0x4cf5554);put(reader+0x20a024,packet+cursor_offset)
    put(ebp-0x4268,packet);put(ebp-0x4274,packetSize)
    u.mem_write(dest-4,b'\xcc'*34)
    u.reg_write(UC_X86_REG_EBP,ebp);u.reg_write(UC_X86_REG_ESP,esp);u.reg_write(UC_X86_REG_ESI,reader)
    u.reg_write(UC_X86_REG_EBX,0xffffffff);u.reg_write(UC_X86_REG_EDI,0x12345678)
    put(0x4588128,0x22003000);put(0x67501f8,0x242ced0)
    calls=[];faults=[];stop_at=RETURN
    def ret(value,argc=0):
        sp=u.reg_read(UC_X86_REG_ESP);ra=get(sp)
        u.reg_write(UC_X86_REG_EAX,value);u.reg_write(UC_X86_REG_ESP,sp+4+argc*4);u.reg_write(UC_X86_REG_EIP,ra)
    def code(uc,addr,size,ctx):
        sp=u.reg_read(UC_X86_REG_ESP)
        if addr==stop_at:u.emu_stop()
        elif addr==0xc4b1c0:ret(0x22000100)
        elif addr==0x3e70882:ret(0)
        elif addr==0x3e730d0:
            dst,src,n=[get(sp+x) for x in (4,8,12)];assert n<4096;u.mem_write(dst,bytes(u.mem_read(src,n)));ret(dst)
        elif addr==0x37e0ae0:
            src=get(sp+4);b=bytes(u.mem_read(src,256)).split(b'\0')[0];calls.append(b.hex())
            wide=b.decode('gb18030',errors='replace').encode('utf-16le')+b'\0\0'
            u.mem_write(0x22001000,wide);ret(0x22001000)
        elif addr==0x3e7e7d2:ret(0x22002000)
        elif addr==0x22003000:ret(get(sp+4),1)
    def invalid(uc,access,address,size,value,ctx):
        faults.append(dict(eip=hex(u.reg_read(UC_X86_REG_EIP)),address=hex(address),access=access));return False
    u.hook_add(UC_HOOK_CODE,code);u.hook_add(UC_HOOK_MEM_INVALID,invalid)
    error=None
    try:u.emu_start(HOOK,RETURN,count=200000)
    except UcError as e:error=str(e)
    finalCursor=get(reader+0x20a024)
    buffer=bytes(u.mem_read(dest,26));end=next((i for i in range(0,26,2) if buffer[i:i+2]==b'\0\0'),26)
    result=dict(patched=patched,rawBytes=len(raw),declared=rawlen,resultEAX=u.reg_read(UC_X86_REG_EAX),
        stoppedAt=hex(u.reg_read(UC_X86_REG_EIP)),cursorDelta=finalCursor-packet,
        output=buffer[:end].decode('utf-16le',errors='replace'),faults=faults,error=error,conversionCalls=len(calls))
    if patched:
        assert not faults and u.reg_read(UC_X86_REG_EIP)==RETURN,result
        assert bytes(u.mem_read(dest-4,4))==b'\xcc'*4 and bytes(u.mem_read(dest+26,4))==b'\xcc'*4
        assert u.reg_read(UC_X86_REG_ESP)==esp and u.reg_read(UC_X86_REG_EBP)==ebp
        assert u.reg_read(UC_X86_REG_ESI)==reader and u.reg_read(UC_X86_REG_EDI)==0x12345678 and u.reg_read(UC_X86_REG_EBX)==0xffffffff
        if expected is None:assert result['resultEAX']==0xffffffff,result
        else:
            assert result['output']==expected,result
            assert result['resultEAX']==len(expected.encode('utf-16le'))//2,result
            assert result['cursorDelta']==4+len(raw),result
            assert bytes(u.mem_read(finalCursor,6))==wire[-6:]
        # Execute the original caller's branch and its following byte/u32/byte
        # reads as well, so a correct helper return cannot hide cursor drift.
        stop_at=0x329e761 if expected is None else 0x329e7b7
        u.emu_start(RETURN,stop_at,count=2000)
        assert u.reg_read(UC_X86_REG_EIP)==stop_at,result
        if expected is not None:
            assert bytes(u.mem_read(ebp-0x4382,1))==b'\x71'
            assert get(ebp-0x4618)==0x12345
            assert bytes(u.mem_read(ebp-0x4384,1))==b'\x02'
            assert get(reader+0x20a024)==finalCursor+6
        result['callerBranchVerified']=True
    return result

def main():
    p=argparse.ArgumentParser();p.add_argument('--baseline',type=Path,required=True);p.add_argument('--candidate',type=Path,required=True);p.add_argument('--output',type=Path,required=True);a=p.parse_args()
    before=a.baseline.read_bytes();after=a.candidate.read_bytes()
    cases=[]
    def check(label,name,expect):
        cases.append(dict(label=label,**run(after,name.encode('gb18030'),expect,True)))
    red=run(before,b'A'*13,None,False)
    assert red['faults'] and red['faults'][0]['eip']=='0x242ced0' and red['faults'][0]['address']=='0x0',red
    for n in (0,1,11,12,13,20,25,26,30,64,255):check('ascii-'+str(n),'A'*n,'A'*min(n,12))
    for n in (1,6,11,12,13,15,31):check('chinese-'+str(n),'宠'*n,'宠'*min(n,12))
    check('default-with-suffix','宠物测试 (等级 3)-纪念','宠物测试 (等级 3)-')
    check('surrogate-fits','A'*10+'😀'+'B','A'*10+'😀')
    check('surrogate-crosses','A'*11+'😀'+'B','A'*11)
    check('embedded-nul','safe\0ignored','safe')
    cases.append(dict(label='malformed-codepage',**run(after,b'\x81','�',True)))
    for label,raw,kwargs in [('length-overrun',b'abc',dict(declared=1000)),('length-wrap',b'abc',dict(declared=0xffffffff)),
        ('too-large-name',b'A'*256,{}),('prefix-truncated',b'',dict(packet_size_override=3)),
        ('tail-truncated',b'abc',dict(packet_truncate=1)),
        ('cursor-before-packet',b'a',dict(cursor_offset=-1)),('cursor-after-packet',b'a',dict(cursor_offset=100)),
        ('packet-end-wrap',b'a',dict(packet_size_override=0xffffffff))]:
        cases.append(dict(label=label,**run(after,raw,None,True,**kwargs)))
    # Each receiving player runs its own unchanged call-site contract. These
    # loops validate repeated receiver executions, not a multiplayer game.
    for players in (2,4):
        for stage in ('entry','room','return','rename'):
            for receiver in range(players-1):check(f'{players}-receivers-{stage}-{receiver}','B'*13,'B'*12)
    out=dict(baselineSHA256=hashlib.sha256(before).hexdigest(),candidateSHA256=hashlib.sha256(after).hexdigest(),
        originalCrash=red,cases=cases,passed=True,count=len(cases),gameExecuted=False,
        scope='Exact native call-site and helper emulation, explicit runtime stubs; repeated receivers are isolated synthetic executions, not game networking')
    a.output.write_text(json.dumps(out,ensure_ascii=False,indent=2),'utf8')
    print(json.dumps(dict(passed=True,cases=len(cases),baselineReproduced=True,candidateSHA256=out['candidateSHA256'])))
if __name__=='__main__':main()
