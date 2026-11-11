from pathlib import Path
import sys,struct,json,re,hashlib,argparse
import pefile
from unicorn import Uc,UC_ARCH_X86,UC_MODE_32,UC_HOOK_CODE
from unicorn.x86_const import *
D=Path(__file__).parent
_p=argparse.ArgumentParser(description='Verify the unified growth-ticket client candidate (235 dynamic b[39] cases).')
_p.add_argument('--candidate',type=Path,default=D/'DNF.exe')
_p.add_argument('--receipt',type=Path,default=D/'client-receipt.json')
_p.add_argument('--map',type=Path,default=D/'growth.map')
_p.add_argument('--report',type=Path,default=D/'native-verification.json')
ARGS=_p.parse_args()
data=ARGS.candidate.read_bytes();p=pefile.PE(data=data)
r=json.loads(ARGS.receipt.read_text());exp=r['exports']
assert hashlib.sha256(data).hexdigest()==r['outputSHA256']
symbols={m.group(1):int(m.group(2),16) for m in re.finditer(r'\?([^@ ]+)@@\S+\s+([0-9a-fA-F]{8})\s+(?:f\s+)?growth.obj',ARGS.map.read_text())}
u=Uc(UC_ARCH_X86,UC_MODE_32);u.mem_map(0x400000,(p.OPTIONAL_HEADER.SizeOfImage+4095)&~4095)
for s in p.sections:u.mem_write(0x400000+s.VirtualAddress,s.get_data())
u.mem_map(0,4096);u.mem_map(0x20000000,0x3000000)
STOP=0x22000000;BODY=0x22001000;STACK=0x20f00000;QUERY=0x22000040;TIME=0x22000060;THREAD=0x22000080;CLOSE=0x220000c0
INV=0x22100000;TARGET=0x22102000;SOURCE=0x22104000;WIN=0x22110000;MGR=0x22120000;VT=0x22140000;VEC=0x22150000;CTL=0x22170000;DEF=0x22180000;NET=0x22200000
SECOND=0x22190000;SECONDVT=0x22191000;YES=0x22192000;NO=0x22193000;SESSION=0x22194000
WINVT=0x22195000;TARGETVT=0x22196000;ROOT=0x22197000;GETROOT=0x22000200;GETDEF=0x22000220;COUNT=0x22000240
u.mem_write(0x6e85b18,struct.pack('<I',QUERY));u.mem_write(0x6e85a90,struct.pack('<I',THREAD));u.mem_write(0x45881f0,struct.pack('<I',TIME))
rw=p.sections[-1];initial=bytes(u.mem_read(0x400000+rw.VirtualAddress,max(rw.Misc_VirtualSize,rw.SizeOfRawData)))
calls=[];packets=[];words=[];results=[];native_mode=0;clock_ms=1000;current=0;stop_at=set();normal_action=False
trace=[];tracing=False;currentThread=7;gameWindowAvailable=True
def word(a):return struct.unpack('<I',u.mem_read(a,4))[0]
def put(a,v):u.mem_write(a,struct.pack('<I',v))
def sv(name,v,n=4):u.mem_write(symbols[name],v.to_bytes(n,'little'))
def ret(v=0,pop=0):
 sp=u.reg_read(UC_X86_REG_ESP);pc=word(sp);u.reg_write(UC_X86_REG_EAX,v&0xffffffff);u.reg_write(UC_X86_REG_ESP,sp+4+pop);u.reg_write(UC_X86_REG_EIP,pc)
def active_window(opened):
 h=MGR+(0x309*5+0x41)*4;put(h+4,VEC);put(h+8,VEC+4 if opened else VEC)
