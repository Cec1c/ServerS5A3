"""Exercise the reported R5 failures through compiled/native UI boundaries."""
from pathlib import Path
import os
import json,struct,hashlib
from verify_native import make as old_make
from unicorn.x86_const import *
O=Path(os.environ['CLIENT_FEATURE_OUTPUT']);cases=[]
def check(n):cases.append(n)
def make():
 m=old_make()
 # The inherited harness replaced readable() with true. This boundary suite
 # executes its actual null/range/protection checks, using mapped pages as OS.
 a=m.g['readable'];m.mocks.pop(a);m.put(a,m.native(a,1))
 def query():
  addr=m.arg();base=addr&~4095
  if base not in m.pages:return 0
  end=base+4096
  while end in m.pages:end+=4096
  m.put(m.arg(1),struct.pack('<7I',base,base,4,end-base,0x1000,4,0));return 28
 m.iat(0x6e85b18,query,12);return m

# The real event ABI uses numeric XUI IDs. Drive PanelEvent, not swapControl.
m=make();P=m.g['panels']+572;W=m.S+0x10000;M=m.S+0x20000;R=m.S+0x40000;V=m.S+0x50000
assert m.read(P+4)==510
m.write(P,1214);m.write(P+28,W);m.write(P+32,M);m.write(P+36,R);m.put(P+40,b'\1');m.write(0x56a3798,M);m.write(m.g['uiThread'],7)
h=M+(1214*5+0x41)*4;m.write(h+4,V);m.write(h+8,V+4);m.write(V,W)
m.mock(m.g['renderSwap'],0);m.mock(m.g['controlAt'],0)
def fmt():
 s=m.wide(m.arg(1)).replace('%d',str(m.arg(2)));m.put(m.arg(),(s+'\0').encode('utf-16le'));return len(s)
m.iat(0x6e866b8,fmt);requests=[];wear=[]
m.mock(m.g['request'],lambda:requests.append(tuple(m.arg(i) for i in range(3))) or 1)
m.mock(m.g['swapEvent'],lambda:wear.append((m.arg(),m.arg(1))) or 0)
for ident in [100,101,102,103,104,105,106]:
 for argument in range(6):
  expected=argument if ident==100 else ident-101
  m.call(m.g['PanelEvent'],(ident,13,argument,0),16,ecx=W+0x1b0)
  assert m.read(m.g['selectedProfile'])==expected and not wear and not requests
  check(f'native-numeric-tab-{ident}-{argument}')
m.call(m.g['PanelEvent'],(205,13,0,0),16,ecx=W+0x1b0);assert m.byte(m.g['editHotkey']);check('numeric-edit-hotkey-button')
m.write(m.g['hotkeyDraft'],0x10061)
m.call(m.g['PanelEvent'],(206,13,0,0),16,ecx=W+0x1b0);assert requests==[(5,5,0x10061)];check('numeric-save-combination-button')
m.call(m.g['PanelEvent'],(202,13,0,0),16,ecx=W+0x1b0);assert wear==[(5,0)];check('numeric-save-selected-equipment')
m.call(m.g['PanelEvent'],(203,13,0,0),16,ecx=W+0x1b0);assert wear==[(5,0),(5,1)];check('numeric-equip-only-on-button')
for ident,kind in [(999,13),(205,12),(0x200,13)]:
 m.put(m.g['editHotkey'],b'\0');m.call(m.g['PanelEvent'],(ident,kind,0,0),16,ecx=W+0x1b0);assert not m.byte(m.g['editHotkey']);check(f'unrelated-event-{ident}-{kind}')

# Native peer-render hook: the original common draw path supplies its exact
# formatted values. Preserve registers/flags/stack and replay displaced LEA.
m=make();W=m.S+0x10000;A=m.S+0x20000;OUT=m.S+0x30000;frame=m.STACK-0x100
m.write(m.g['uiThread'],999);m.write(m.g['roleActor'],A+0x10000);m.write(m.g['roleUnique'],1);m.write(m.g['epoch'],9)
m.write(W,0x4cfd41c);m.write(W+0x1bc,A);m.write(W+0x38c,4)
m.put(0x349efaf,m.native(0x349efaf,6))
values=['130081/130081','9891/87838','3368','2710','2439','2518','56252','15621','','2802']
registers=[UC_X86_REG_EBX,UC_X86_REG_ECX,UC_X86_REG_EDX,UC_X86_REG_EDI]
for ident,chars in enumerate(values):
 if ident==8:continue
 m.write(frame+8,ident);m.put(frame-0x810,(chars+'\0').encode('utf-16le'))
 for i,r in enumerate(registers):m.u.reg_write(r,0x11110000+i)
 m.u.reg_write(UC_X86_REG_EBP,frame);m.u.reg_write(UC_X86_REG_ESP,frame-0x900);m.u.reg_write(UC_X86_REG_ESI,W);m.u.reg_write(UC_X86_REG_EFLAGS,0x246)
 m.u.emu_start(0x349efaf,0x349efb5,count=100000)
 assert m.u.reg_read(UC_X86_REG_ESP)==frame-0x900 and m.u.reg_read(UC_X86_REG_EAX)==frame-0x810
 assert m.u.reg_read(UC_X86_REG_EFLAGS)==0x246 and m.u.reg_read(UC_X86_REG_ESI)==W
 assert [m.u.reg_read(r) for r in registers]==[0x11110000+i for i in range(len(registers))]
 check(f'original-peer-render-hook-ABI-stat-{ident}')
