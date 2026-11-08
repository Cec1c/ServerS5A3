"""Real native map construction/insertion plus compiled private row integration."""
from pathlib import Path
import struct,json
from verify_native import Machine
from unicorn.x86_const import *
from verification_paths import S, D, BASE
def make():
 m=Machine(D/'build/key_repeat.payload',D/'build/key_repeat.map',BASE)
 # Load actual executable code; restore adapter stubs overwritten by the image.
 image=m.pe.get_memory_mapped_image();base=m.pe.OPTIONAL_HEADER.ImageBase
 start=None
 for page in range(base,base+((len(image)+4095)&~4095)+4096,4096):
  missing=page<base+len(image) and page not in m.pages
  if missing and start is None:start=page
  if not missing and start is not None:
   m.u.mem_map(start,page-start);m.pages.update(range(start,page,4096));start=None
 m.u.mem_write(base,image)
 print('native image mapped',flush=True)
 for addr in m.mocks:m.put(addr,b'\xc3')
 m.put(0,bytes(4096));m.write(0x55b3bdc,0x48a90123);m.mock(0x3e72ce4)
 m.heap=m.S+0x200000
 def alloc():
  p=m.heap;n=m.arg();assert 0<n<0x100000;m.heap+=(n+15)&~15;return p
 m.mock(0x8a9480,alloc);m.mock(0x37f2170,alloc);m.mock(0x3e70882)
 m.put(m.g['loaded'],b'\1');m.put(m.g['menuLoaded'],b'\1');m.put(m.g['menuSelected'],b'\1')
 return m

