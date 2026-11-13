from verify_native import make
from pathlib import Path
import os
import json,struct
from unicorn.x86_const import UC_X86_REG_ECX
O=Path(os.environ['CLIENT_FEATURE_OUTPUT']);cases=[]
for sw,sh in [(800,600),(1067,600),(1280,720),(1600,900)]:
 m=make();P=m.g['panels']+572*3;ROOT=m.S+0x10000;FRAME=m.S+0x20000;OWNER=m.S+0x30000;COMPUTE=m.S+0x40000
 # Drawing/chrome is verified against real native style paths in R8 checks.
 m.write(P+36,ROOT);m.put(P+40,b'\1');m.mock(m.g['owned'],1);m.mock(m.g['paintPower'],0);m.mock(m.g['fitPowerChrome'],0)
 m.write(m.g['powerCompute'],COMPUTE);m.mock(COMPUTE,0)
 m.put(FRAME,struct.pack('<7i',336,0,1,120,100,288,389)+bytes(308));m.write(FRAME+32,1);m.write(FRAME+328,sw);m.write(FRAME+332,sh)
 moves=[]
 def move():
  obj=m.u.reg_read(UC_X86_REG_ECX);x,y=m.arg(),m.arg(1);m.write(obj+0x7c,x);m.write(obj+0x80,y);moves.append((x,y));return 0
 m.mock(0x37ecc40,move,8)
 for _ in range(4):m.call(m.g['nativePowerFrame'],(FRAME,OWNER))
 assert (m.read(ROOT+0x7c),m.read(ROOT+0x80))==(408,109);cases.append('first-right-edge-visible-title-aligned-'+str(sw))
 m.write(ROOT+0x7c,429);m.write(ROOT+0x80,113);m.call(m.g['nativePowerFrame'],(FRAME,OWNER))
 assert (m.read(ROOT+0x7c),m.read(ROOT+0x80))==(429,113);cases.append('user-drag-retained-'+str(sw))
 m.write(FRAME+12,140);m.write(FRAME+16,110)
 for _ in range(4):m.call(m.g['nativePowerFrame'],(FRAME,OWNER))
 assert (m.read(ROOT+0x7c),m.read(ROOT+0x80))==(449,123);cases.append('follows-owner-after-drag-'+str(sw))
 m.write(FRAME+328,640);m.write(FRAME+332,480);m.call(m.g['nativePowerFrame'],(FRAME,OWNER))
 assert m.read(ROOT+0x7c)+120<=632 and m.read(ROOT+0x80)+338<=472;cases.append('resolution-clamp-'+str(sw))
 m.write(FRAME+328,sw);m.write(FRAME+332,sh);m.write(FRAME+32,2)
 m.call(m.g['nativePowerFrame'],(FRAME,OWNER));assert (m.read(ROOT+0x7c),m.read(ROOT+0x80))==(428,119);cases.append('new-inspected-role-resets-offset-'+str(sw))
(O/'native-docking-r6.json').write_text(json.dumps(dict(passed=True,cases=cases,gameExecuted=False),indent=2),'utf8');print('native docking cases',len(cases))
