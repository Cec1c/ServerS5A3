"""Execute the current native event-list filter and row renderer in isolation.

Fixtures replace the tree widget, STL iterator construction, sprite lookup and
graphics sinks. The native visibility test, title selection and type-to-frame
selection execute unmodified. This is not a screen or live-click acceptance.
"""
from pathlib import Path
import hashlib,json,struct,sys
import pefile
from unicorn import Uc,UC_ARCH_X86,UC_MODE_32,UC_HOOK_CODE
from unicorn.x86_const import *
import argparse
sys.path.insert(0,str(Path(__file__).resolve().parent/'client'))
from client.patch_online_panel_v146 import build
parser=argparse.ArgumentParser(description=__doc__)
parser.add_argument('--exe',type=Path,required=True)
parser.add_argument('--candidate',type=Path,required=True)
parser.add_argument('--receipt',type=Path,required=True)
args=parser.parse_args()
assert not args.receipt.exists()
before=args.exe.read_bytes();after=args.candidate.read_bytes()
assert build(before)[0]==after
def exercise(b):
 pe=pefile.PE(data=b,fast_load=True);vm=Uc(UC_ARCH_X86,UC_MODE_32)
 base=pe.OPTIONAL_HEADER.ImageBase;vm.mem_map(base,(pe.OPTIONAL_HEADER.SizeOfImage+4095)//4096*4096)
 for s in pe.sections:vm.mem_write(base+s.VirtualAddress,s.get_data())
 vm.mem_map(0,4096);vm.mem_map(0x10000000,0x40000);vm.mem_map(0x20000000,0x40000)
 SP=0x10020000;STOP=0x10001000;STUB=0x10002000
 CTX=0x20001000;VEC=0x20002000;PROXY=0x20002100;ENTRY=0x20003000;UI=0x20005000;EMPTY=0x20006000
 RESOURCE=0x20007000;RESOURCE_VT=0x20007100;FRAME=0x20008000;DRAW=0x20009000;LOOKUP=0x2000a000
 state={}
 def r(a):return struct.unpack('<I',vm.mem_read(a,4))[0]
 def w(a,v):vm.mem_write(a,struct.pack('<I',v&0xffffffff))
 def ret(v=0,clean=0):
  sp=vm.reg_read(UC_X86_REG_ESP);vm.reg_write(UC_X86_REG_EAX,v);vm.reg_write(UC_X86_REG_ESP,sp+4+clean);vm.reg_write(UC_X86_REG_EIP,r(sp))
 def wide(a):
  out=bytearray()
  for i in range(256):
   pair=bytes(vm.mem_read(a+i*2,2))
   if pair==b'\0\0':return out.decode('utf-16le')
   out.extend(pair)
  raise AssertionError('UNBOUNDED_TITLE')
 def hook(u,at,size,unused):
  sp=u.reg_read(UC_X86_REG_ESP)
  if at==STOP:u.emu_stop();return
  if at==0x32bea50:
   state['opened'].append(r(sp+4));ret(0,12);return
  if at==0x32b55e0:ret(0,4);return
  if at==0x3e738b1:raise AssertionError('NATIVE_FAIL_FAST')
  if at in (0x224e5f0,0xa8f6c0):
   out=r(sp+4);w(out,PROXY);w(out+4,0);w(out+8,ENTRY if at==0x224e5f0 else ENTRY+0x16c);ret(out,4);return
  if at==0x38756a0:ret();return
  if at==0x38759b0:
   title=wide(r(sp+8));owner=r(sp+16)
   if title:state['rows'].append(dict(title=title,owner=owner))
   ret(0,24);return
  if at==0x38736e0:ret(0,8);return
  if at==0x3873640:ret(len(state['rows']),4);return
  if at==0x37de240:ret(EMPTY);return
  if at==STUB:
   out=r(sp+4);w(out,RESOURCE);w(out+4,0);ret(out,8);return
  if at==0x38080c0:
   out=r(sp+4);frame=r(sp+8);state['sprite_frames'].append(frame);assert frame<(15 if len(state['sprite_frames'])==1 else 4),'INVALID_SPRITE_FRAME'
   w(out,FRAME);w(out+4,0);ret(out,8);return
  if at==0x1e93430:
   target=u.reg_read(UC_X86_REG_ECX);source=r(sp+4);u.mem_write(target,bytes(u.mem_read(source,8)));ret(target,4);return
  if at==0x37f3b00:state['image_draws']+=1;ret(0,16);return
  if at==STUB+0x10:ret(0,4);return
  if at==STUB+0x20:state['text_draws'].append(wide(r(sp+16)));ret(0,16);return
  if at==STUB+0x30:ret();return
  if at==0x3e741d2:ret(1);return
  if at==0x3e72ce4:ret();return
 vm.hook_add(UC_HOOK_CODE,hook)
 def run(at,ecx,args):
  for reg in (UC_X86_REG_EAX,UC_X86_REG_EBX,UC_X86_REG_EDX,UC_X86_REG_ESI,UC_X86_REG_EDI,UC_X86_REG_EBP):vm.reg_write(reg,0)
  vm.reg_write(UC_X86_REG_ECX,ecx);vm.reg_write(UC_X86_REG_ESP,SP);vm.reg_write(UC_X86_REG_EFLAGS,0x202)
  w(SP,STOP)
  for i,arg in enumerate(args):w(SP+4+i*4,arg)
  vm.emu_start(at,0,count=100000);assert vm.reg_read(UC_X86_REG_EIP)==STOP
 def case(title,presentation,tail):
  state.clear();state.update(rows=[],sprite_frames=[],image_draws=0,text_draws=[])
  vm.mem_write(ENTRY,b'\0'*0x16c);w(ENTRY,2347);w(ENTRY+8,presentation);vm.mem_write(ENTRY+0xb4,bytes([tail]));w(ENTRY+0x44,7)
  vm.mem_write(ENTRY+0x30,title.encode('utf-16le')+b'\0\0');w(ENTRY+0x40,len(title))
  w(CTX,VEC);w(VEC,PROXY);w(VEC+4,ENTRY);w(VEC+8,ENTRY+0x16c);w(PROXY,VEC)
  run(0x23edb30,CTX,[UI,0])
  if state['rows']:
   w(DRAW,100);w(DRAW+4,200);w(DRAW+12,ENTRY+0x30);w(DRAW+20,0xffffffff);w(DRAW+24,ENTRY)
   run(0x23ed810,0,[DRAW])
  return json.loads(json.dumps(state))
 w(LOOKUP,RESOURCE_VT);w(RESOURCE_VT+8,STUB);w(0x581a3e4,LOOKUP)
 w(0x581a298,UI);w(UI,RESOURCE_VT);w(RESOURCE_VT+0x64,STUB+0x10);w(RESOURCE_VT+0x78,STUB+0x20);w(RESOURCE_VT+0x68,STUB+0x30)
 vm.mem_write(FRAME+0x2e,struct.pack('<H',39))
 rows=[]
 for presentation in (1,2,3,4,5,6,7,0,8,255,0xffffffff):
  try:
   result=case('累计在线奖励',presentation,1)
   rows.append(dict(presentation=presentation,passed=True,result=result))
  except AssertionError as error:
   rows.append(dict(presentation=presentation,passed=False,error=str(error)))
 clicks=[]
 for event in (0,228,331,2037,2347,2449,65535):
  for window in (0,39,0x134,0x138,0x45c):
   for flag in (0,1):
    state.clear();state.update(opened=[])
    vm.mem_write(ENTRY,b'\0'*0x16c)
    w(ENTRY,event);w(ENTRY+0x140,window);w(CTX+4,UI)
    try:run(0x23edfa0,CTX,[ENTRY,flag])
    except Exception as error:raise AssertionError(dict(event=event,window=window,flag=flag,eip=hex(vm.reg_read(UC_X86_REG_EIP)),ecx=hex(vm.reg_read(UC_X86_REG_ECX)))) from error
    clicks.append(dict(event=event,window=window,flag=flag,opened=list(state['opened'])))
 return dict(module_sha256=hashlib.sha256(b).hexdigest(),rows=rows,clicks=clicks)

old=exercise(before);new=exercise(after)
for a,b in zip(old['rows'],new['rows']):
 if 1<=a['presentation']<=4:
  assert a['passed'] and b['passed'] and a['result']==b['result']
 else:
  assert not a['passed'] and 'INVALID_SPRITE_FRAME' in a['error']
  assert b['passed'] and b['result']['sprite_frames']==[0]
  assert b['result']['text_draws']==['累计在线奖励']
for a,b in zip(old['clicks'],new['clicks']):
 if a['event']==228:assert b['opened']==[39]
 else:assert a==b
receipt=dict(passed=True,input=old,candidate=new,
 native_executed=['full activity click handler until native window-open entry','event-list filter and row renderer','valid tag selection','bounded invalid tag handling'],
 fixtures=['STL iterator construction','CNTree widget','sprite lookup/frame constructor','graphics sinks','date conversion','window opener returns null after recording requested ID'],
 reproduced_invalid_metadata_failure=True,historical_crash_root_cause_proven=False,screen_acceptance=False,live_click_acceptance=False)
with args.receipt.open('x',encoding='utf-8') as stream:json.dump(receipt,stream,ensure_ascii=False,indent=2)
print(json.dumps(dict(passed=True,row_cases=len(new['rows']),click_cases=len(new['clicks']),module_sha256=new['module_sha256'])))
