"""Real bounded x86 send/receive tests, with no game process or socket."""
import hashlib,json,struct,sys
from pathlib import Path
import argparse
P=argparse.ArgumentParser(description="Bounded native ACK checks; no game process or socket")
P.add_argument('--before',type=Path,required=True)
P.add_argument('--after',type=Path,required=True)
P.add_argument('--out',type=Path,required=True,help='New output directory')
cli_args=P.parse_args()
import patch_container_upgrade_ack as patch
EXPECTED=patch.INPUT_SHA256
def load():
    raw=cli_args.before.read_bytes()
    assert hashlib.sha256(raw).hexdigest()==EXPECTED,'Input SHA256 mismatch'
    return raw,None
sys.dont_write_bytecode = True
from test_patch_fatigue_count_v164 import native,windows_image
from test_guild_war_refresh_v173 import WarNative
from unicorn.x86_const import *

class Sender(WarNative):
    OBJECT,VTABLE,ITEM,CONTAINER=0x20010000,0x20011000,0x20012000,0x20013000
    def __init__(self,data,slot=87,active=True,definition=True,container=True):
        super().__init__(data);self.sent=[]
        self.put(self.NETWORK+8,int(active));self.put(self.OBJECT,self.VTABLE)
        self.put(self.OBJECT+0x550,148);self.put(self.ITEM+0x18,490008560)
        self.put(self.VTABLE+0x28,self.STOP+0x110);self.put(self.VTABLE+0x38,self.STOP+0x120)
        self.hooks.update({
            0x25eca10:lambda:self.ret(4,slot),
            0x374f280:self.begin,
            0x374f390:lambda:self.append(1),0x374f3c0:lambda:self.append(2),0x374f3f0:lambda:self.append(4),
            0x3750ac0:lambda:self.ret(),
            self.STOP+0x110:lambda:self.ret(value=self.ITEM if definition else 0),
            self.STOP+0x120:lambda:self.ret(value=self.CONTAINER if container else 0),
            0x3e4b210:lambda:self.ret(), # adapter presents an already decoded item ID
            0x25edb70:lambda:self.ret(value=0),
        })
    def begin(self):self.sent.append(dict(opcode=self.arg(),body=''));self.ret(4)
    def append(self,size):
        self.sent[-1]['body']+=(self.arg()&((1<<(8*size))-1)).to_bytes(size,'little').hex();self.ret(4)

def main():
    before,_=load();e=cli_args.out.resolve();after=cli_args.after.read_bytes()
    expected,receipt=patch.build(before)
    assert after==expected,'Output differs from the exact cumulative patch'
    for bad in (before[:-1],before[:-1]+bytes([before[-1]^1]),after,b''):
        try: patch.build(bad)
        except ValueError: pass
        else: raise AssertionError('unrecognized input was accepted')
    e.mkdir(parents=True,exist_ok=False)
    result={'baselineSHA256':EXPECTED,'candidateSHA256':hashlib.sha256(after).hexdigest(),'sender':[],'gate':[],'receive':[],'gameExecuted':False,'gameAcceptance':'not_run'}
    cases=[('personal',0x2a27760,(10005198,),{},[dict(opcode=408,body='5700')]),
           ('personal_missing',0x2a27760,(10005198,),{'slot':-1},[]),
           ('inventory',0x24a5df0,(87,),{},[dict(opcode=430,body='5700')]),
           ('inventory_disconnected',0x24a5df0,(87,),{'active':False},[]),
           ('account',0x24edc40,(),{},[dict(opcode=515,body=struct.pack('<HBIIII',87,0,0,148,0,0).hex())]),
           ('account_missing',0x24edc40,(),{'slot':-1},[]),
           ('account_no_definition',0x24edc40,(),{'definition':False},[]),
           ('account_no_container',0x24edc40,(),{'container':False},[])]
    for name,entry,args,kw,want in cases:
        for label,data in [('baseline',before),('candidate',after)]:
            lab=Sender(data,**kw);lab.run(entry,args,ecx=lab.OBJECT,pop=0)
            assert lab.sent==want,(name,label,lab.sent)
            result['sender'].append(dict(case=name,image=label,passed=True,packets=lab.sent))
    for cls in (0,1,2,255):
        old=native(before,[cls],range(65536));new=native(after,[cls],range(65536))
        changed=[i for i,(a,b) in enumerate(zip(old,new)) if a!=b]
        assert changed==([408,430] if cls==1 else []),(cls,changed)
        assert cls!=1 or all(new[i]==1 for i in changed)
        result['gate'].append(dict(classification=cls,opcodes=65536,changed=changed,passed=True))
    for cls in range(3,255):
        ops=(44,66,306,408,430,431,515,0x7f01,0x7f02,0x7f10,0x7f11,0x7f12)
        assert native(before,[cls],ops)==native(after,[cls],ops)
    for opcode in (408,430):
        for success in (False,True):
            for label,data in [('baseline',before),('candidate',after)]:
                lab=WarNative(data);events=[]
                lab.hooks[0x374fc40]=lambda:(events.append(('clear_pending',lab.arg())),lab.ret(4))
                # 408 has a verified registered native callback; 430 has none.
                # No invented callback or artificial permission in this test.
                if opcode==408:lab.put(0x2000c000,0x1fbe160,0)
                else:lab.hooks[0x2523ff0]=lambda:lab.ret(value=0)
                body=b'\1' if success else b'\0\x13'
                wire=bytearray(16)+body;wire[0]=1
                struct.pack_into('<H',wire,1,opcode);struct.pack_into('<I',wire,3,len(wire))
                lab.u.mem_write(lab.PACKET,bytes(wire));lab.run(0x25239a0,(0,lab.PACKET,0),ecx=lab.NETWORK)
                assert events==([('clear_pending',opcode)] if label=='candidate' else []),(opcode,label,events)
                if label=='candidate':assert lab.read(0x57f9ad4)==0,'ACK consumed exactly'
                expected=[0x562b] if label=='candidate' and opcode==408 and not success else []
                assert lab.messages==expected,(opcode,label,lab.messages)
                result['receive'].append(dict(opcode=opcode,success=success,image=label,pendingCleared=bool(events),remaining=lab.read(0x57f9ad4),passed=True))
    windows_image(cli_args.after.resolve())
    result.update(passed=True,windowsImage=True,controlledDependencies='virtual item/container getters, decoded item ID, lookup slot, packet writer sinks, CRT, callback lookup and UI; native send branches and real ACK dispatcher/gate/status/error readers execute unchanged')
    (e/'native-tests.json').write_text(json.dumps(result,ensure_ascii=False,indent=2)+'\n','utf-8')
    print(json.dumps({k:v for k,v in result.items() if k not in ('sender','receive')},ensure_ascii=False))
if __name__=='__main__':main()