def hook(uc,at,size,_):
 global native_mode,current,words
 if tracing:
  trace.append(at)
  if len(trace)>40:trace.pop(0)
 if at==STOP or at in stop_at:uc.emu_stop();return
 sp=uc.reg_read(UC_X86_REG_ESP);self=uc.reg_read(UC_X86_REG_ECX)
 if at==QUERY:
  put(word(sp+8)+0,0x400000);put(word(sp+8)+12,0x23000000-0x400000);put(word(sp+8)+16,0x1000);put(word(sp+8)+20,4);ret(28,12)
 elif at==THREAD:ret(currentThread)
 elif at==0x22000320:ret(1234)
 elif at==0x22000340:
  hwnd=word(sp+4);put(word(sp+8),5678 if hwnd==1 else 1234);ret(7 if hwnd in (5,6) else 9,8)
 elif at==0x22000360:ret(123 if word(sp+4)==2 else 0,8)
 elif at==0x22000380:ret(0 if word(sp+4)==3 or (word(sp+4) in (5,6) and not gameWindowAvailable) else 1,4)
 elif at==0x220003a0:
  dst=word(sp+8);put(dst,0);put(dst+4,0);put(dst+8,100 if word(sp+4)==4 else 800);put(dst+12,100 if word(sp+4)==4 else 600);ret(1,8)
 elif at==0x220003c0:ret(0,12) # Suppress bounded diagnostic file IO in emulator.
 elif at==0x251e280:calls.append(('register',word(sp+4),word(sp+8),word(sp+12),currentThread));ret(0,12)
 elif at==TIME:ret(clock_ms)
 elif at==0x3819d00:ret(native_mode)
 elif at in (0x32c0830,0x3819ce0):native_mode=word(sp+4);calls.append(('mode',native_mode));ret(0,4 if at==0x32c0830 else 0)
 elif at==0x3e73cb6:ret(word(sp+4)) # RTTI leaf only; real cached getter/lookup execute.
 elif at==0x32b94a0:ret(0,12)
 elif at==0x3754450:calls.append(('metadata',word(sp+4)));ret(DEF,8)
 elif at==0x25eb680:ret(TARGET if word(sp+4)==10 else SOURCE if word(sp+4)==9 else 0,4)
 elif at==0x25ece10:ret(10 if word(sp+4)==TARGET else 9 if word(sp+4)==SOURCE else -1,4)
 elif at==0x3e4b210:ret()
 elif at==0x32b5400:ret()
 elif at==0x255ee50:ret(0)
 elif at==0x36feeb0:ret(word(self+0x1e4))
 elif at==0x32b55e0:ret(0,4)
 elif at==0x29ba8f0:ret()
 elif at==0x335b310:ret(0,4)
 elif at==0x3826970:ret(1 if self==YES else 0)
 elif at==0x1fa2790:ret(SESSION)
 elif at==0x2526b50:ret(1)
 elif at==0x391b870:ret(self)
 elif at==0x12e3250:calls.append(('original-final-yes-routine',self))
 elif at==GETROOT:dst=word(sp+4);put(dst,ROOT);put(dst+4,0);ret(dst,4)
 elif at==GETDEF:ret(DEF)
 elif at==COUNT:ret(1)
 elif at==0x3826c80:ret(1)
 elif at==0x1d46f10:ret(0)
 elif at==0x2534e00:ret(1)
 elif at==0x2531a80:ret(INV)
 elif at==0x25ea1a0:ret(0,4)
 elif at in (0x33124a0,0x330d290):ret(1)
 elif at==0x3283780:ret(0)
 elif at==0x230d520:ret(self,4)
 elif at in (0x330d970,0x38160c0):ret()
 elif at==0x3816810:ret(0,8)
 elif at==0x3816310:ret(2)
 elif at==0x25ea1b0:ret(1)
 elif at==0x25f0200:ret(0,4)
 elif at in (0x39a6620,0x37de240):ret(BODY)
 elif at==0x25f8620:ret(0,28)
 elif at==0x23a9df0:ret(SESSION)
 elif at==0x23a9dc0:ret()
 elif at==0x12de9a0:calls.append(('native-first-refresh',word(self+0x288)));ret()
 elif at==0x32bea50:
  wid,arg,kind=word(sp+4),word(sp+8),word(sp+12)
  calls.append(('native-window',wid,arg,kind))
  if wid==0x23b:
   assert arg==0 and kind in (11,12);current=CTL;put(CTL+0x1e4,kind)
   h=MGR+(0x23b*5+0x41)*4;put(h+4,VEC+16);put(h+8,VEC+20);put(VEC+16,CTL)
   calls.append(('native-controller',kind));ret(CTL,12)
  elif wid==0x309:assert arg==TARGET and u.mem_read(symbols['busy'],1)==b'\1';put(WIN+0x284,TARGET);active_window(True);ret(WIN,12)
  elif wid==0x491:assert arg==WIN and kind==0;put(0x56694ac,WIN);ret(SECOND,12)
  elif wid==0x41c:
   assert arg==TARGET and kind==0
   calls.append(('native-result',word(0x55a761c),word(0x55a762c)));ret(WIN,12)
  else:raise AssertionError(('unexpected window',hex(wid)))
 elif at==0x3316a40:put(CTL+0x1ec,0);put(CTL+0x220,0);put(CTL+0x228,0);calls.append(('native-complete',));ret(0,4)
 elif at==CLOSE:
  if self==SECOND:calls.append(('native-ticket-confirm-close',));ret()
  else:
   current=0;active_window(False);h=MGR+(0x23b*5+0x41)*4;put(h+8,word(h+4));calls.append(('native-close',));ret()
 elif at==0x374ea10:ret(NET)
 elif at==0x374f280:assert word(sp+4)==0x7f20;words=[];calls.append(('begin-send',));ret(1,4)
 elif at==0x374f3f0:words.append(word(sp+4));ret(1,4)
 elif at==0x3750ac0:packets.append(b''.join(struct.pack('<I',v) for v in words));ret(1)
 elif at==0x2531940:calls.append(('hint',));ret()
 elif at==0x2531a60:ret()
 elif at==0x3e730d0:
  dst,src,n=word(sp+4),word(sp+8),word(sp+12);uc.mem_write(dst,bytes(uc.mem_read(src,n)));ret(dst)
