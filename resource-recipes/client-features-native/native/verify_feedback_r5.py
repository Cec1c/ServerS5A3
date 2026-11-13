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
m.write(m.g['uiThread'],7);m.write(m.g['roleActor'],A+0x10000);m.write(m.g['roleUnique'],1);m.write(m.g['epoch'],9)
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

# Geometry is sourced from the native root, with misleading but readable
# hidden close/title controls deliberately present. Old R4 shrinks to 170.
m=make();W=m.S+0x10000;R=m.S+0x20000;C=m.S+0x30000;T=m.S+0x40000
m.write(m.g['roleActor'],m.S+0x50000);m.put(m.g['haveSnapshot'],b'\1');m.write(m.g['snapshot']+16,1)
m.write(W+0x30,R);m.write(W+0x50,C);m.write(W+0x48,T);m.write(R+0x6c,288);m.write(R+0x70,389);m.write(C+0x6c,20)
m.mock(m.g['activeWindow'],lambda:W if m.arg()==240 else 0)
m.mock(m.g['readPersonalStats'],0)
feed=m.S+0x60000;m.write(m.g['powerFeed'],feed);frames=[]
m.mock(feed,lambda:frames.append(bytes(m.u.mem_read(m.arg(),700))) or 0)
def coords():
 obj=m.u.reg_read(UC_X86_REG_ECX);xy=(120,133) if obj==R else (264,111) if obj==C else (120,30)
 m.put(m.arg(),struct.pack('<ii',*xy));return m.arg()
m.mock(0x37eb710,coords,4)
for sw,sh in [(800,600),(1067,600),(1280,720)]:
 m.write(0x55b0318,sw);m.write(0x55b031c,sh);m.call(m.g['powerSidebar'])
 assert struct.unpack_from('<5i',frames[-1],8)==(1,120,133,288,389)
 check(f'native-root-docking-ignores-hidden-chrome-{sw}-{sh}')
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
# Paired creator hook forwards this/argument/result and reports bounded data.
m=make();W=m.S+0x10000;seen=[];m.write(m.g['featureGetterCalls'],9)
def create():
 seen.append((m.u.reg_read(UC_X86_REG_ECX),m.arg()));m.now+=37;m.write(m.g['featureGetterCalls'],28);return W+4
m.mock(0x2965bd0,create,4);timing=m.S+0x20000;m.write(m.g['nativeTiming'],timing);reports=[]
m.mock(timing,lambda:reports.append(tuple(m.arg(i) for i in range(3))) or 0)
assert m.call(m.exports['TownVisualCreate'],(0,),4,ecx=W)==W+4
assert seen==[(W,0)] and reports==[(1,37,19)];check('town-creator-native-ABI-and-timing')
result={'passed':True,'caseCount':len(cases),'cases':cases,'compiledNativeABI':True,'gameExecuted':False,'clientSHA256':hashlib.sha256((O/'candidate/DNF.exe').read_bytes()).hexdigest()}
(O/'feedback-r5-native.json').write_text(json.dumps(result,indent=2),'utf8');print(json.dumps({'passed':True,'caseCount':len(cases)}))
