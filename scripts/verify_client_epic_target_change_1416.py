"""Offline fixed-client 1416 gate/receiver execution; no production writes."""
from pathlib import Path
import argparse,struct,json,hashlib,sys
from unicorn import Uc,UC_ARCH_X86,UC_MODE_32,UC_HOOK_CODE
from unicorn.x86_const import *
p=argparse.ArgumentParser(description=__doc__)
p.add_argument('--before',type=Path,required=True);p.add_argument('--exe',type=Path,required=True);p.add_argument('--receipt',type=Path,required=True);args=p.parse_args()
b=args.exe.read_bytes();old=args.before.read_bytes()
assert hashlib.sha256(old).hexdigest()=='b5b4642f3988155f35fc7ec40d9b1e7ea5a07e8eb0a8cba4087e2e68b5d1bad9'
assert hashlib.sha256(b).hexdigest()=='b76f38da5bf75c479cb05bacd28a02574900b82f01270abbc6e433e1c2355b68'
assert len(old)==len(b)==111779840
assert [i for i,(x,y) in enumerate(zip(old,b)) if x!=y]==[111748017]
assert old[111748017]==6 and b[111748017]==7
r=dict(candidate_sha256=hashlib.sha256(b).hexdigest(),old_byte=6,new_byte=7,gate_va='0x6e83610')
pe=struct.unpack_from('<I',b,0x3c)[0];opt=pe+24;tab=opt+struct.unpack_from('<H',b,pe+20)[0];base=struct.unpack_from('<I',b,opt+28)[0];size=struct.unpack_from('<I',b,opt+56)[0]
u=Uc(UC_ARCH_X86,UC_MODE_32);u.mem_map(base,(size+4095)//4096*4096);u.mem_write(base,b[:4096])
for i in range(struct.unpack_from('<H',b,pe+6)[0]):
 q=tab+i*40;vs,rv,rs,rp=struct.unpack_from('<IIII',b,q+8);u.mem_write(base+rv,b[rp:rp+rs])
for va,sz in [(0,4096),(0x10000000,0x20000),(0x20000000,0x10000),(0x30000000,4096)]:u.mem_map(va,sz)
w=lambda a,v:u.mem_write(a,struct.pack('<I',v&0xffffffff));rd=lambda a:struct.unpack('<I',u.mem_read(a,4))[0]
stack=0x10010000;stop=0x30000000;window=0x20001000;pane=0x20002000;buffer=0x20003000
state=dict(mode='gate',window=True,calls=[])
def ret(pop=0,value=0):
 sp=u.reg_read(UC_X86_REG_ESP);target=rd(sp);u.reg_write(UC_X86_REG_EAX,value);u.reg_write(UC_X86_REG_ESP,sp+4+pop);u.reg_write(UC_X86_REG_EIP,target)
def hook(uc,address,size,user):
 if address==stop:u.emu_stop();return
 if state['mode']=='gate':return
 sp=u.reg_read(UC_X86_REG_ESP)
 if address==0x32b55e0:
  widget=rd(sp+4);state['calls'].append(dict(call='window_exists',widget=widget));ret(4,1 if state['window'] and widget==0x29c else 0);return
 if address==0x32b5c90:assert rd(sp+4)==0x29c;ret(4,window);return
 if address==0x3e73cb6:ret(value=window);return
 if address==0x1174560:state['calls'].append(dict(call='close_epic_window'));ret();return
 if address==0x1c6b5a0:assert u.reg_read(UC_X86_REG_ECX)==window;ret(value=pane);return
 if address in (0x1168870,0x1161310):
  assert u.reg_read(UC_X86_REG_ECX)==pane;state['calls'].append(dict(call=hex(address),target=rd(0x5656e8c)));ret();return
 if address==0x39a6620:state['calls'].append(dict(call='error_string',id=rd(sp+4)));ret(value=0x20004000);return
 if address==0x32bea50:state['calls'].append(dict(call='error_notice'));ret(12);return
 if address==0x3e72ce4:ret();return
u.hook_add(UC_HOOK_CODE,hook)
gate=int(r['gate_va'],16);bitmap=0x6e92300;delta=[]
for opcode in range(65536):
 values=[]
 for byte_value in (r['old_byte'],r['new_byte']):
  u.mem_write(bitmap+1416//8,bytes([byte_value]));w(stack,stop);w(stack+4,opcode);u.reg_write(UC_X86_REG_ESP,stack);u.emu_start(gate,0,count=30);assert u.reg_read(UC_X86_REG_ESP)==stack+8;values.append(u.reg_read(UC_X86_REG_EAX))
 if values[0]!=values[1]:delta.append(opcode)
assert delta==[1416]
rows=[]
for target in [101020693,101030760,101040510]:
 for exists in [True,False]:
  for patched in [False,True]:
   state.update(mode='gate',window=exists,calls=[]);w(0x5656e8c,101040510);w(0x5656e90,1000);w(0x5656eb8,1);w(0x57f9ad0,buffer);w(0x57f9ad4,4);w(buffer,target)
   u.mem_write(bitmap+1416//8,bytes([r['new_byte'] if patched else r['old_byte']]));w(stack,stop);w(stack+4,1416);u.reg_write(UC_X86_REG_ESP,stack);u.emu_start(gate,0,count=30);admit=u.reg_read(UC_X86_REG_EAX);assert admit==int(patched)
   if admit:
    state['mode']='handler';w(stack,stop);w(stack+4,0);w(stack+8,1);w(stack+12,0);u.reg_write(UC_X86_REG_ESP,stack);u.emu_start(0xb210d0,0,count=2000);assert u.reg_read(UC_X86_REG_EIP)==stop and u.reg_read(UC_X86_REG_ESP)==stack+4
   assert rd(0x5656e8c)==(target if patched else 101040510) and rd(0x5656e90)==1000
   assert rd(0x57f9ad4)==(0 if patched else 4)
   refresh=[c for c in state['calls'] if c['call'] in ['0x1168870','0x1161310']]
   assert len(refresh)==(2 if patched and exists else 0) and all(x['target']==target for x in refresh)
   rows.append(dict(status=1,target=target,window_present=exists,patched=patched,admitted=bool(admit),target_after=rd(0x5656e8c),reader_remaining=rd(0x57f9ad4),refresh=refresh,passed=True))
for code in [3,4,18,22,123]:
 for exists in [True,False]:
  state.update(mode='handler',window=exists,calls=[]);w(0x5656e8c,101040510);w(0x5656e90,1000);w(0x5656eb8,1);w(0x57f9ad0,buffer);w(0x57f9ad4,0)
  w(stack,stop);w(stack+4,0);w(stack+8,0);w(stack+12,code);u.reg_write(UC_X86_REG_ESP,stack);u.emu_start(0xb210d0,0,count=2000);assert u.reg_read(UC_X86_REG_EIP)==stop and u.reg_read(UC_X86_REG_ESP)==stack+4
  assert rd(0x5656e8c)==101040510 and rd(0x5656e90)==1000 and rd(0x57f9ad4)==0
  assert not any(x['call'] in ['0x1168870','0x1161310'] for x in state['calls']) and any(x['call']=='error_notice' for x in state['calls'])==(code in [3,4,22,123]),(code,exists,state['calls'])
  rows.append(dict(status=0,error_code=code,window_present=exists,target_after=rd(0x5656e8c),calls=state['calls'],passed=True))
receipt=dict(passed=True,exe_sha256=r['candidate_sha256'],compared_opcodes=65536,newly_admitted_opcodes=delta,receiver='0xb210d0',reader='0x37e83c0',refresh_wrapper='0x116a4b0',cases=rows,scope='Actual native filter, COMMAND1416 receiver, u32 packet reader, singleton state assignment and refresh wrapper. GUI lookup, close, RTTI, final refresh rendering and error-message dependencies stubbed. Gate and receiver composed by harness, not full dispatcher. No first-selection or native UI acceptance claim.',native_acceptance=False)
args.receipt.write_text(json.dumps(receipt,indent=2),encoding='utf-8');print(json.dumps(dict(passed=True,gate_cases=65536,receiver_cases=len(rows),sha256=r['candidate_sha256'])))