u.hook_add(UC_HOOK_CODE,hook)

for at,to in [(0x6e85a68,0x22000320),(0x6e8672c,0x22000340),(0x6e86728,0x22000360),(0x6e866f0,0x22000380),(0x6e866d8,0x220003a0),(0x6f2106c,0x22000300),(0x6e85aa8,0x220003c0)]:put(at,to)
# Simulated EnumWindows leaf executes the real compiled findGameWindow callback.
c=bytearray.fromhex('558bec53568b5d088b750c');jumps=[]
for hwnd in [1,2,3,4,5,6]:
 c+=b'\x56\x68'+struct.pack('<I',hwnd)+bytes.fromhex('ffd385c07400');jumps.append(len(c)-1)
end=len(c);c+=bytes.fromhex('b8010000005e5b8be55dc20800')
for at in jumps:c[at]=end-at-1
u.mem_write(0x22000300,bytes(c))

def call(entry,args=(),self=MGR):
 global tracing,trace
 tracing=entry==0x12daa00;trace=[]
 u.mem_write(STACK,struct.pack('<I',STOP)+b''.join(struct.pack('<I',x&0xffffffff) for x in args))
 u.reg_write(UC_X86_REG_ESP,STACK);u.reg_write(UC_X86_REG_ECX,self);u.reg_write(UC_X86_REG_EDX,0)
 before=[u.reg_read(x) for x in (UC_X86_REG_EBX,UC_X86_REG_ESI,UC_X86_REG_EDI,UC_X86_REG_EBP)]
 try:u.emu_start(entry,STOP,count=800000)
 except Exception as e:raise RuntimeError((hex(u.reg_read(UC_X86_REG_EIP)),hex(u.reg_read(UC_X86_REG_ESP)),calls[-14:],[hex(x) for x in trace])) from e
 assert u.reg_read(UC_X86_REG_EIP) in stop_at|{STOP},hex(u.reg_read(UC_X86_REG_EIP))
 if not stop_at:assert before==[u.reg_read(x) for x in (UC_X86_REG_EBX,UC_X86_REG_ESI,UC_X86_REG_EDI,UC_X86_REG_EBP)]
 return u.reg_read(UC_X86_REG_EAX)
