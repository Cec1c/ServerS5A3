"""Run compiled x86 plus native dispatch/ABI in isolation; no game process."""
from pathlib import Path
import os
import sys,json,struct,re,hashlib,importlib.util
S=Path(__file__).parent

sys.path.insert(0,str(S.parents[1]/'keyrepeat-native/client'))
spec=importlib.util.spec_from_file_location('previous_native_harness',str(S.parents[1]/'keyrepeat-native/client/verify_native.py'))
previous=importlib.util.module_from_spec(spec);spec.loader.exec_module(previous);Machine=previous.Machine
from unicorn.x86_const import *
def make():
 m=Machine(S/'build/client.dll',S/'build/client.map',Path(os.environ['CLIENT_FEATURE_CANDIDATE']))
 import pefile
 lib=pefile.PE(str(S/'build/client.dll'))
 for sec in lib.sections:
  if sec.Misc_VirtualSize>sec.SizeOfRawData:m.put(lib.OPTIONAL_HEADER.ImageBase+sec.VirtualAddress+sec.SizeOfRawData,bytes(sec.Misc_VirtualSize-sec.SizeOfRawData))
 def query():
  b=m.arg(1);m.put(b,struct.pack('<IIIIIII',0,0,4,0x40000000,0x1000,4,0));return 28
 m.iat(0x6e85b18,query,12)
 return m
