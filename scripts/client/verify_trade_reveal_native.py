from pathlib import Path
import sys,struct,json,hashlib,argparse
p=argparse.ArgumentParser(description='Replay native trade state and drawing guard; no live process access')
p.add_argument('--deps',type=Path,required=True);p.add_argument('--input',type=Path,required=True)
p.add_argument('--candidate',type=Path,required=True);p.add_argument('--receipt',type=Path,required=True)
options=p.parse_args();sys.path.insert(0,str(options.deps))
from unicorn import Uc,UC_ARCH_X86,UC_MODE_32,UC_HOOK_CODE
from unicorn.x86_const import *
import patch_trade_reveal_v125 as patch
OLD,NEW=options.input,options.candidate
expected,metadata=patch.build(OLD.read_bytes())
assert NEW.read_bytes()==expected, 'candidate differs from exact recipe'
del expected

def probe(path):
 b=path.read_bytes();pe=struct.unpack_from('<I',b,0x3c)[0];opt=pe+24;table=opt+struct.unpack_from('<H',b,pe+20)[0];base=struct.unpack_from('<I',b,opt+28)[0]
 u=Uc(UC_ARCH_X86,UC_MODE_32);u.mem_map(base,(struct.unpack_from('<I',b,opt+56)[0]+4095)//4096*4096)
 for i in range(struct.unpack_from('<H',b,pe+6)[0]):
  at=table+40*i;rva,n,raw=struct.unpack_from('<III',b,at+12)
  if n:u.mem_write(base+rva,b[raw:raw+n])
 stack,stop,heap=0x10000000,0x20000000,0x60000000
 for at,n in [(stack,0x10000),(stop,4096),(heap,0x10000)]:u.mem_map(at,n)
 manager,ui=heap+0x1000,heap+0x2000
 put=lambda p,v:u.mem_write(p,struct.pack('<I',v))
 get=lambda p:struct.unpack('<I',u.mem_read(p,4))[0]
 def ret(value=0,pop=0):
  sp=u.reg_read(UC_X86_REG_ESP);target=get(sp);u.reg_write(UC_X86_REG_EAX,value);u.reg_write(UC_X86_REG_ESP,sp+4+pop);u.reg_write(UC_X86_REG_EIP,target)
 events=[];partner=[1];draw=[False]
 def hook(uc,at,n,ctx):
  sp=uc.reg_read(UC_X86_REG_ESP)
  if at in (0x332263f,0x33227ba) and draw[0]:uc.emu_stop();return
  if at==0x32b4c00:ret(ui)
  elif at==0x331daf0:ret(get(ui+0x260))
  elif at in (0x3553560,0x32c7db0):ret()
  elif at==0x296d730:
   events.append(dict(kind='partner_lookup',id=get(sp+4),present=partner[0]));ret(partner[0])
  elif at in (0x32bea50,0x32b94a0):
   events.append(dict(kind='control',id=get(sp+4),value=get(sp+8)));ret(0,12)
  elif at==0x25f3f10:ret()
 u.hook_add(UC_HOOK_CODE,hook);put(0x56a37cc,manager);put(0x56a3798,ui)
 def mask():
  draw[0]=True;u.reg_write(UC_X86_REG_EBX,1);u.reg_write(UC_X86_REG_ESP,stack+0x7000)
  u.emu_start(0x3322623,stop,count=100)
  pc=u.reg_read(UC_X86_REG_EIP);draw[0]=False;assert pc in (0x332263f,0x33227ba)
  return pc==0x332263f
 def reset():
  u.mem_write(manager,b'\0'*0x100);put(manager+4,162);put(ui+0x260,0);partner[0]=1;events.clear()
 def update(other,state):
  sp=stack+0x8000;u.mem_write(sp,struct.pack('<III',stop,int(other),state));u.reg_write(UC_X86_REG_ESP,sp);u.reg_write(UC_X86_REG_ECX,manager)
  u.emu_start(0x2606120,stop,count=2000);assert u.reg_read(UC_X86_REG_EIP)==stop and u.reg_read(UC_X86_REG_ESP)==sp+12
  return dict(states=[get(manager+8),get(manager+12)],flag=u.mem_read(manager+0x54,1)[0],phase=get(manager+0x50),peer_mask=mask())
 cases=[]
 for name,sequence in [('local_first',[(0,1),(1,1)]),('peer_first',[(1,1),(0,1)]),('local_pending',[(0,5),(1,1),(0,1)]),('consent_and_unlock',[(0,1),(1,1),(0,3),(1,0),(1,1),(0,1)])]:
  reset();rows=[]
  for other,state in sequence:rows.append(dict(other=other,state=state,**update(other,state)))
  cases.append(dict(name=name,steps=rows,events=events.copy()))
 reset();partner[0]=0;update(0,1);missing=update(1,1)
 reset();update(0,1);update(1,1);sp=stack+0x8000;put(sp,stop);u.reg_write(UC_X86_REG_ESP,sp);u.reg_write(UC_X86_REG_ECX,manager);u.emu_start(0x26060d0,stop,count=1000)
 assert u.reg_read(UC_X86_REG_EIP)==stop
 cancelled=dict(states=[get(manager+8),get(manager+12)],flag=u.mem_read(manager+0x54,1)[0],peer_mask=mask())
 return dict(sha256=hashlib.sha256(b).hexdigest(),cases=cases,missing_partner=missing,cancel_reset=cancelled)

old,new=probe(OLD),probe(NEW)
assert all(c['steps'][-1]['peer_mask'] for c in old['cases'][:3]),old
for c in new['cases'][:3]:
 assert c['steps'][-1]['flag']==1 and not c['steps'][-1]['peer_mask'],c
 assert all(x['flag']==0 and x['peer_mask'] for x in c['steps'][:-1]),c
rows=new['cases'][3]['steps'];assert not rows[2]['peer_mask'] and rows[3]['peer_mask'] and not rows[-1]['peer_mask']
assert new['missing_partner']['flag']==0 and new['missing_partner']['peer_mask']
assert new['cancel_reset']==dict(states=[0,0],flag=0,peer_mask=True)
for a,c in zip(old['cases'],new['cases']):
 assert a['events']==c['events']
 assert [x['states']for x in a['steps']]==[x['states']for x in c['steps']]
 assert [x['phase']for x in a['steps']]==[x['phase']for x in c['steps']]
out=dict(passed=True,old=old,new=new,scope='Original native 2606120 state branch and 3322623 draw guard; UI context/control and partner lookup are explicit fixtures; native 1cc7030, 38103d0, 26060d0 execute; no complete GUI acceptance')
options.receipt.write_text(json.dumps(out,indent=2),encoding='utf-8');print(json.dumps(dict(passed=True,old_masked=True,new_reveals_after_both_locks=True,new_sha256=new['sha256'])))