assert m.call(m.g['readPeerStats'],(W,4,OUT))&255
assert [m.read(OUT+x) for x in range(32,68,4)]==[130081,87838,3368,2710,2439,2518,56252,15621,2802]
check('peer-final-stats-match-native-visible-fields')
for label,offset,replacement in [('target',W+0x38c,5),('actor',W+0x1bc,A+4),('epoch',m.g['epoch'],10)]:
 old=m.read(offset);m.write(offset,replacement)
 # Target validity is enforced by the caller's current target argument.
 assert not m.call(m.g['readPeerStats'],(W,5 if label=='target' else 4,OUT))&255
 m.write(offset,old);check('peer-cache-reject-'+label)

# Native sidebar receives cached renderer positions, independent of alignment.
m=make();W=m.S+0x10000;R=m.S+0x20000
m.write(m.g['roleActor'],m.S+0x50000);m.put(m.g['haveSnapshot'],b'\1');m.write(m.g['snapshot']+16,1)
m.write(W+0x30,R);m.write(R+0x6c,288);m.write(R+0x70,389);m.write(R+0x7c,120);m.write(R+0x80,133)
m.mock(m.g['activeWindow'],lambda:W if m.arg()==240 else 0);m.mock(m.g['readPersonalStats'],0)
m.write(m.g['powerCompute'],m.S+0x60000);frames=[]
m.mock(m.g['nativePowerFrame'],lambda:frames.append(bytes(m.u.mem_read(m.arg(),700))) or 0)
for sw,sh in [(800,600),(1067,600),(1280,720)]:
 m.write(0x55b0318,sw);m.write(0x55b031c,sh);m.call(m.g['powerSidebar'])
 assert struct.unpack_from('<5i',frames[-1],8)==(1,120,133,288,389)
 check(f'renderer-coordinate-frame-{sw}-{sh}')

# The compiled confirmation closes the original dialog before native shop
# transition. Poison it in close; any later access is an error in this test.
from unicorn import UC_HOOK_MEM_READ
for group in [1,13,41,42,43,44,45,46,47,48,49,50,51,52,53]:
 m=make();W=m.S+0x10000;M=m.S+0x20000;SHOP=m.S+0x40000;PRODUCT=m.S+0x50000;CLOSE=m.S+0x60000
 m.write(W,0x497406c);m.write(W+0x1c0,group);m.write(0x56a3798,M);m.write(M+0xc3bc,SHOP);m.write(0x497406c+0x7c,CLOSE)
 events=[];closed=[False]
 def close():events.append('close');closed[0]=True;return 0
 def forbid(u,access,address,size,value,data):
  assert not(closed[0] and W<=address<W+0x1c4),'confirmation read after close'
 m.u.hook_add(UC_HOOK_MEM_READ,forbid);m.mock(CLOSE,close)
 m.mock(0x1977820,lambda:events.append('open') or 1,4)
 m.put(PRODUCT,bytes([8]));m.write(PRODUCT+0x14,123)
 m.mock(0x194d380,lambda:PRODUCT,12);m.mock(0x195b570,1,4);m.mock(0x194b230,8,4)
 m.mock(0x1978da0,lambda:events.append('select') or 1,16);m.mock(0x1974850,lambda:events.append('fallback') or 1,8)
 assert m.call(m.exports['CouponConfirm'],ecx=W)&255
 assert events==['close','open','select' if group in [13,*range(41,52)] else 'fallback'],(group,events)
 check('coupon-close-before-shop-group-'+str(group))
for absent in ['manager','shop','wrong-dialog']:
 m=make();W=m.S+0x10000;M=m.S+0x20000;m.write(W,0x497406c if absent!='wrong-dialog' else 0);m.write(W+0x1c0,49)
 m.write(0x56a3798,0 if absent=='manager' else M);m.write(M+0xc3bc,0)
 assert not m.call(m.exports['CouponConfirm'],ecx=W)&255
 check('coupon-unavailable-'+absent)

# Resource/actor construction can call the native singleton getter repeatedly
# inside one frame. Only extension work is limited, never the native getter.
m=make();m.write(m.g['uiThread'],7);m.put(m.g['loaded'],b'\1');calls=[];original=[]
m.mock(m.g['swapTick'],lambda:calls.append('swap') or 0);m.mock(m.g['featureTick'],lambda:calls.append('features') or 0)
m.mock(0x71119c8,lambda:original.append(1) or 0x12345678)
for now,want in [(100,2),(100,2),(115,2),(116,4),(131,4),(132,6)]:
 m.now=now
 for i in range(100):assert m.call(m.exports['TickGetter'])==0x12345678
 assert len(calls)==want;check('getter-burst-budget-'+str((now,want)))
assert len(original)==600;check('every-original-getter-call-preserved')
m.write(m.g['lastFeatureTick'],0xfffffff0);m.now=5;m.call(m.exports['TickGetter']);assert len(calls)==8;check('feature-tick-DWORD-wrap')
# Production must execute the original native constructor directly.
m=make()
assert 'TownVisualCreate' not in m.exports and 'featureGetterCalls' not in m.g and 'nativeTiming' not in m.g
assert m.native(0x6f8b59f,5)==b'\xe8'+struct.pack('<i',0x2965bd0-0x6f8b59f-5)
check('production-native-creator-no-timing-wrapper')
result={'passed':True,'caseCount':len(cases),'cases':cases,'compiledNativeABI':True,'gameExecuted':False,'clientSHA256':hashlib.sha256(Path(os.environ['CLIENT_FEATURE_CANDIDATE']).read_bytes()).hexdigest()}
(O/'feedback-r6-native.json').write_text(json.dumps(result,indent=2),'utf8');print(json.dumps({'passed':True,'caseCount':len(cases)}))