binding={k:int(v,16) for k,v in re.findall(r'#define (OLD_\w+) (0x[0-9a-f]+)',(S/'binding.h').read_text())}
cases=[]
def case(n):cases.append(n)
def verify():
 m=make();m.put(m.g['loaded'],b'\1');seen=[]
 for key,pop in [('OLD_MENU_ENABLED',0),('OLD_MENU_SELECTED',4),('OLD_MENU_EVENT',16)]:m.mock(binding[key],lambda:seen.append(tuple(m.arg(i) for i in range(1))) or 0x12345670,pop)
 for typ in range(61):
  result=m.call(m.exports['MenuEnabled'],(typ,))&255
  assert result==(1 if typ in (49,50) else 0x70),(typ,result)
  result=m.call(m.exports['MenuSelected'],(typ,),4,ecx=m.S+0x1000)&255
  assert result==(1 if typ in (49,50) else 0x70)
  case('menu-state-'+str(typ))
 assert m.call(m.exports['MenuEvent'],(42,23,99,123),16,ecx=m.S)==0x12345670;case('unrelated-menu-event-ABI')
 # Both added-menu branch gates forward the prior exact handler for every
 # old type, including the original 47 sentinel and repeat's 48 extension.
 m=make()
 for fn,old,accepted in [('MenuCheckGate','OLD_MENU_CHECK',0x1227cef),('MenuReflowGate','OLD_MENU_REFLOW',0x1228c13)]:
  for typ in [0,46,47,48,49,50,999]:
   obj=m.S+0x1000;m.write(obj+0x3c,typ);seen=[]
   m.mock(binding[old],lambda:seen.append('old') or 11);m.mock(accepted,lambda:seen.append('new') or 22)
   m.put(m.STACK,struct.pack('<I',m.STOP));m.u.reg_write(UC_X86_REG_ESP,m.STACK);m.u.reg_write(UC_X86_REG_EBX,obj)
   m.u.emu_start(m.exports[fn],m.STOP+1,count=10000)
   assert seen==(['new'] if typ in (49,50) else ['old']);assert m.u.reg_read(UC_X86_REG_ESP)==m.STACK+4;case(fn+'-'+str(typ))
 # Exact class0/op7f30 admission; previous growth classifier executes too.
 m=make()
 for cls,op,new in [(0,0x7f30,True),(1,0x7f30,False),(0,0x7f20,False),(0,47,False),(2,0x7f30,False)]:
  m.put(m.S,bytes([cls]));m.mock(0x7101bff,13,4);m.put(m.STACK,struct.pack('<II',m.STOP,op));m.u.reg_write(UC_X86_REG_ESP,m.STACK);m.u.reg_write(UC_X86_REG_EDI,m.S)
  m.u.emu_start(m.exports['FeatureGate'],m.STOP+1,count=10000);assert m.u.reg_read(UC_X86_REG_EAX)==(1 if new else 13);assert m.u.reg_read(UC_X86_REG_ESP)==m.STACK+8;case('private-admission-'+str((cls,op)))
 # The bridge must pass native thiscall arguments without spoofing item data
 # or bypassing the native pickup function. Its callee sees a native return.
 m=make();bridge=0x1edc163;m.put(bridge,m.native(bridge,13));seen=[]
 def pickup():
  seen.append((m.u.reg_read(UC_X86_REG_ECX),m.arg(),m.arg(1),m.read(m.u.reg_read(UC_X86_REG_ESP))))
  return 1
 m.mock(0x25f4970,pickup,8);m.call(bridge,(0x12345678,1),8,ecx=m.S)
 assert seen==[(m.S,0x12345678,1,0x1edc16f)];case('native-pickup-bridge-ABI')
 # Native RDAR currently returns early when its training UI is absent. The
 # compiled observer still records the real event, while that native return
 # and all seven original arguments retain their ABI.
 m=make();m.put(0x1edc0a0,m.native(0x1edc0a0,0xc3));m.write(0x567d43c,0);m.write(0x567d440,0)
 src=m.S+0x10000;vt=m.S+0x20000;name=m.S+0x30000;victim=m.S+0x40000;m.write(src,vt);m.write(victim,vt)
 m.put(name,'测试角色\0'.encode('utf-16le'));m.mock(0x78f6a0,0x9999)
 m.write(vt+0x4f8,m.S+0x50000);m.mock(m.S+0x50000,src)
 m.write(vt+0x3c,m.S+0x50020);m.mock(m.S+0x50020,1,4)
 m.write(vt+0x3ac,m.S+0x50040);m.mock(m.S+0x50040,name)
 m.now=1000;m.call(0x1edc0a0,(src,victim,1,10000,0,77,0))
 row=m.g['damageRows'];assert m.wide(row)=='测试角色';assert m.read(row+128)==10000;case('real-damage-event-before-training-gate')
 for lo,hi,want in [(0,0,10000),(0xffffffff,0xffffffff,10000),(0,2,(2<<32)+10000)]:
  m.now+=1000;m.call(0x1edc0a0,(src,victim,1,lo,hi,77,0));got=m.read(row+128)|(m.read(row+132)<<32);assert got==want;case('damage-64bit-'+str((lo,hi)))
 m.mock(m.S+0x50020,0,4);m.call(0x1edc0a0,(src,victim,1,33,0,77,0));assert m.read(row+128)==10000;case('non-player-damage-excluded')
 # Full scan repeat operates on the actual raw device bytes, keeping keypad
 # Enter separate from Return and preserving every unconfigured scan.
 m=make();obj=m.S+0x10000;m.put(obj,bytes(0x1400));m.put(obj+4,b'\1');m.put(obj+12,b'\1');m.write(0x584678c,9);m.write(0x5846770,9)
 m.mock(0x29610e0,m.S+0x8000);m.put(0x3840a30,m.native(0x3840a30,0x24));m.put(m.g['loaded'],b'\1')
 config=struct.pack('<II',0x32524b44,2)+bytes(1 if i in [30,156] else 0 for i in range(256));m.put(m.g['saved'],config)
 def sample(t,keys,result=0):
  raw=bytearray([0x35]*256)
  for scan in [30,156]:raw[scan]=128 if scan in keys else 0
  m.put(obj+0x10d,raw);m.now=t;m.call(m.exports['InputGate'],ecx=obj,eax=result)
  got=bytes(m.u.mem_read(obj+0x10d,256));assert all(got[i]==raw[i] for i in range(256) if i not in [30,156]);return got
 for t,on in [(100,128),(101,0),(179,0),(180,128),(181,0),(260,128)]:
  b=sample(t,{30,156});assert b[30]==b[156]==on;assert b[28]==0x35;case('physical-repeat-'+str(t))
 b=sample(261,{156});assert b[30]==0;case('physical-release-stops')
 m.write(obj+0x1310,1);b=sample(300,{156});assert b[156]==128;case('chat-input-pass-through')
 m.write(obj+0x1310,0);b=sample(400,{156});assert b[156]==0;sample(401,set());b=sample(402,{156});assert b[156]==128;case('chat-resume-requires-release')
 # Every physical key executes the compiled event and native activity-vector
 # ownership check. Only control lookup/draw/OS storage endpoints are adapters.
 m=make();P=m.g['panels'];W=m.S+0x10000;M=m.S+0x20000;R=m.S+0x40000;V=m.S+0x50000
 assert m.read(P+4)==636
 m.write(P,1214);m.write(P+28,W);m.write(P+32,M);m.write(P+36,R);m.put(P+40,b'\1');m.write(0x56a3798,M);m.write(m.g['uiThread'],7);m.put(m.g['loaded'],b'\1')
 h=M+(1214*5+0x41)*4;m.write(h+4,V);m.write(h+8,V+4);m.write(V,W)
 controls={name:m.S+0x80000+i*0x600 for i,name in enumerate(['KR_STATUS','KR_COUNT','KR_SAVE','KR_CLEAR','KR_CANCEL'])}
 for ptr in controls.values():m.write(ptr,0x4d4fa4c)
 m.mock(m.g['controlAt'],lambda:controls.get(m.wide(m.arg(1)),0));captions=[]
 m.mock(0x3835940,lambda:captions.append(m.wide(m.arg())),4)
 def fmt():
  text=m.wide(m.arg(1));text=text.replace('%u',str(m.arg(2)));m.put(m.arg(),(text+'\0').encode('utf-16le'));return len(text)
 m.iat(0x6e866b8,fmt)
 vt=m.S+0x60000;show=m.S+0x61000;m.write(vt+8,show);m.mock(show,0,4)
 scans=json.loads((S/'keyboard-layout.json').read_text())['keys'];table=m.native(0x6e8c000,256)
 m.put(m.g['draft'],struct.pack('<II',0x32524b44,2)+bytes(256));m.put(m.g['saved'],struct.pack('<II',0x32524b44,2)+bytes(256))
 for i,key in enumerate(scans):
  off=m.S+0x100000+i*0xc00;on=off+0x600;m.write(off,vt);m.write(on,vt);m.write(m.g['keyOff']+i*4,off);m.write(m.g['keyOn']+i*4,on)
 for i,key in enumerate(scans):
  scan=key['scan'];allowed=bool(table[scan]) and scan not in [219,220,221,222,223]
  off=m.read(m.g['keyOff']+i*4);on=m.read(m.g['keyOn']+i*4)
  for ptr,want in [(off,1 if allowed else 0),(on,0)]:
   m.call(m.g['PanelEvent'],(ptr,13,0,0),16,ecx=W+0x1b0);assert m.byte(m.g['draft']+8+scan)==want,(i,scan,want)
  case('keyboard-toggle-'+str(scan))
 # Failed persistence leaves the window/draft open and active config intact.
 m.put(m.g['configPath'],'C:\\fixture.ini\0'.encode('utf-16le'));m.put(m.g['draft']+8+30,b'\1');state={'write':False,'read':True,'data':b''}
 def write():
  if state['write']:state['data']=bytes(m.u.mem_read(m.arg(2),m.arg(3)))
  return state['write']
 def read():
  if state['read'] and state['data']:m.put(m.arg(2),state['data']);return 1
  return 0
 m.iat(0x6e85b58,write,20);m.iat(0x6e85b60,read,20)
 assert not m.call(m.g['saveKeyboard']);assert m.byte(P+40)==1 and m.byte(m.g['saved']+38)==0;case('keyboard-write-failure-retains-draft')
 state['write']=True;state['read']=False;assert not m.call(m.g['saveKeyboard']);assert m.byte(m.g['saved']+38)==0;case('keyboard-readback-failure-retains-config')
 state['read']=True;m.mock(0x32b94a0,lambda:m.write(h+8,V),12);assert m.call(m.g['saveKeyboard'])&255;assert m.byte(m.g['saved']+38)==1 and m.byte(P+40)==0;case('keyboard-commit-after-exact-readback')
 # The 200-unit circle requires all eight current live items to match the
 # server's PVF-proven set snapshot; removing a piece takes effect immediately.
 m=make();A=m.S+0x10000;DROP=m.S+0x20000;VT=m.S+0x30000;ITEMS=[m.S+0x40000+i*0x100 for i in range(8)]
 m.write(m.g['roleActor'],A);m.mock(0x29610e0,A);m.put(m.g['haveSnapshot'],b'\1');m.put(m.g['snapshot']+142,b'\1\0')
 for i,ptr in enumerate(ITEMS):m.write(m.g['snapshot']+48+i*4,100+i);m.write(ptr+0x10,100+i)
 m.mock(0x2531b60,lambda:ITEMS[m.arg(1)]);m.mock(m.g['itemId'],lambda:m.read(m.arg()+0x10))
 m.write(DROP,VT);m.write(VT+0x3c,m.S+0x50000);m.mock(m.S+0x50000,1,4)
 m.mock(0x2677960,lambda:m.read(m.u.reg_read(UC_X86_REG_ECX)+0x40));m.mock(0x2677970,lambda:m.read(m.u.reg_read(UC_X86_REG_ECX)+0x44))
 for x,y,want in [(0,0,True),(200,0,True),(0,200,True),(120,160,True),(201,0,False),(0,201,False),(141,142,False),(-201,0,False)]:
  m.write(DROP+0x40,x);m.write(DROP+0x44,y);assert bool(m.call(m.g['radius200'],(A,DROP))&255)==want;case('sky-radius-'+str((x,y)))
 m.write(DROP+0x40,100);m.write(DROP+0x44,0)
 for i,ptr in enumerate(ITEMS):
  m.write(ptr+0x10,0);assert not(m.call(m.g['radius200'],(A,DROP))&255);m.write(ptr+0x10,100+i);case('sky-unequip-'+str(i))
 # Saved presets execute the existing native wear call once, wait for its
 # authoritative acknowledgement, and continue past missing pieces.
 m=make();C=m.S+0x10000;ITEM=m.S+0x20000;m.write(m.g['epoch'],7);m.write(m.g['pendingSequence'],8);m.write(m.g['pendingAction'],2);m.write(m.g['pendingPreset'],1)
 m.put(m.g['swapping'],b'\1');m.write(m.g['swapTotal'],2);m.mock(0x2531a80,C);m.mock(0x2531b60,ITEM);m.mock(m.g['itemId'],100)
 calls=[]
 def wear():calls.append((m.u.reg_read(UC_X86_REG_ECX),tuple(m.arg(i) for i in range(5))));return 1
 m.mock(0x25f9a70,wear,20)
 def fmt_many():
  values=iter(m.arg(i) for i in range(2,8));out=re.sub('%u',lambda _:str(next(values)),m.wide(m.arg(1)));m.put(m.arg(),(out+'\0').encode('utf-16le'));return len(out)
 m.iat(0x6e866b8,fmt_many)
 def response(status,ordinal=0,epoch=7):
  b=bytearray(512);b[:4]=b'CFR1';struct.pack_into('<HBBIIIHHHHHI',b,4,2,2,1,8,epoch,1,status,ordinal,0,20,12,0);struct.pack_into('<I',b,32,100)
  m.put(m.g['incoming'],b);m.put(m.g['waiting'],b'\1');m.write(m.g['responseReady'],1);m.call(m.g['processFeatureResponse'])
 response(1,epoch=6);assert not calls and m.byte(m.g['waiting']);case('swap-stale-character-response-rejected')
 m.now=1000;response(1);assert calls==[(C,(20,12,1,6,0))] and m.byte(m.g['wearWaiting']) and m.read(m.g['swapOrdinal'])==0;case('swap-native-wear-ABI')
 m.now=1400;response(1);assert len(calls)==1;case('swap-waits-without-duplicate-wear')
 response(0);assert m.read(m.g['swapOrdinal'])==1 and not m.byte(m.g['wearWaiting']);case('swap-authoritative-wear-ack')
 response(3,1);assert not m.byte(m.g['swapping']) and m.read(m.g['swapMissing'])==1;case('swap-missing-piece-completes')
 # The real DI table maps modifiers to left/right VK values and numpad1 to End.
 m=make();keys=m.S+0x10000;m.put(m.g['panels']+572+40,b'\1');m.put(m.g['editHotkey'],b'\1')
 for scan,mod in [(56,1),(184,1),(29,2),(157,2),(42,4),(54,4)]:
  m.put(m.g['previousKeys'],bytes(256));m.write(m.g['queuedSwapKey'],0);raw=bytearray(256);raw[scan]=128;m.put(keys,raw);m.call(m.g['swapKeys'],(keys,1))
  assert m.read(m.g['queuedSwapKey'])==0;case('modifier-alone-not-binding-'+str(scan))
  for key,vk in [(30,65),(79,0x61),(82,0x60),(72,0x68)]:
   raw[key]=128;m.put(keys,raw);m.call(m.g['swapKeys'],(keys,1));assert m.read(m.g['queuedSwapKey'])==(0x1000000|vk|(mod<<16));case('combo-'+str((scan,key)))
   raw[key]=0;m.put(keys,raw);m.call(m.g['swapKeys'],(keys,1))
 m.put(m.g['editHotkey'],b'\0');m.put(m.g['previousKeys'],bytes(256));m.write(m.g['queuedSwapKey'],0);m.write(m.g['savedHotkeys'],0xa4)
 raw=bytearray(256);raw[56]=128;m.put(keys,raw);m.call(m.g['swapKeys'],(keys,1));assert m.read(m.g['queuedSwapKey'])==0;case('old-pure-alt-bind-does-not-equip')
 m.write(m.g['savedHotkeys'],0x10061);raw[79]=128;m.put(keys,raw);m.call(m.g['swapKeys'],(keys,1));assert m.read(m.g['queuedSwapKey'])==1;case('saved-alt-numpad1-equips-only-bound-profile')
 # Preview tab events cannot send a request or invoke native wear.
 m=make();m.mock(m.g['renderSwap'],0);m.mock(m.g['request'],lambda:(_ for _ in ()).throw(AssertionError('tab sent a request')));m.mock(m.g['swapEvent'],lambda:(_ for _ in ()).throw(AssertionError('tab equipped')))
 for tab in range(6):
  m.call(m.g['swapControl'],(101+tab,0));assert m.read(m.g['selectedProfile'])==tab and not m.byte(m.g['swapping']);case('tab-preview-only-'+str(tab))
 m=make();m.write(m.g['epoch'],9);m.write(m.g['pendingSequence'],12);m.write(m.g['pendingAction'],6);m.write(m.g['pendingPreset'],0);m.write(m.g['inspectedTarget'],222)
 own=bytes([0x5a])*512;m.put(m.g['snapshot'],own)
 for target in [221,222]:
  b=bytearray(512);b[:4]=b'CFR1';struct.pack_into('<HBBIII',b,4,2,6,0,12,9,target);m.put(m.g['incoming'],b);m.put(m.g['waiting'],b'\1');m.write(m.g['responseReady'],1);m.call(m.g['processFeatureResponse'])
  assert bytes(m.u.mem_read(m.g['snapshot'],512))==own
  assert m.read(m.g['inspectedSnapshot']+16)==(222 if target==222 else 0);case('peer-response-isolation-'+str(target))
 # Exact compiled personal-window projection: full/current values and SSO/heap strings.
 m=make();W=m.S+0x10000;C=m.S+0x20000;A=m.S+0x30000;V=m.S+0x40000;OUT=m.S+0x80000
 m.write(W+0x1dc,C);m.write(C+12,A);m.write(C+0x38,V);m.write(C+0x3c,V+20)
 numbers=['1200/60000','100/30000','1500','700','1000','600','1800','1500','','1100']
 for row in range(5):
  r=m.S+0x50000+row*0x200;m.write(V+row*4,r)
  for side in range(2):
   ident=row*2+side;val=m.S+0x60000+ident*0x200;chars=numbers[ident];m.write(r+side*4,ident);m.write(r+8+side*4,val)
   st=val+0x14;m.write(st+0x14,len(chars));m.write(st+0x18,31 if len(chars)>=8 else 7)
   dest=val+0x80 if len(chars)>=8 else st+4
   if len(chars)>=8:m.write(st+4,dest)
   m.put(dest,(chars+'\0').encode('utf-16le'))
 assert m.call(m.g['readPersonalStats'],(W,A,OUT))&255
 assert [m.read(OUT+x) for x in range(32,68,4)]==[60000,30000,1500,700,1000,600,1800,1500,1100];case('personal-native-final-stats')
 assert not m.call(m.g['readPersonalStats'],(W,A+4,OUT))&255;case('personal-reject-other-character')
 m.write(C+0x3c,V+16);assert not m.call(m.g['readPersonalStats'],(W,A,OUT))&255;case('personal-reject-incomplete-fields')
 # The installed template must use current personal window 240, never A21 167.
 assert m.native(0x3350e6f,5)==bytes.fromhex('68f0000000');case('current-personal-window-native-id-240')
 result=dict(passed=True,cases=cases,caseCount=len(cases),clientSHA256=hashlib.sha256((Path(os.environ['CLIENT_FEATURE_CANDIDATE'])).read_bytes()).hexdigest(),compiledNativeABI=True,gameExecuted=False,graphicsValidated=False)
 (Path(os.environ['CLIENT_FEATURE_OUTPUT'])/'native-verification.json').write_text(json.dumps(result,ensure_ascii=False,indent=2),'utf8');print(json.dumps(dict(passed=True,caseCount=len(cases)),indent=2))
if __name__=='__main__':verify()
