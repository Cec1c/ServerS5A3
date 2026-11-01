"""Bounded native shop guards, accepted transition and sender; UI is stubbed."""
import argparse,json,struct
from pathlib import Path
import patch_native_mall_visit as patch
from test_guild_war_refresh_v173 import WarNative
from test_patch_fatigue_count_v164 import windows_image
from unicorn.x86_const import *

class Shop(WarNative):
    OBJECT=0x24000000
    def __init__(self,data,active=True):
        super().__init__(data)
        self.u.mem_map(self.OBJECT,0x30000)
        self.put(self.NETWORK+8,int(active)); self.sent=[]
        self.hooks.update({0x374F280:self.begin,0x374F3F0:self.append,0x3750AC0:lambda:self.ret()})
    def begin(self):self.sent.append(dict(opcode=self.arg(),body=''));self.ret(4)
    def append(self):self.sent[-1]['body']+=struct.pack('<I',self.arg()).hex();self.ret(4)
    def open_scene(self,old,new,ready=True):
        self.put(self.OBJECT+8,old)
        self.hooks.update({
            0x29610E0:lambda:self.ret(value=int(ready)),
            0x2525A60:lambda:self.ret(value=1),
            0x2303380:lambda:self.ret(4,0x20005000),
            0x22FF610:lambda:self.ret(value=0),
            0x19568B0:lambda:self.ret(8),
            0x3E73CB6:lambda:self.ret(value=0),
            0x32B50E0:lambda:self.ret(value=1),
            0x3525860:lambda:self.ret(value=0),
            0x20E8070:lambda:self.ret(value=1),
            0x20E5D90:lambda:self.ret(value=0),
            0x25253D0:lambda:self.ret(value=0),
            0x2526B50:lambda:self.ret(value=0),
            0x9872B0:lambda:self.ret(value=1),
            0x985C30:lambda:self.ret(value=0),
            0x1455F70:lambda:self.ret(value=0),
            # Only the rendering/construction block is skipped, after all
            # original acceptance guards and the native visibility assignment.
            0x1977AD1:lambda:self.u.reg_write(UC_X86_REG_EIP,0x1978767),
        })
        self.run(0x1977820,(new,),ecx=self.OBJECT)

def verify(before,after,path):
    expected,receipt=patch.build(before);assert after==expected
    rows=[]
    for active in (False,True):
        for state in (0,1,5,6):
            lab=Shop(after,active)
            lab.run(patch.HELPER,(state,),pop=0)
            want=[dict(opcode=63,body=patch.MARKER.hex())] if active and state==5 else []
            assert lab.sent==want,(active,state,lab.sent)
            assert lab.read(0x569F0C0)==state and lab.u.mem_read(0x5596BD8,1)==b'\1'
            rows.append(dict(active=active,state=state,packets=lab.sent))
    for label,data in [('before',before),('after',after)]:
        for name,old,new,ready,want in [('open',0,1,True,label=='after'),('already open',1,1,True,False),('unavailable',0,1,False,False)]:
            lab=Shop(data);lab.open_scene(old,new,ready)
            assert lab.sent==([dict(opcode=63,body=patch.MARKER.hex())] if want else []),(label,name,lab.sent)
            rows.append(dict(image=label,case=name,packets=lab.sent))
        lab=Shop(data);lab.run(0x194B2C0)
        assert lab.sent==[dict(opcode=63,body='')]
        # Original select-character ACK sender remains an empty query.
        lab=Shop(data);lab.put(lab.STACK,lab.STOP);lab.u.reg_write(UC_X86_REG_ESP,lab.STACK)
        lab.u.emu_start(0x1FB2AAD,0x1FB2AC0,count=1000)
        assert lab.sent==[dict(opcode=63,body='')] and lab.reg(UC_X86_REG_ESP)==lab.STACK
    for bad in (before[:-1],before[:-1]+bytes([before[-1]^1]),after):
        try:patch.build(bad)
        except ValueError:pass
        else:raise AssertionError('bad input accepted')
    windows_image(path)
    return dict(passed=True,cases=rows,patch=receipt,windowsImage=True,gameExecuted=False,
        scope='Original guards/visibility assignment, shared state setter, marker helper and unchanged login sender. UI construction/rendering, world accessors and writer sinks are explicit stubs; no full-game acceptance.')

if __name__=='__main__':
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--before',type=Path,required=True);p.add_argument('--after',type=Path,required=True);p.add_argument('--result',type=Path,required=True)
    a=p.parse_args();result=verify(a.before.read_bytes(),a.after.read_bytes(),a.after.resolve())
    with a.result.open('x',encoding='utf8') as f:json.dump(result,f,ensure_ascii=False,indent=2)
    print(json.dumps(dict(passed=True,cases=len(result['cases']),outputSHA256=result['patch']['outputSHA256'])))
