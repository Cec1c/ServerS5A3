"""Replay real-server FiendWar bodies through native callbacks and UI selection.

STL/resource nodes, timer/constructor leaves, localized announcements,
derived icon-cache rebuild and UI-manager leaves are explicit fixtures.
The native state reset, body readers, model reducers and battle-window
selection execute original instructions. This does not render the game.
"""
import argparse,collections,hashlib,json,struct,sys
from pathlib import Path
import pefile
from unicorn import Uc,UC_ARCH_X86,UC_MODE_32,UC_HOOK_CODE,UC_HOOK_MEM_WRITE
from unicorn.x86_const import *
sys.path.insert(0,str(Path(__file__).resolve().parent/'client'))
from patch_raid_board_v144 import build,INPUT_SHA256

def probe(raw,corpus,candidate):
 u=Uc(UC_ARCH_X86,UC_MODE_32)
 pe=pefile.PE(data=raw,fast_load=True);base=pe.OPTIONAL_HEADER.ImageBase
 assert base==0x400000
 u.mem_map(base,(pe.OPTIONAL_HEADER.SizeOfImage+4095)//4096*4096)
 for section in pe.sections:u.mem_write(base+section.VirtualAddress,section.get_data())
 module_sha=hashlib.sha256(raw).hexdigest()
 for at,size in [(0,4096),(0x10000000,0x40000),(0x20000000,0x20000),(0x60000000,0x1000000)]:u.mem_map(at,size)
 SP,STOP,BUF,MODEL=0x10030000,0x20001000,0x20002000,0x60000000
 def r(a):return struct.unpack('<I',u.mem_read(a,4))[0]
 def w(a,v):u.mem_write(a,struct.pack('<I',v&0xffffffff))
 def ret(value=0,pop=0):
  sp=u.reg_read(UC_X86_REG_ESP);u.reg_write(UC_X86_REG_EAX,value);u.reg_write(UC_X86_REG_EIP,r(sp));u.reg_write(UC_X86_REG_ESP,sp+4+pop)
 allocation=MODEL+0x10000;maps={};trace=collections.deque(maxlen=20);calltrace=collections.deque(maxlen=15);seen=set();ui=[]
 native_writes=[];alloc_calls=[]
 def memwrite(vm,access,at,size,value,unused):
  if MODEL<=at<MODEL+0x1000000:native_writes.append(dict(at=hex(at-MODEL),size=size,value=value,instruction=hex(vm.reg_read(UC_X86_REG_EIP))))
 u.hook_add(UC_HOOK_MEM_WRITE,memwrite)
 def alloc(size=0x1000):
  nonlocal allocation
  at=allocation;allocation+=(size+255)//256*256;return at
 def mp(address):
  if address not in maps:
   container,root,sentinel=alloc(),alloc(),alloc();w(container,root);w(root+8,sentinel);maps[address]=dict(container=container,root=root,sentinel=sentinel,nodes={})
  return maps[address]
 def node(address,key):
  m=mp(address)
  if key not in m['nodes']:m['nodes'][key]=alloc()
  return m['nodes'][key]
 def iterator(dest,address,key=None):
  m=mp(address);u.mem_write(dest,struct.pack('<III',m['container'],0,m['sentinel'] if key is None else node(address,key)));return dest
 def hook(vm,at,size,unused):
  trace.append(hex(at));seen.add(at);sp=u.reg_read(UC_X86_REG_ESP);ecx=u.reg_read(UC_X86_REG_ECX)
  if size==5 and u.mem_read(at,1)==b'\xe8':calltrace.append(hex(at+5+struct.unpack('<i',u.mem_read(at+1,4))[0]))
  if at==STOP:u.emu_stop()
  elif at==0x8a9480:alloc_calls.append(r(sp+4));ret(MODEL)
  elif at==0xb8f990:ret(MODEL)
  elif at in (0xb89210,0xb46290,0x3311d80,0x3dc1f60):ret(iterator(r(sp+4),ecx,r(r(sp+8))),8)
  elif at==0xa8f6c0:ret(iterator(r(sp+4),ecx),4)
  elif at in (0x38bcf40,0x3ccceb0):
   victim=r(sp+16);w(victim+16,0);ret(iterator(r(sp+4),ecx),16)
  elif at==0x1223ea0:ret(r(ecx+8)+16)
  elif at==0x261f680:ret(1,4)
  elif at==0xb8ae00:ret() # derived icon cache rebuild, checked separately
  elif at==0xb8a590:ret(0,8) # localized announcement branches
  elif at==0xb8ac10:ret(0,4)
  elif at in (0xb8b3c0,0xb8b4e0,0xb8b2a0):ret(0,4) # diagnostic formatting only
  elif at==0x32b5c90:ret(0,8)
  elif at==0x3e73cb6:ret(0)
  elif at in (0x32b8920,0x32b88b0):
   w(r(sp+4),0);w(r(sp+4)+4,0);ret(r(sp+4),4) # absent intro windows, not battle UI
  elif at in (0xb8d320,):ret(0,4)
  elif at in (0x15bcc40,0x38160c0,0x385c030):ret()
  elif at==0x385c050:ret(0)
  elif at==0x3815ea0:ret(MODEL+0x5000)
  elif at==0x38134b0:ret()
  elif at==0xb32880:ret(0,4)
  elif at==0x1a553e0:ret(MODEL+0x6000)
  elif at in (0x39a6620,0x37de240):ret(BUF+0x10000)
  elif at==0x3810650:ret(MODEL+0x7000,4)
  elif at==0x2a02ac0:ret()
  elif at==0x38168f0:
   w(ecx,r(sp+4));w(ecx+4,r(sp+8));ret(0,8)
  elif at==0x32b55e0:ret(int(r(sp+4) in ui),4)
  elif at==0x32b94a0:
   op=r(sp+4)
   if op in ui:ui.remove(op)
   ret(0,12)
  elif at==0x32bea50:ui.append(r(sp+4));ret(0,12)
  elif at==0x3e738b1:raise AssertionError('native iterator invariant failed')
 u.hook_add(UC_HOOK_CODE,hook)
 w(0x5657ee4,MODEL);head=alloc();w(MODEL+0x10,head);w(head,head);w(head+4,head);w(head+8,head)
 def run(start,args=(),ecx=0):
  for reg in [UC_X86_REG_EAX,UC_X86_REG_EBX,UC_X86_REG_EDX,UC_X86_REG_ESI,UC_X86_REG_EBP]:u.reg_write(reg,0)
  u.reg_write(UC_X86_REG_ECX,ecx);u.reg_write(UC_X86_REG_ESP,SP);u.reg_write(UC_X86_REG_EFLAGS,0x202);w(SP,STOP)
  for i,x in enumerate(args):w(SP+4+i*4,x)
  try:u.emu_start(start,0,count=50000)
  except Exception as e:raise AssertionError(dict(scenario=scenario['id'],opcode=op,eip=hex(u.reg_read(UC_X86_REG_EIP)),trace=list(trace),calls=list(calltrace),error=str(e))) from e
  assert u.reg_read(UC_X86_REG_EIP)==STOP,dict(scenario=scenario['id'],opcode=op,eip=hex(u.reg_read(UC_X86_REG_EIP)),calls=list(calltrace),trace=list(trace))
 handlers={1483:r(0xb90cc5),1484:0xb903f0,1485:0xb90480,1486:0xb905e0,1487:0xb90730,1488:0xb90880,1489:0xb909e0,1490:0xb90540,1491:0xb90aa0}
 results=[]
 scenario={'id':'stale-board-initializer'};op=1483
 w(MODEL+4,6);w(MODEL+0x48,0);w(MODEL+0x6c,17);w(MODEL+0x14,31)
 run(handlers[1483]);run(0xb88390,(1,),MODEL)
 init=dict(old_nodes=r(MODEL+0x14),state=r(MODEL+4),board_mode=r(MODEL+0x48),merchant=r(MODEL+0x6c),battle_ui=list(ui))
 if candidate:assert init==dict(old_nodes=0,state=2,board_mode=1,merchant=0,battle_ui=[262]),init
 else:assert init['old_nodes']==31 and init['state']==6 and init['board_mode']==0 and init['merchant']==17
 if candidate:
  w(0x5657ee4,0);run(handlers[1483]);assert r(0x5657ee4)==MODEL and alloc_calls==[0x20c]
 for scenario in corpus['scenarios']:
  for packet in scenario['packets']:
   op=packet['opcode']
   if op==601:w(MODEL+0x6000+0x2c,bytes.fromhex(packet['body_hex'])[0])
   if op not in handlers:continue
   native_writes.clear();body=bytes.fromhex(packet['body_hex']);u.mem_write(BUF,body or b'\0');w(0x57f9ad0,BUF);w(0x57f9ad4,len(body));run(handlers[op])
   assert r(0x57f9ad4)==0,(scenario['id'],op,'native reader left bytes',r(0x57f9ad4))
   if op==1484:run(0xb88390,(1,),MODEL)
   if op==1490:assert r(MODEL+0x6c)==body[0]
   if op==1484:assert r(MODEL+4)==body[0]
   results.append(dict(scenario=scenario['id'],opcode=op,consumed=len(body),state=r(MODEL+4),board_mode=r(MODEL+0x48),battle_ui=list(ui),native_model_writes=list(native_writes)))
 rout=dict(passed=True,module_sha256=module_sha,initializer=init,constructor_allocations=alloc_calls,packets=len(results),results=results,fixtures=['resource indexed STL nodes, iterators and erase leaves','native constructor allocation fixture for absent object','timer leaf','intro UI lookups absent','localized announcement and derived icon cache rebuild','diagnostic formatting, RTTI and terminal result lookup','battle UI manager open/close leaves'],native_executed=['nine registered callbacks','native byte and block readers','state reducer and model reset','area/member/monster/enemy/timer/symbol reducers','per-frame battle UI selection'],rendered=False)
 return rout

def main():
 p=argparse.ArgumentParser(description=__doc__)
 p.add_argument('--input',type=Path,required=True);p.add_argument('--candidate',type=Path,required=True);p.add_argument('--corpus',type=Path,required=True);p.add_argument('--receipt',type=Path,required=True)
 a=p.parse_args()
 if a.receipt.exists() or a.receipt.resolve() in {x.resolve() for x in(a.input,a.candidate,a.corpus)}:p.error('new distinct receipt required')
 original=a.input.read_bytes();candidate=a.candidate.read_bytes()
 assert hashlib.sha256(original).hexdigest()==INPUT_SHA256
 assert build(original)[0]==candidate
 corpus=json.loads(a.corpus.read_text(encoding='utf-8'))
 before=probe(original,corpus,False);after=probe(candidate,corpus,True)
 assert before['packets']==after['packets'] and before['packets']>0
 receipt=dict(passed=True,source=before,candidate=after,corpus_sha256=hashlib.sha256(a.corpus.read_bytes()).hexdigest(),rendered=False,full_dispatch=False,full_ui_acceptance=False)
 with a.receipt.open('x',encoding='utf-8') as f:json.dump(receipt,f,indent=2)
 print(json.dumps(dict(passed=True,packets_per_image=after['packets'],source_initializer=before['initializer'],candidate_initializer=after['initializer'])))

if __name__=='__main__':main()
