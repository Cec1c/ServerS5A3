from verify_native import make
from pathlib import Path
import os
from unicorn.x86_const import *
import json,struct,sys,xml.etree.ElementTree as E
O=Path(os.environ['CLIENT_FEATURE_OUTPUT']);S=Path(__file__).parent;cases=[]
# Execute the exact native style switch, not a reimplementation of its offset.
for style,want in [(0,11),(10,0)]:
 m=make();W=m.S+0x10000;P=W+0x200;K=W+0x400;OUT=W+0x600;M=W+0x800;VT=M+0x100;LOAD=M+0x200
 m.put(0x3360660,m.native(0x3360660,0x37c));m.put(0x33609f8,m.native(0x33609f8,64))
 m.write(W+4,P);m.write(W+12,K);m.write(P+0x24,10);m.write(P+0x28,26);m.write(K+8,13)
 m.write(0x581a3e4,M);m.write(M,VT);m.write(VT+8,LOAD)
 m.mock(0x37de240,0);m.mock(LOAD,lambda:m.arg(),8);m.mock(0xc3b0a0,0,4);m.mock(0xf11f90,0);m.mock(0x230d520,0,4)
 m.u.reg_write(UC_X86_REG_EAX,P);m.u.reg_write(UC_X86_REG_ESI,OUT);m.u.reg_write(UC_X86_REG_EDI,W);m.u.reg_write(UC_X86_REG_EBX,0)
 m.u.reg_write(UC_X86_REG_EBP,m.STACK+0x400);m.write(m.STACK+0x40c,style);m.u.reg_write(UC_X86_REG_ESP,m.STACK)
 m.u.emu_start(0x3360660,0x33609dc,count=10000)
 assert (m.read(OUT+0x20),m.read(OUT+0x24))==(want,want)
 assert m.read(OUT+0x28)==(97 if style==0 else 13);cases.append('actual-native-offset-style-'+str(style))
# Compiled helper changes only the power instance; keep native title and drag.
for panel in [0,1,2,3,4,5]:
 m=make();P=m.g['panels']+572*panel;W=m.S+0x10000;PROP=W+0x400;SKIN=W+0x600;TITLE=W+0x800;CLOSE=W+0x1000;VT=W+0x1800;SIZE=W+0x2000
 m.write(P+28,W);m.write(W+4,PROP);m.write(W+12,SKIN);m.write(W+0x48,TITLE);m.write(W+0x50,CLOSE);m.write(TITLE,VT);m.write(VT+0x5c,SIZE)
 m.write(PROP+0x40,0);m.write(SKIN+12,11);m.write(SKIN+16,11);moves=[];sizes=[]
 m.mock(SIZE,lambda:sizes.append((m.arg(),m.arg(1))) or 0,8)
 m.mock(0x37ea560,lambda:moves.append((m.u.reg_read(UC_X86_REG_ECX),m.arg(),m.arg(1))) or 0,8)
 m.call(m.g['fitPowerChrome'],(P,))
 if panel==3:
  assert m.read(PROP+0x40)==10 and m.read(SKIN+12)==m.read(SKIN+16)==0
  assert sizes==[(124,26)] and moves==[(TITLE,0xfffffffe,0xffffffe7),(CLOSE,108,0xfffffff1)]
  # Visible native pixels: top(-25+3)+new docking offset9 == own(-21+8).
  assert -25+3+9==-21+8 and -2+2==0 and -2+124-2==120
 else:assert not moves and not sizes and m.read(PROP+0x40)==0 and m.read(SKIN+12)==11
 cases.append('power-chrome-instance-'+str(panel))
sys.path.insert(0,str(S.parents[1]/'amp13-icon'));from npk_codec import entries,frames,frame_image
fs=frames(list(entries((S.parent/'assets/sprite_interface_private_features20261113.NPK').read_bytes()))[0][1])
for i in range(13,22):assert frame_image(fs,i).getbbox() is None;cases.append('no-outer-tile-'+str(i))
x=E.fromstring((S/'Power.xui').read_bytes());p=x.find('POPUPWINDOW');assert p.get('TileType')=='NUM:10' and p.get('TileStartingFrame')=='NUM:13' and p.get('TileOffset_')=='VECTOR:0, 0'
assert frame_image(fs,7).size==(120,338);cases.append('original-content-native-font-retained')
report=dict(passed=True,cases=cases,gameExecuted=False,scope='Actual native style switch and compiled chrome helper; visual game acceptance remains pending')
(O/'feedback-r8-native.json').write_text(json.dumps(report,indent=2),'utf8');print('R8 native chrome',len(cases))
