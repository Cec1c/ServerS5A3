from pathlib import Path
import os
import json,struct,hashlib,xml.etree.ElementTree as E
from verify_native import make
from unicorn.x86_const import *
O=Path(os.environ['CLIENT_FEATURE_OUTPUT']);S=Path(__file__).parent;cases=[]
for hp,mp,independent in [(130081,87838,2802),(93678,57580,2864),(59150,26650,1011)]:
 m=make();W=m.S+0x10000;A=m.S+0x20000;OUT=m.S+0x30000;frame=m.STACK-0x100
 m.write(m.g['roleActor'],A+0x10000);m.write(m.g['roleUnique'],1);m.write(m.g['epoch'],9)
 m.write(W,0x4cfd41c);m.write(W+0x1bc,A);m.write(W+0x38c,4)
 for ident,n in enumerate([hp,mp,3368,2710,2439,2518,56252,15621]):
  m.put(OUT,(str(n)+'\0').encode('utf-16le'));m.call(m.exports['CapturePeerStat'],(W,ident,OUT))
 assert not m.call(m.g['readPeerStats'],(W,4,OUT+512))&255
 text=(str(independent)+'\0').encode('utf-16le');m.put(frame-0x810,text);m.write(frame+8,9)
 def run(code):
  m.put(0x349ca6a,code);m.put(0x349ca70,m.native(0x349ca70,12))
  m.u.ctl_remove_cache(0x349ca6a,0x349ca7c)
  m.u.reg_write(UC_X86_REG_EBP,frame);m.u.reg_write(UC_X86_REG_ESP,frame-0x900);m.u.reg_write(UC_X86_REG_ESI,W);m.u.reg_write(UC_X86_REG_EAX,0x12345678);m.u.reg_write(UC_X86_REG_EFLAGS,0x246)
  m.u.emu_start(0x349ca6a,0x349efbc,count=100000)
  assert m.u.reg_read(UC_X86_REG_ESI)==frame-0x810 and m.u.reg_read(UC_X86_REG_ESP)==frame-0x904
  assert m.u.reg_read(UC_X86_REG_EAX)==0x12345678 and m.u.reg_read(UC_X86_REG_EFLAGS)==0x246
 # Execute the real branch and its jump (not the former synthetic common
 # hook), demonstrating why the R6 hook never saw stat ID9.
 run(bytes.fromhex('8db5f0f7ffff'))
 assert not m.call(m.g['readPeerStats'],(W,4,OUT+512))&255;cases.append(f'old-independent-native-branch-missing-{hp}')
 run(m.native(0x349ca6a,6))
 assert m.call(m.g['readPeerStats'],(W,4,OUT+512))&255, (m.read(m.g['peerStats']+16),m.trace)
 assert m.read(OUT+512+32)==hp and m.read(OUT+512+36)==mp and m.read(OUT+512+64)==independent
 cases.append(f'fixed-real-branch-registers-final-stats-{hp}')
 assert not m.call(m.g['readPeerStats'],(W,3,OUT+512))&255;cases.append('stale-target-rejected')
 m.write(m.g['epoch'],10);assert not m.call(m.g['readPeerStats'],(W,4,OUT+512))&255;cases.append('stale-generation-rejected')
# Verify actual native tiled geometry getters, rather than metadata alone.
m=make();P=m.g['panels']+572*3;W=m.S+0x10000;PROP=m.S+0x20000;m.write(W+4,PROP)
m.write(PROP+0x24,m.read(P+12));m.write(PROP+0x28,m.read(P+16));m.write(PROP+0x44,12);m.write(PROP+0x48,13);m.write(PROP+0x40,0)
for fn,dimension in [(0x335a7b0,4),(0x335a7e0,8)]:
 m.put(fn,m.native(fn,64));assert m.call(fn,ecx=W)==m.read(P+dimension);cases.append('native-tile-dimension-'+str(dimension))
x=E.parse(S/'Power.xui');assert not list(x.iter('CNBoardImage'))
assert all(b.get('ButtonDrawType')=='STR:NORMALIMAGE' for b in x.iter('CNButton'));cases.append('complete-original-button-sprites')
assert any(i.get('ID')=='STR:CP_SKIN' and i.get('imageIndex')=='NUM:7' for i in x.iter('CNUIControlImage'));cases.append('original-opaque-content-background')
assert all(i.get('font')=='STR:system font' for i in x.iter('CNUIControlText'));cases.append('native-system-font')
result=dict(passed=True,caseCount=len(cases),cases=cases,clientSHA256=hashlib.file_digest(Path(os.environ['CLIENT_FEATURE_CANDIDATE']).open('rb'),'sha256').hexdigest(),actualIndependentBranchTested=True,oldFailureReproduced=True,gameExecuted=False)
(O/'feedback-r7-native.json').write_text(json.dumps(result,indent=2),'utf8');print('R7 native regression:',len(cases),'passed')