def reset(idk=0,attr=0,level=12,run=True):
 global calls,packets,words,native_mode,clock_ms,current,stop_at,currentThread,gameWindowAvailable
 currentThread=7;gameWindowAvailable=True
 calls=[];packets=[];words=[];native_mode=0;clock_ms=1000;current=0;stop_at=set()
 kind=1 if attr else 0
 u.mem_write(0x400000+rw.VirtualAddress,initial);u.mem_write(MGR,bytes(0xc640));u.mem_write(CTL,bytes(0x4d8))
 # Native lookup 32ba120 returns a cached controller even with no window open.
 h=MGR+(0x23b*5+0x1879)*4;put(h+4,VEC+8);put(h+8,VEC+12);put(VEC+8,CTL)
 put(CTL,VT);put(VT+0x7c,CLOSE);put(0x56a3798,MGR);put(0x56a379c,INV)
 put(SECOND,SECONDVT);put(SECONDVT+0x7c,CLOSE);put(SECOND+0x20c,YES);put(SECOND+0x214,NO);put(WIN+0x26c,YES);put(WIN+0x274,0)
 put(WIN,WINVT);put(WINVT+0x80,GETROOT);put(WINVT+0x7c,CLOSE);put(TARGET,TARGETVT);put(TARGETVT+0x28,GETDEF);put(TARGETVT+0x48,COUNT);put(TARGETVT+4,COUNT)
 put(CTL+0x128,MGR);put(WIN+0x128,MGR);put(SECOND+0x128,MGR)
 put(CTL+0x224,0)
 put(TARGET+0x29c,INV);put(TARGET+0x20,101040493);put(SOURCE+0x29c,INV);put(SOURCE+0x20,990033101+idk)
 put(NET+8,1);put(NET+0x2bcc34,0);put(NET+0x2bcc2c,0)
 sv('threadId',7);sv('registered',1,1)
 b=bytearray(5424);b[:4]=b'GTS1';struct.pack_into('<H',b,4,1);struct.pack_into('<H',b,10,1);b[16:32]=bytes(range(16))
 struct.pack_into('<I',b,32,990033101+idk);struct.pack_into('<H',b,36,9);b[38]=level;b[39]=attr
 struct.pack_into('<H',b,48,10);b[50]=1 if level>1 else 0;struct.pack_into('<I',b,52,101040493)
 u.mem_write(symbols['response'],bytes(b));sv('responseReady',1,1)
 if not run:return b
 assert call(0x32bac90)==CTL # cached getter is deliberately non-null before begin
 call(exp['Tick']);assert word(CTL+0x1e8)==990034000+kind*100+level
 assert native_mode==(30 if kind else 20) and not packets
 return b
def record(name):results.append(dict(name=name,passed=True))

