"""P153 -> cached actor -> actual town HUD percentage regression, fixed v173."""
from pathlib import Path
import sys,struct,json,importlib.util
import argparse,hashlib
from types import SimpleNamespace
import hp_aux_cache as aux_cache
import hp_realtime_lookup as patch
from hp_pe_support import PE
p=argparse.ArgumentParser(description=__doc__)
p.add_argument('--input',type=Path,required=True);p.add_argument('--receipt',type=Path,required=True)
p.add_argument('--candidate',type=Path)
a=p.parse_args();data=a.input.read_bytes()
from build_hp_realtime import PUBLISHED_PROFILES
expected=hashlib.sha256(data).hexdigest()
assert PUBLISHED_PROFILES.get(expected)==len(data)
pe=PE(data)
im=SimpleNamespace(data=data,sha256=expected,bytes_at=pe.read,
 size_of_image=struct.unpack_from('<I',data,pe.opt+56)[0],
 sections=[dict(va=pe.base+s['rva'],raw=s['raw'],raw_size=s['raw_size']) for s in pe.sections])
from unicorn import Uc,UC_ARCH_X86,UC_MODE_32,UC_HOOK_CODE
from unicorn.x86_const import *
u=Uc(UC_ARCH_X86,UC_MODE_32)
u.mem_map(0x400000,(im.size_of_image+4095)//4096*4096)
for sec in im.sections:u.mem_write(sec['va'],im.data[sec['raw']:sec['raw']+sec['raw_size']])
u.mem_map(0,0x1000);u.mem_map(0x100000,0x10000);u.mem_map(0x200000,0x20000);u.mem_map(0x7000000,0x1000)
MANAGER,SCENE,SELF,PARTY,ACTIVE,AUX,STAT,HUD,CONTEXT,MENU=range(0x100000,0x10A000,0x1000)
STACK=0x208000;STOP=0x7000000;BUFFER=0x210000;state={}
def put(a,b):u.mem_write(a,b)
def w(a,v):put(a,struct.pack('<I',v&0xffffffff))
def r(a):return struct.unpack('<I',u.mem_read(a,4))[0]
def ret(value=0,pop=0):
 sp=u.reg_read(UC_X86_REG_ESP);target=r(sp)
 u.reg_write(UC_X86_REG_EAX,value);u.reg_write(UC_X86_REG_ECX,0xcafebabe);u.reg_write(UC_X86_REG_EDX,0xfaceb00c)
 u.reg_write(UC_X86_REG_ESP,sp+4+pop);u.reg_write(UC_X86_REG_EIP,target)
def hook(_u,a,_n,_):
 if a==STOP or (state.get('mode')=='ui' and a==0x224b533):u.emu_stop();return
 if a==0x296d730:
  key=r(u.reg_read(UC_X86_REG_ESP)+4);ret(ACTIVE if state['variant']=='active' and key==20 else 0);return
 if a==0x1fa2790:ret(CONTEXT);return
 if a==0x2526b50:ret(3 if state['variant']=='dungeon' else 1);return
 if a==0x18c2800:ret(int(state['variant']=='arena'));return
 if a in (0x37e3940,0x37e3970):ret();return
 if a in (0x37df550,0x37df5c0,0x37df630,0x37df6a0,0x37df7f0):
  sp=u.reg_read(UC_X86_REG_ESP);ctx=u.reg_read(UC_X86_REG_ECX)
  assert ctx in (0x56a1e80,0x56a1e74);w(r(sp+8),5 if ctx==0x56a1e80 else 2);ret(pop=8);return
 if a==0x3e738b1:raise AssertionError('invalid native container')
u.hook_add(UC_HOOK_CODE,hook)
def run(start,ecx=0,args=(),mode='call'):
 state['mode']=mode
 for reg in (UC_X86_REG_EAX,UC_X86_REG_EDX,UC_X86_REG_EBX,UC_X86_REG_ESI,UC_X86_REG_EDI):u.reg_write(reg,0)
 u.reg_write(UC_X86_REG_ECX,ecx);u.reg_write(UC_X86_REG_EBP,STACK+0x2000);u.reg_write(UC_X86_REG_ESP,STACK)
 u.reg_write(UC_X86_REG_EFLAGS,0x202);w(STACK,STOP)
 for i,a in enumerate(args):w(STACK+4+i*4,a)
 try:u.emu_start(start,0,count=25000)
 except Exception:
  print('native failure',state,hex(u.reg_read(UC_X86_REG_EIP)));raise
 assert u.reg_read(UC_X86_REG_EIP) in (STOP,0x224b533)
 return u.reg_read(UC_X86_REG_EAX)
original=im.bytes_at(0x203323d,5);helper=patch.emit()
patched_call=b'\xe8'+struct.pack('<i',patch.BASE-0x2033242)
candidate_sha=None
if a.candidate:
 candidate_data=a.candidate.read_bytes();candidate=PE(candidate_data)
 assert len(candidate_data)==len(data)
 assert candidate.read(patch.BASE,len(helper))==helper
 assert candidate.read(0x203323d,5)==patched_call
 for va,size in ((0x6ec1000,0x600),(0x20331e0,0x5d),(0x2033242,0x28),(0x224b50e,0x25)):
  assert candidate.read(va,size)==pe.read(va,size)
 helper=candidate.read(patch.BASE,len(helper));patched_call=candidate.read(0x203323d,5)
 candidate_sha=hashlib.sha256(candidate_data).hexdigest()
assert original==b'\xe8'+struct.pack('<i',0x296d730-0x2033242)
assert im.bytes_at(patch.BASE,len(helper))==bytes(len(helper))
put(patch.BASE,helper)
rows=[]
for variant in ('aux','active','left_party','self_missing','wrong_owner','wrong_route','wrong_actor_key','wrong_actor_owner','wrong_actor_route','no_stats','cache_miss','no_scene','no_self','arena','dungeon'):
 for hp in (0,73,100):
  for patched in (False,True):
   state['variant']=variant
   for address in range(MANAGER,MENU+0x1000,0x1000):put(address,bytes(4096))
   w(0x56a3758,0 if variant=='no_scene' else SCENE);w(0x56a3768,MANAGER)
   w(SCENE+0x70,0 if variant=='no_self' else SELF);w(SELF+0x20,19);w(SELF+0x4c8,0 if variant=='left_party' else PARTY)
   for slot in range(8):w(PARTY+0x2c+4*slot,0xffff)
   w(PARTY+0x2c,0xffff if variant=='self_missing' else 19);w(PARTY+0x30,20)
   w(PARTY+0x70,3 if variant=='wrong_route' else 2);w(PARTY+0x74,6 if variant=='wrong_owner' else 0)
   if variant=='active':w(MANAGER+0x4c,ACTIVE)
   for actor in (ACTIVE,AUX):
    w(actor+0x20,21 if variant=='wrong_actor_key' else 20)
    w(actor+0x604,0x206 if variant=='wrong_actor_owner' else 0x305 if variant=='wrong_actor_route' else 0x205)
    w(actor+0x4f8,0 if variant=='no_stats' else STAT)
    w(actor+0x34c,42)
   cache={} if variant=='cache_miss' else {aux_cache.identity(20,5,2):AUX}
   aux_cache.install(u,put,w,{4:cache},{a:im.bytes_at(a,size) for a,size in aux_cache.RANGES.items()})
   put(0x203323d,patched_call if patched else original)
   u.ctl_remove_cache(0x20331e0,0x203326a)
   body=struct.pack('<BHBBB',1,20,hp,1,0);put(BUFFER,body);w(0x57f9ad0,BUFFER);w(0x57f9ad4,len(body))
   run(0x20331e0)
   assert r(0x57f9ad0)==BUFFER+len(body) and r(0x57f9ad4)==0
   actor=ACTIVE if variant=='active' else AUX
   want=hp if variant=='active' or patched and variant=='aux' else 42
   assert r(actor+0x34c)==want,(variant,hp,patched,r(actor+0x34c))
   if variant in ('active','aux'):
    hudActor=run(0x6ec1000,MANAGER,(1,));assert hudActor==actor
    run(0x224b50e,actor,mode='ui');assert r(STACK+0x2000-0x680)==want
   rows.append(dict(variant=variant,packetHP=hp,patched=patched,descriptorHP=r(actor+0x34c),expected=want))
out=dict(passed=True,sourceSHA256=im.sha256,candidateSha256=candidate_sha,cases=rows,helper=helper.hex(),
 finding='The current P153 active-only lookup skips the auxiliary actor that the ordinary-town HUD actually draws.',
 actual=['P153 receiver and byte readers','current HUD lookup and native auxiliary hash lookup','descriptor HP write','HUD percentage read/arithmetic'],
 fixtures=['active actor lookup','namespace decoder and locks','scene state singleton'],gameAcceptance=False)
with a.receipt.open('x',encoding='utf8') as f:json.dump(out,f,indent=2)
print(json.dumps(dict(passed=True,cases=len(rows))))
