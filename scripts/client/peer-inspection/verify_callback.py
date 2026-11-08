"""Execute the actual Buff success callback with explicit API stubs, not the game."""
from pathlib import Path
import argparse,importlib.util,json,struct,sys

def run(data, present, success):
    import pefile
    from unicorn import Uc,UC_ARCH_X86,UC_MODE_32,UC_HOOK_CODE
    from unicorn.x86_const import UC_X86_REG_ESP,UC_X86_REG_EIP,UC_X86_REG_EAX,UC_X86_REG_ECX,UC_X86_REG_ESI,UC_X86_REG_EBP
    p=pefile.PE(data=data,fast_load=True);u=Uc(UC_ARCH_X86,UC_MODE_32)
    pages={0x1fc0000,0x37e8000,0x296d000,0x10f1000,0x22c3000,0x32be000,0x56a3000}
    for page in pages:
        u.mem_map(page,4096);u.mem_write(page,p.get_data(page-p.OPTIONAL_HEADER.ImageBase,4096))
    stack=0x10008000;end=0x20000000;actor=0x30000000;payload=0x30001000
    u.mem_map(0x10000000,0x10000);u.mem_map(end,4096)
    u.mem_write(stack,struct.pack('<III',end,0,success))
    u.reg_write(UC_X86_REG_ESP,stack);u.reg_write(UC_X86_REG_ESI,0x12345678);u.reg_write(UC_X86_REG_EBP,0x87654321)
    calls=[]
    def word(at):return struct.unpack('<I',u.mem_read(at,4))[0]
    def ret(value=0,pop=0):
        sp=u.reg_read(UC_X86_REG_ESP);u.reg_write(UC_X86_REG_EAX,value)
        u.reg_write(UC_X86_REG_EIP,word(sp));u.reg_write(UC_X86_REG_ESP,sp+4+pop)
    def hook(uc,va,size,user):
        sp=u.reg_read(UC_X86_REG_ESP)
        if va==0x37e8350:
            u.mem_write(word(sp+4),struct.pack('<I',17));calls.append('target-u16');ret()
        elif va==0x296d730:
            assert word(sp+4)==17;calls.append('lookup');ret(actor if present else 0)
        elif va==0x10f1d10:
            assert u.reg_read(UC_X86_REG_ECX)==actor,'departed actor dereference'
            calls.append('actor-payload');ret(payload)
        elif va==0x22c35e0:
            assert word(sp+4)==payload;calls.append('native-buff-reader');ret()
        elif va==0x32bea50:
            assert [word(sp+i) for i in (4,8,12)]==[0xf2,0,17]
            calls.append('open-target-F2');ret(pop=12)
    u.hook_add(UC_HOOK_CODE,hook)
    u.emu_start(0x1fc0e10,end,count=150)
    assert u.reg_read(UC_X86_REG_EIP)==end and u.reg_read(UC_X86_REG_ESP)==stack+4
    assert u.reg_read(UC_X86_REG_ESI)==0x12345678 and u.reg_read(UC_X86_REG_EBP)==0x87654321
    return calls

if __name__=='__main__':
    a=argparse.ArgumentParser();a.add_argument('--baseline',type=Path,required=True);a.add_argument('--candidate',type=Path,required=True);a.add_argument('--python-deps',required=True);a.add_argument('--output',type=Path,required=True);args=a.parse_args()
    sys.path.insert(0,args.python_deps)
    before=args.baseline.read_bytes();after=args.candidate.read_bytes()
    assert run(before,True,1)==run(after,True,1)==['target-u16','lookup','actor-payload','actor-payload','native-buff-reader','open-target-F2']
    assert run(before,True,0)==run(after,True,0)==[]
    try:run(before,False,1)
    except AssertionError as e:assert str(e)=='departed actor dereference'
    else:raise AssertionError('negative baseline did not reproduce')
    assert run(after,False,1)==['target-u16','lookup']
    result=dict(passed=True,baselineMissingActorFailed=True,candidateMissingActorSafe=True,validTargetOpensF2=True,failureNoWindow=True,externalCallsStubbed=True,gameplayAcceptance=False)
    args.output.write_text(json.dumps(result,indent=2),'utf8');print(json.dumps(result))
