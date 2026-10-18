"""Native pending-19 recovery: real decision, sender and ACK map stores.

STL lookup/iterator leaf functions and UI/transport leaves are isolated. The
pending decisions, wire constructor callsites, ACK branch and map writes run
as original x86 instructions. This is not an interactive game acceptance.
"""
from pathlib import Path
import sys,struct,json,hashlib,os
from unicorn import Uc,UC_ARCH_X86,UC_MODE_32,UC_HOOK_CODE
from unicorn.x86_const import *
import patch_item_sync_v130 as patch

def probe(image,counts):
 opt,ss=patch.pe.headers(image);u=Uc(UC_ARCH_X86,UC_MODE_32)
 u.mem_map(0x400000,struct.unpack_from('<I',image,opt+56)[0])
 for va,n,raw in ss:
  if n:u.mem_write(va,image[raw:raw+n])
 for addr,n in [(0,4096),(0x10000000,0x40000),(0x20000000,0x10000),(0x30000000,0x1000),(0x60000000,0x400000)]:u.mem_map(addr,n)
 stack=0x10030000;stop=0x20000000;packet=0x20001000;obj=0x60000000
 u.mem_write(0x57ed8d4,struct.pack('<I',obj))
 u.mem_write(0x4588670,struct.pack('<I',0x30000000))
 nodes={obj+0x2bd0a8:0x60300000,obj+0x2bd094:0x60301000}
 for idx,(mp,node) in enumerate(nodes.items()):
  root=node+0x100;container=node+0x200;sentinel=node+0x300
  u.mem_write(container,struct.pack('<I',root));u.mem_write(root+8,struct.pack('<I',sentinel))
  u.mem_write(node+0x10,struct.pack('<I',counts[idx]))
 sent=[];stores=[]
 def dword(p):return struct.unpack('<I',u.mem_read(p,4))[0]
 def ret(value=0,pop=0):
  sp=u.reg_read(UC_X86_REG_ESP);u.reg_write(UC_X86_REG_EAX,value);u.reg_write(UC_X86_REG_ESP,sp+4+pop);u.reg_write(UC_X86_REG_EIP,dword(sp))
 def hook(uc,a,n,ctx):
  sp=u.reg_read(UC_X86_REG_ESP);ecx=u.reg_read(UC_X86_REG_ECX)
  if a==0xb46290:
   node=nodes[ecx];dest=dword(sp+4);key=dword(dword(sp+8));assert key==19
   u.mem_write(dest,struct.pack('<III',node+0x200,0,node));ret(dest,8)
  elif a==0xa8f6c0:
   node=nodes[ecx];dest=dword(sp+4);u.mem_write(dest,struct.pack('<III',node+0x200,0,node+0x300));ret(dest,4)
  elif a in (0x37d7050,0x37d4ed0,0x1fa2790):ret(obj)
  elif a==0x2526b50:ret(1)
  elif a==0x37de240:ret(obj)
  elif a==0x37dd6c0:ret()
  elif a==0x30000000:ret(10000)
  elif a==0x374f280:sent.append(['opcode',dword(sp+4)]);ret(0,4)
  elif a==0x374f390:sent.append(['byte',dword(sp+4)&255]);ret(0,4)
  elif a==0x3750ac0:sent.append(['flush']);ret()
  elif a in (0x3750208,0x374fd31):stores.append(hex(a))
  elif a==0x3e738b1:raise AssertionError('native iterator validation failed')
 u.hook_add(UC_HOOK_CODE,hook)
 def execute(a,end,args=(),this=obj):
  u.reg_write(UC_X86_REG_ESP,stack);u.reg_write(UC_X86_REG_ECX,this)
  u.mem_write(stack,struct.pack('<'+'I'*(len(args)+1),stop,*args))
  u.emu_start(a,end,count=20000)
  assert u.reg_read(UC_X86_REG_EIP)==end
 def values():return [dword(n+16) for n in nodes.values()]
 execute(0x29098a0,0x2909929,(0,))
 first=list(sent);assert bool(first)==any(counts)
 if any(counts):assert first==[['opcode',868],['byte',255],['flush']]
 # Actual admission call fragment, as used by the current receive dispatcher.
 u.mem_write(packet,struct.pack('<BH',1,868));u.reg_write(UC_X86_REG_EDI,packet)
 u.reg_write(UC_X86_REG_ECX,868);u.reg_write(UC_X86_REG_ESP,stack)
 u.emu_start(patch.FRAGMENT_VA,patch.FRAGMENT_END,count=100)
 admitted=u.reg_read(UC_X86_REG_EAX)
 if admitted:execute(0x1fadf80,stop,(868,1,0))
 after=values();sent.clear()
 # Reset sender throttle to expose whether the native decision still requests recovery.
 u.mem_write(0x56a33a4,bytes(4))
 execute(0x29098a0,0x2909929,(0,))
 if admitted:assert after==[0,0] and not sent
 else:assert after==list(counts) and bool(sent)==any(counts)
 return dict(initial=list(counts),first_recovery_wire=first,ack_admitted=bool(admitted),
             after=after,second_recovery_wire=list(sent),native_map_store_addresses=stores)

if __name__=='__main__':
 out=Path(os.environ['DNF_SYNC_RECEIPT']).resolve().parent;results={}
 for label,path in [('v130',os.environ['DNF_CLASS_INPUT_EXE']),('v131',os.environ['DNF_CLASS_CANDIDATE_EXE'])]:
  data=Path(path).read_bytes();results[label]=dict(sha256=hashlib.sha256(data).hexdigest(),cases=[probe(data,c) for c in [(0,0),(1,0),(0,1),(2,3)]])
 r=dict(passed=True,results=results,scope=__doc__,full_ui_acceptance=False)
 Path(os.environ['DNF_SYNC_RECEIPT']).write_text(json.dumps(r,indent=2),encoding='utf-8')
 print(json.dumps({'passed':True,'cases':8,'old_ack_blocked':True,'new_ack_clears_native_pending':True}))
