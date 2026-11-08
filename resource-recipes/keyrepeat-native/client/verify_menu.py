"""Compiled menu entry, per-instance update and routing regression checks."""
from pathlib import Path
import json,struct
from verify_native import Machine
from unicorn.x86_const import UC_X86_REG_ECX
from verification_paths import S, D, BASE
def make():return Machine(D/'build/key_repeat.payload',D/'build/key_repeat.map',BASE).setup_ui()
def verify():
 m=make();g=m.g;menu=m.parent
 # The registered vtable entry receives a numeric ID, closes menu then opens
 # the reusable panel. Unknown IDs must return the native handler's result.
 m.vector(0x453,True,[menu]);m.config([(65,1)]);m.put(g['shown'],b'\0');actions=[]
 def close():
  actions.append(('close',m.arg()));assert m.arg()==0x453;m.vector(0x453,True,[])
 m.mock(0x32b94a0,close,12)
 native_open=m.mocks[0x32bea50][0]
 def opened():actions.append(('open',m.arg()));return native_open()
 m.mock(0x32bea50,opened,12)
 assert m.call(m.exports['MenuEvent'],(7200,13,0,0),16,ecx=menu+0x1b0)==1
 assert actions==[('close',0x453),('open',1214)] and m.byte(g['shown'])
 actions.clear();m.call(m.exports['MenuEvent'],(7200,13,0,0),16,ecx=menu+0x1b0);assert not actions,'stale menu callback accepted'
 # Panel ticking no longer depends on the keyboard settings update or menu.
 updates=[];m.mock(0x3361970,lambda:updates.append('native'));m.mock(g['captureTick'],lambda:updates.append('capture'))
 m.call(g['PanelUpdate'],ecx=m.win);assert updates==['native','capture']
 m.vector(1214,True,[]);m.vector(1214,False,[m.win]);updates.clear();m.call(g['PanelUpdate'],ecx=m.win)
 assert updates==['native'] and not m.byte(g['shown']) and m.read(g['capture'])==0xffffffff
 # Real layout dispatcher: original keyboard XUI must pass down the existing
 # cumulative chain; menu XML is parsed, private panel only during creation.
 m=make();g=m.g;chain=[];parsed=[];doc=m.S+0x190000;path=m.S+0x191000;m.write(doc,0x4627bb4)
 m.mock(m.exports['PreviousLayout'],lambda:chain.append(m.wide(m.arg())) or 1)
 m.mock(0x3e43eb0,lambda:parsed.append(bytes(m.u.mem_read(m.arg(),96))),12);m.mock(0x3e3f700,1)
 def layout(p):m.put(path,(p+'\0').encode('utf-16le'));return m.call(m.exports['Layout'],(path,doc))&255
 assert layout('UI/HotKey/HotKeyWindow.xui')==1 and chain==['UI/HotKey/HotKeyWindow.xui'] and not parsed
 assert layout('UI/HUD/MainMenuWindowVer2.xui')==1 and len(parsed)==1
 assert layout('UI/PrivateKeyRepeat001/KeyRepeat.xui')==1 and len(parsed)==1
 m.put(g['creating'],b'\1');assert layout('UI/PrivateKeyRepeat001/KeyRepeat.xui')==1 and len(parsed)==2
 # Execute compiled render(), recording real text/width/position calls.
 m=make();g=m.g;m.config([(65,1),(32,1),(160,1),(33,0),(97,0),(66,1)])
 lib=m.pe.__class__(str(D/'build/key_repeat.payload'));m.mocks.pop(g['render']);m.put(g['render'],lib.get_data(g['render']-lib.OPTIONAL_HEADER.ImageBase,1))
 captions={};points={};checked=[]
 for i in range(10):
  p=m.S+0x180000+i*0x500;m.write(g['labels']+4*i,p);m.write(p,0x4d4fa4c)
 count=m.S+0x187000;m.controls['KR_COUNT']=count;m.write(count,0x4d4fa4c)
 def set_text():captions[m.u.reg_read(UC_X86_REG_ECX)]=m.wide(m.arg())
 def measure():return sum(12 if ord(c)>127 else 6 for c in captions[m.u.reg_read(UC_X86_REG_ECX)])
 def positioned():points[m.u.reg_read(UC_X86_REG_ECX)]=(m.arg(),m.arg(1))
 m.mock(0x3835940,set_text,4);m.mock(0x3835890,measure);m.mock(0x37ea190,positioned,8);m.mock(0x3840b20,lambda:checked.append(m.arg()),4)
 m.call(g['render']);assert len(checked)==10 and captions[count]=='已启用 4 / 10'
 for i in range(10):
  p=m.read(g['labels']+4*i);x,y=points[p];w=sum(12 if ord(c)>127 else 6 for c in captions[p]);left=91+i//5*222
  assert x==left+(112-w)//2 and y==102+i%5*31 and x+w<=left+112
 assert captions[m.read(g['labels']+4)]=='Space' and captions[m.read(g['labels']+8)]=='LShift'
 m.write(g['capture'],9);m.call(g['render']);assert captions[m.read(g['labels']+36)]=='请按一个键'
 # Native X cancels unsaved draft, preserving the active settings.
 before=bytes(m.u.mem_read(g['saved'],88));m.write(g['draft']+8,67);cross=m.S+0x188000;m.write(m.win+0x50,cross)
 m.call(g['PanelEvent'],(cross,13,0,0),16);assert not m.active and bytes(m.u.mem_read(g['saved'],88))==before
 return dict(passed=True,actualMenuVtableHookABI=True,closeMenuThenOpenPanel=True,staleMenuCallbackRejected=True,panelOwnUpdate=True,externalCloseStopsRecording=True,keyboardLayoutPassesUnchanged=True,privateLayoutCreationGated=True,compiledTenSlotRenderAndLongNames=True,nativeXDiscardsDraft=True,gameExecuted=False)
if __name__=='__main__':
 r=verify();(D/'menu-native-tests.json').write_text(json.dumps(r,indent=2),'utf8');print(json.dumps(r,indent=2))