# Dynamic b[39] template selection: the unified shop ID 990033101 opens the
# strengthen window for raw attribute 0 and the universal amplify window for
# raw attributes 1..4, matching begin() k=state[39]?1:0.
for attr in range(5):
 for level in range(1,32):
  b=reset(0,attr,level);kind=1 if attr else 0;meta=990034000+kind*100+level
  assert call(exp['FindSource'],[meta],INV)==9
  assert call(exp['Hover'],[TARGET])==2
  call(exp['Action'],[64 if kind else 12,TARGET,0,0,0]);assert not any(c[0]=='native-window' and c[1]!=0x23b for c in calls) and not packets
  # Execute the complete original native action branch through its real window ID and mode setter.
  call(exp['Action'],[64 if kind else 12,TARGET,1,0,0]);assert ('native-window',0x309,TARGET,1) in calls and not packets
  assert ('native-first-refresh',kind) in calls and native_mode==0
  put(CTL+0x220,TARGET);put(CTL+0x228,SOURCE);put(CTL+0x1ec,1)
  assert call(exp['NativeSend'],[TARGET,SOURCE,kind],INV)&255
  assert len(packets)==1 and len(packets[0])==32 and packets[0][:4]==b'GTQ1' and packets[0][16:]==b[16:32]
  put(CTL+0x1ec,2);assert call(exp['NativeSend'],[TARGET,SOURCE,kind],INV)&255;assert len(packets)==1
  result=bytearray(5424);result[:4]=b'GTS1';struct.pack_into('<H',result,4,1);struct.pack_into('<H',result,6,3)
  u.mem_write(symbols['response'],bytes(result));sv('responseReady',1,1);call(exp['Tick'])
  assert ('native-result',kind,b[50]) in calls and len(packets)==1 and current==0
  record(f'dynamic-b39-window-attr{attr}-level{level}')
for attr in range(5):
 reset(0,attr);kind=1 if attr else 0;call(exp['Action'],[64 if kind else 12,TARGET,1,0,0])
 # The real first-window button code opens UpgradeConfirmForTicket (491).
 call(0x12e3840,[],WIN)
 assert ('native-window',0x491,WIN,0) in calls and not packets
 call(0x12daa00,[NO,13,0,0],SECOND+0x1b0)
 assert ('native-ticket-confirm-close',) in calls and not packets and not any(c[0]=='original-final-yes-routine' for c in calls)
 call(0x12daa00,[YES,13,0,0],SECOND+0x1b0)
 assert ('original-final-yes-routine',WIN) in calls and len(packets)==1
 record(f'dynamic-first-yes-and-ticket-confirm-cancel-yes-dispatch-attr{attr}')
for cause in ('cancel-first','expired','changed-source','changed-target','wrong-mode','net-busy'):
 reset();call(exp['Action'],[12,TARGET,1,0,0]);put(CTL+0x220,TARGET);put(CTL+0x228,SOURCE)
 if cause=='cancel-first':active_window(False);call(exp['Tick']);assert current==0
 elif cause=='expired':clock_ms+=120001;call(exp['Tick']);assert current==0
 else:
  put(CTL+0x1ec,1)
  if cause=='changed-source':put(SOURCE+0x20,123)
  if cause=='changed-target':put(TARGET+0x20,123)
  if cause=='net-busy':put(NET+0x2bcc34,1)
  assert not call(exp['NativeSend'],[TARGET,SOURCE,1 if cause=='wrong-mode' else 0],INV)&255
 assert not packets;record(cause+'-no-consumption-request')
for name,address,args,self in [('FindSource',0x25eca16,[3037],INV),('NativeSend',0x25fdab5,[TARGET,SOURCE,0],INV),('Validate',0x32c9fc5,[1,TARGET,0,0,0,0],MGR),('Action',0x32d2089,[1,TARGET,1,0,0],MGR)]:
 reset();sv('ready',0,1);sv('awaiting',0,1);stop_at={address};call(exp[name],args,self);record(name+'-ordinary-native-trampoline')