def verify():
 m=make();data=m.S+0x10000
 print('construct map',flush=True);m.call(0xfa7220,(0,0),8,ecx=data);print('map ready',flush=True)
 assert m.read(data+12)==0
 for i in range(3):assert m.call(m.exports['MenuRows'],ecx=data)==data
 assert m.read(data+12)==1
 node=m.call(m.g['menuNode'],(data,7200));assert node
 assert m.wide(node+0x14)=='按键连发'
 assert tuple(m.read(node+o) for o in [0x30,0x34,0x38,0x3c])==(30,31,30,48)

 # Add native neighboring rows with the actual constructor and insertion code.
 pair=m.S+0x11000;result=pair+0x100;text=pair+0x200
 for ident,kind,title in [(1000,0,'我的信息'),(1001,1,'物品栏')]:
  m.put(pair,bytes(52));m.write(pair,ident);m.put(text,(title+'\0').encode('utf-16le'))
  m.call(0x45fea0,(text,),4,ecx=pair+4)
  for off,val in [(36,2),(40,3),(44,2),(48,kind)]:m.write(pair+off,val)
  m.call(0x2287990,(result,pair),8,ecx=data);m.call(0x1c5f110,ecx=pair)
 # Apply the exact reviewed code edits to the emulated original image.
 build=json.loads((D/'client-build.json').read_text('utf8'))
 for e in build['edits']:m.put(e['va'],bytes.fromhex(e['after']))
 menu=m.S+0x20000;root=m.S+0x21000;vt=m.S+0x22000;rc=m.S+0x23000
 m.write(menu+0x30,root);m.write(root,vt);m.write(rc+4,10000);m.write(rc+8,10000)
 m.call(0x168e100,(0,0),8,ecx=menu+0x254)
 m.call(0x8a3100,(0,0),8,ecx=menu+0x268)
 m.call(0x8a3100,(0,0),8,ecx=menu+0x290)
 m.call(0xfa7220,(0,0),8,ecx=data+0x14);m.call(0xfa7220,(0,0),8,ecx=data+0x28)
 controls={k:m.S+0x30000+i*0x1000 for i,k in enumerate([1000,1001,7200])}
 checks={};shown={};positions={};sizes={};nextcheck=[m.S+0x40000]
 def reg(offset,fn,pop):
  addr=m.S+0x24000+offset*4;m.write(vt+offset,addr);m.mock(addr,fn,pop)
 def receiver():return m.u.reg_read(UC_X86_REG_ECX)
 def ref(out,obj):m.write(out,obj);m.write(out+4,rc if obj else 0);return out
 for i,(k,obj) in enumerate(controls.items()):
  m.write(obj,vt);m.write(obj+0x64,20+53*i);m.write(obj+0x68,54);m.put(obj+0x60,b'\1')
 m.mock(0x2677d30,root);m.mock(0x22880e0,data)
 m.mock(0x29b7b00,lambda:ref(m.arg(),controls.get(m.arg(1),checks.get(m.arg(1),0))),8)
 m.mock(0x8c76e0,lambda:ref(m.arg(),m.read(m.arg(1))))
 m.mock(0x37de240,m.S+0x25000)
 m.write(0x581a3e4,root)
 reg(8,lambda:shown.update({receiver():bool(m.arg()&255)}),4)
 # Resource manager's +8 differs from control Show; use a separate vtable.
 asset=m.S+0x26000;assetvt=asset+0x100;m.write(asset,assetvt);m.write(0x581a3e4,asset)
 m.write(assetvt+8,asset+0x200);m.mock(asset+0x200,lambda:ref(m.arg(),asset),8)
 reg(0x3c,lambda:0,8);reg(0x54,lambda:0,4)
 reg(0x5c,lambda:sizes.update({receiver():(m.arg(),m.arg(1))}),8)
 m.mock(0x3826f50,menu+0x1b0)
 m.mock(0x37e9ec0,lambda:checks.update({m.arg():receiver()}),4)
 m.mock(0x37ea190,lambda:positions.update({receiver():(m.arg(),m.arg(1))}),8)
 m.mock(0x37ea560,lambda:positions.update({receiver():(m.arg(),m.arg(1))}),8)
 def newcheck():
  assert [m.arg(i) for i in range(3,8)]==[43,91,42,92,0xffffffff]
  obj=nextcheck[0];nextcheck[0]+=0x1000;m.write(obj,vt);return ref(m.arg(),obj)
 m.mock(0x3842740,newcheck)
 # Real native creation, tree insertion, ID assignment and repeat-open reuse.
 m.call(0x1227b30,(data,),4,ecx=menu)
 assert set(checks)=={11000,11001,17200} and m.read(menu+0x260)==3,checks
 end=nextcheck[0];m.call(0x1227b30,(data,),4,ecx=menu);assert nextcheck[0]==end
 private=checks[17200]

 # Reproduce the actual old failure through BOTH native initial Show and
 # periodic enable callbacks. The predicate and setter/getter execute natively.
 m.write(vt+0xc,0x1f0b7a0)
 m.mock(0x1fa2790,menu);m.mock(0x2526b50,1);m.mock(0x2526ef0,0xffffffff,4)
 enableEdits=[e for e in build['edits'] if e['label']=='MenuEnabled'];assert len(enableEdits)==2
 for e in enableEdits:m.put(e['va'],bytes.fromhex(e['before']))
 oldFailures=[]
 for callback in ['show','periodic']:
  m.put(controls[7200]+0x61,b'\1')
  if callback=='show':m.call(0x1224fe0,(controls[7200],rc,48))
  else:m.call(0x1225ca0,(7200,48),8,ecx=menu)
  assert m.call(0x3a01ce0,ecx=controls[7200])&255==0
  oldFailures.append(callback)
 for e in enableEdits:m.put(e['va'],bytes.fromhex(e['after']))
 m.u.ctl_remove_cache(0x1224fe0,0x1225da0)
 for repeat in range(3):
  m.call(0x1224fe0,(controls[7200],rc,48));assert m.call(0x3a01ce0,ecx=controls[7200])&255==1
  m.call(0x1225ca0,(7200,48),8,ecx=menu);assert m.call(0x3a01ce0,ecx=controls[7200])&255==1
 # Compare the unchanged native branch with the wrapper for all other types.
 # Adapt the original predicate only for this forwarding contract check.
 preserved=[]
 def originalPredicate():preserved.append(m.arg());return 0x12000000|(m.arg()%2)
 nativePredicate=m.native(0x12249e0,1);m.mock(0x12249e0,originalPredicate)
 for typ in list(range(48))+[49,100]:
  assert m.call(m.exports['MenuEnabled'],(typ,))&255==typ%2
 assert preserved==list(range(48))+[49,100]
 m.mocks.pop(0x12249e0);m.put(0x12249e0,nativePredicate)
 # Known fixture native rows and the new row must enter drawing with real
 # enabled bytes, never a constant mocked 'enabled=true' answer.
 for ident,obj in controls.items():
  if ident!=7200:m.call(0x1f0b7a0,(1,),4,ecx=obj)
 # Real checkbox visibility/state/position traversal. Native checkbox setter
 # has notification/UI side effects; retain its storage semantics at endpoint.
 m.mock(0x3840b20,lambda:m.put(receiver()+0x578,bytes([bool(m.arg())])),4)
 m.mock(0x209bbe0,lambda:m.read(receiver()+0x64));m.mock(0x37e9f40,lambda:m.read(receiver()+0x68))
 m.call(0x12283c0,(1,),4,ecx=menu)
 assert shown[private] and m.byte(private+0x578)==1
 assert positions[private]==(158,54),positions
 # Real native save reads all native checkboxes, sends exactly 47 records and
 # skips private enum 48 without indexing past its stack array.
 packets=[];m.mock(0x374ea10,m.S+0x27000)
 m.mock(0x374f280,lambda:packets.append(('opcode',m.arg())),4)
 m.mock(0x374eeb0,lambda:packets.append(('body',bytes(m.u.mem_read(m.arg(),m.arg(1))))),8)
 m.mock(0x3750ac0)
 m.put(m.g['configPath'],'C:\\fixture.ini\0'.encode('utf-16le'))
 saved={};storage={'ok':True}
 def writeconfig():
  assert m.wide(m.arg(1))=='MenuVisibleV1'
  if storage['ok']:saved['value']=bytes(m.u.mem_read(m.arg(2),4))
  return storage['ok']
 def readconfig():
  if 'value' in saved:m.put(m.arg(2),saved['value']);return 1
  return 0
 m.iat(0x6e85b58,writeconfig,20);m.iat(0x6e85b60,readconfig,20)
 m.put(private+0x578,b'\0');m.call(m.exports['MenuSave'],ecx=menu)
 assert not m.byte(m.g['menuSelected']) and saved['value']==bytes(4)
 assert packets[0]==('opcode',0x353) and len(packets[1][1])==188,packets
 assert m.call(0x2284490,(48,),4,ecx=data)&255==0
 # In edit mode hidden entries remain visible and can be checked again.
 m.call(0x12283c0,(1,),4,ecx=menu);assert shown[private] and not m.byte(private+0x578)
 m.put(private+0x578,b'\1');m.call(m.exports['MenuSave'],ecx=menu)
 assert m.byte(m.g['menuSelected']) and saved['value']==b'\1\0\0\0'
 m.put(m.g['menuSelected'],b'\0');m.put(m.g['menuLoaded'],b'\0')
 assert m.call(0x2284490,(48,),4,ecx=data)&255==1,'reloaded preference'
 storage['ok']=False;m.put(private+0x578,b'\0');m.call(m.exports['MenuSave'],ecx=menu)
 assert m.byte(m.g['menuSelected']),'failed storage changed active visibility'
 # All native states remain governed by their original array getter.
 for kind in range(47):
  for hidden in [0,1]:
   m.put(data+0x3e+kind*4,struct.pack('<H',hidden))
   assert m.call(0x2284490,(kind,),4,ecx=data)&255==1-hidden

 # Execute the original row renderer, including its native hover plate/animation
 # and text color/origin. Rendering APIs are sinks; branch selection is native.
 for kind in range(47):m.put(data+0x3e+kind*4,b'\0\0')
 m.put(menu+0x1e0,b'\0');m.put(m.g['menuSelected'],b'\1')
 m.write(menu+0x20c,asset);m.write(menu+0x210,rc);m.write(menu+0x22c,asset)
 renderer=m.S+0x28000;rendervt=renderer+0x100;drawfn=renderer+0x200
 m.write(renderer,rendervt);m.write(rendervt+0x7c,drawfn);m.write(0x581a298,renderer)
 texts=[];glows=[];hover={'obj':0}
 m.mock(drawfn,lambda:texts.append((m.wide(m.arg(4)),m.arg(1),m.arg(2),m.arg(3))))
 m.mock(0x38269d0,lambda:receiver()==hover['obj'])
 m.mock(0x37f3b00,lambda:glows.append(('plate',m.arg(),m.arg(1))),16)
 m.mock(0x361c5c0,lambda:glows.append(('animation',m.arg(1),m.arg(2))),24)
 m.mock(0x361a790)
 m.call(0x1225e10,(data,0),8,ecx=menu)
 assert texts==[('我的信息',18,94,0xff9bc8e6),('物品栏',71,94,0xff9bc8e6),('按键连发',124,94,0xff9bc8e6)],texts
 assert not glows
 texts.clear();hover['obj']=controls[7200];m.call(0x1225e10,(data,0),8,ecx=menu)
 assert glows==[('plate',126,46),('animation',126,46)],glows
 assert texts[-1]==('按键连发',124,94,0xff9bc8e6)
 # Native reflow uses its 53-pixel grid, including the added enum in animation.
 # Initialize the real position vector and native per-row index map.
 vec=m.S+0x29000;proxy=vec+0x100;m.write(menu+0x27c,proxy);m.write(proxy,menu+0x27c)
 m.write(menu+0x280,vec);m.write(menu+0x284,vec+24);m.write(menu+0x288,vec+24)
 for i in range(3):m.write(vec+i*8,20+53*i);m.write(vec+i*8+4,54)
 m.mock(0x38168f0,0,8);m.mock(0x1225ab0)
 positions.clear();m.call(0x12268f0,ecx=menu)
 assert positions[controls[7200]]==(126,54),positions
 m.put(m.g['menuSelected'],b'\0');m.put(menu+0x1e0,b'\1');positions.clear();m.call(0x12268f0,ecx=menu)
 assert shown[controls[7200]] and positions[controls[7200]]==(126,54)
 m.put(menu+0x1e0,b'\0');m.call(0x12268f0,ecx=menu);assert not shown[controls[7200]]

 # Execute both compiled branch gates, including the exact former sentinel
 # and out-of-range cases. Unsupported IDs retain their original targets.
 branches=[];gatecases=0
 for name,yes,no in [('MenuCheckGate',0x1227cef,0x1227caa),('MenuReflowGate',0x1228c13,0x1228eb3)]:
  m.mock(yes,lambda:branches.append('yes'));m.mock(no,lambda:branches.append('no'))
  for typ in [0,25,46,47,48,49,100]:
   m.write(0x11111111+0x3c,typ);branches.clear();m.call(m.exports[name])
   assert branches==['yes' if typ<47 or typ==48 else 'no'];gatecases+=1
 print('real native registration, checkbox edit/restore and bounded save passed',flush=True)
 return dict(passed=True,realNativeMapAndStringConstruction=True,duplicateRegistrationSafe=True,
  oldDisabledRegression=oldFailures,initialAndPeriodicEnablePassed=True,otherTypeForwardingCases=50,realEnabledGetterInRenderer=True,nativeBranchGateCases=gatecases,nativeRendererTextColorSpacingAndHoverGlow=True,nativeHiddenAndEditReflow=True,nativeCheckboxCreationAndReuse=True,nativeCheckboxEditPlacement=True,hideSaveRestoreAndPersistence=True,
  failedStoragePreservesVisibility=True,native47PreferenceCases=94,nativeSaveBodyBytes=188,
  privateEnumExcludedFromNativeWireArray=True,payloadSha256=build['payloadSha256'],candidateSHA256=build['outputSha256'],gameExecuted=False,
  adapters=['allocation/free','resource and control lookup/creation','checkbox setter and graphics endpoints','INI storage','network writer'])
if __name__=='__main__':
 r=verify();(D/'menu-integration-tests.json').write_text(json.dumps(r,indent=2),'utf8');print(json.dumps(r,indent=2))