for order in ('worker-first','window-late','ui-first','queued-on-worker'):
 for attr in range(5):
  kind=1 if attr else 0
  for level in (1,7,31):
   b=reset(0,attr,level,False);sv('threadId',0);sv('registered',0,1);sv('responseReady',0,1)
   if order in ('worker-first','window-late'):
    currentThread=9;gameWindowAvailable=order!='window-late';call(exp['Tick'])
    assert word(symbols['threadId'])==(7 if gameWindowAvailable else 0)
    assert u.mem_read(symbols['registered'],1)==b'\0' and not any(c[0]=='native-controller' for c in calls)
   currentThread=7;gameWindowAvailable=True;call(exp['Tick'])
   registrations=[c for c in calls if c[0]=='register'];assert registrations==[('register',0x7f20,exp['Receive'],0,7)],registrations
   currentThread=55;u.mem_write(BODY,bytes(b));put(0x57f9ad0,BODY);put(0x57f9ad4,len(b));call(exp['Receive'])
   assert word(0x57f9ad4)==0 and word(0x57f9ad0)==BODY+len(b) # Real native reader advanced.
   assert u.mem_read(symbols['responseReady'],1)==b'\1'
   if order=='queued-on-worker':
    currentThread=9;call(exp['Tick']);assert u.mem_read(symbols['responseReady'],1)==b'\1'
   currentThread=7;call(exp['Tick']);assert native_mode==(30 if kind else 20)
   assert ('native-controller',12 if kind else 11) in calls and not packets
   record(f'window-owner-bootstrap-receive-{order}-attr{attr}-{level}')

# Legacy four IDs route by their real raw attribute (server sends b[39]), not the old per-ID template index.
for idk,attr in ((1,1),(2,3),(3,2),(4,4)):
 b=reset(idk,attr,12);meta=990034000+1*100+12
 assert call(exp['FindSource'],[meta],INV)==9
 assert call(exp['Hover'],[TARGET])==2
 call(exp['Action'],[64,TARGET,1,0,0]);assert ('native-window',0x309,TARGET,1) in calls and not packets
 assert ('native-first-refresh',1) in calls and native_mode==0
 put(CTL+0x220,TARGET);put(CTL+0x228,SOURCE);put(CTL+0x1ec,1)
 assert call(exp['NativeSend'],[TARGET,SOURCE,1],INV)&255
 assert len(packets)==1 and packets[0][:4]==b'GTQ1'
 record(f'legacy-id{idk}-real-attr{attr}-amplify')
# A legacy ID whose raw attribute is 0 still opens strengthen; no new client ID restriction.
reset(1,0,12);meta=990034000+12
assert call(exp['FindSource'],[meta],INV)==9
assert call(exp['Hover'],[TARGET])==2
call(exp['Action'],[12,TARGET,1,0,0]);assert ('native-window',0x309,TARGET,1) in calls and not packets
assert ('native-first-refresh',0) in calls and native_mode==0
record('legacy-id-strengthen-no-restriction')

assert set(exp)=={'Action','FindSource','Gate','Getter','Hover','HoverGate','NativeSend','Receive','Tick','Validate'}
assert not any(x in (D/'growth.cpp').read_text('utf8') for x in ('GrowthConfirm.xui','GT_YES','GT_NO','layoutXml'))
out=dict(passed=True,actualCompiledWindowOwnerCallbackExecuted=True,realNativeReaderExecuted=True,backgroundBootstrapCases=60,nativeCases=len(results),cases=results,sha256=r['outputSHA256'],originalActionExecuted=True,
 nativeUIClassesUnchanged=True,originalTicketConfirmationDispatchExecuted=True,originalFinalYesControllerStateMachineExecuted=True,all155DynamicAttributeMappingsCovered=True,legacyFourIdByRealTypeCovered=True,noClientIdRestriction=True,externalBoundaryMocks=['Windows APIs','native control rendering/window factory','catalog lookup','inventory objects/count/payment validation','native timers/string/preview leaves','network transport'],
 fullGameGUIExecuted=False,gameAcceptance=False,testScriptSHA256=hashlib.sha256(Path(__file__).read_bytes()).hexdigest())
ARGS.report.write_text(json.dumps(out,indent=2),'utf8');print(json.dumps({k:out[k] for k in ('passed','nativeCases','sha256','originalActionExecuted')}))
