from pathlib import Path
import datetime,hashlib,json,sys
def sha(p):return hashlib.sha256(Path(p).read_bytes()).hexdigest()
def now():return datetime.datetime.now(datetime.timezone.utc).isoformat()
def save(p,v):Path(p).write_text(json.dumps(v,indent=2)+'\n','utf8')
if len(sys.argv)!=4:raise SystemExit('usage: verify_native.py BASE_EXE CANDIDATE_EXE REPORT_JSON')
import struct,pefile
from unicorn import Uc,UC_ARCH_X86,UC_MODE_32,UC_HOOK_CODE
from unicorn.x86_const import *
before=Path(sys.argv[1])
after=Path(sys.argv[2])
cases=[];gate_values=[]
for path in (before,after):
 raw=path.read_bytes();pe=pefile.PE(data=raw,fast_load=True);base=pe.OPTIONAL_HEADER.ImageBase
 assert pe.OPTIONAL_HEADER.SizeOfHeaders<=min(sec.VirtualAddress for sec in pe.sections),'headers overlap mapped section'
 u=Uc(UC_ARCH_X86,UC_MODE_32);u.mem_map(base,(pe.OPTIONAL_HEADER.SizeOfImage+4095)//4096*4096)
 u.mem_write(base,raw[:pe.OPTIONAL_HEADER.SizeOfHeaders])
 for sec in pe.sections:u.mem_write(base+sec.VirtualAddress,sec.get_data())
 u.mem_map(0,4096);u.mem_map(0x10000000,0x20000);u.mem_map(0x20000000,0x100000)
 SP=0x10010000;STOP=0x10001000;WORLD=0x20010000;PLAYER=0x20020000;NPC=0x20030000;DATA=0x20040000;VT=0x20050000
 GETTER=0x20000100;ANIMATION=0x20000200;GOLD=0x20000300;GOLDOBJ=0x20060000;ITEM=0x20070000;MATCH=0x20000400;SETCOUNT=0x20000500
 state={}
 def r(a):return struct.unpack('<I',u.mem_read(a,4))[0]
 def w(a,v):u.mem_write(a,struct.pack('<I',v&0xffffffff))
 def ret(value=0,pop=0):
  sp=u.reg_read(UC_X86_REG_ESP);u.reg_write(UC_X86_REG_EAX,value&0xffffffff);u.reg_write(UC_X86_REG_EIP,r(sp));u.reg_write(UC_X86_REG_ESP,sp+4+pop)
 def vector(v,storage):
  w(v,0);w(v+4,storage);w(v+8,storage);w(v+12,storage+28*8)
 def rows(v):return [list(struct.unpack('<7I',u.mem_read(a,28))) for a in range(r(v+4),r(v+8),28)]
 def hook(_,at,size,ctx):
  if at==STOP:u.emu_stop();return
  if state.get('mode')=='gate':return
  sp=u.reg_read(UC_X86_REG_ESP)
  if at in (0x37e82e0,0x37e8350,0x37e83c0):
   n=r(sp+8) if at==0x37e82e0 else 2 if at==0x37e8350 else 4
   pos=state['cursor'];assert pos+n<=len(state['body']),('overread',hex(at),pos,n,len(state['body']))
   u.mem_write(r(sp+4),state['body'][pos:pos+n]);state['cursor']+=n;ret(1);return
  if at in (0x2047360,0x22bb310):ret(WORLD,pop=4);return
  if at==0x22bb930:ret(0);return
  if at==0x2046ef0:ret(1);return
  if at in (0x2046e00,0x2047010,0x1fa2790,0x2524fb0):ret(WORLD);return
  if at==0x29044e0:state['npc_id']=r(sp+4);ret(pop=4);return
  if at==0x29044d0:ret(state.get('npc_id',1003));return
  if at==0x287a8d0:ret(0);return
  if at==0x2674450:ret(NPC if state.get('actor',True) else 0);return
  if at==GETTER:ret(DATA);return
  if at==0x288a260:ret(pop=32);return
  if at==0x2974420:ret(19);return
  if at==0x2974890:ret(PLAYER,pop=4);return
  if at==0x36a5de0:
   v=u.reg_read(UC_X86_REG_ECX);w(v+8,r(v+4));ret();return
  if at in (0x2d4f2e0,0x3a249d0):u.mem_write(u.reg_read(UC_X86_REG_ECX),bytes(28));ret();return
  if at in (0x2678080,ANIMATION):ret(pop=4);return
  if at==0x106d4e0:raise AssertionError('unexpected allocation; bounded fixture capacity exceeded')
  if at==0x3e738b1:raise AssertionError('native STL fail-fast')
  # Buy decoder retains the actual field reads; model mutations/localized UI
  # are explicit boundaries. Continue at its native final-stock field reader.
  if at==0x32b94a0:ret(pop=12);return
  if at==0x1d44630:ret(0);return
  if at==0x2531a80:
   bp=u.reg_read(UC_X86_REG_EBP)
   state['ack_fields']={k:r(bp+o) for k,o in [('gold',-0x2fc),('slot',-0x2e4),('item',-0x2e0),('value',-0x2f0),('ext',-0x2d9),('durability',-0x2ec),('cost',-0x2f8),('cost_remaining',-0x2f4)]}
   # This scene/UI fixture substitutes downstream inventory drawing only.
   u.reg_write(UC_X86_REG_EIP,0x1fa9cec);return
  if at==0x1cc3d80:ret(pop=20);return
  if at==0x20a3150:ret(WORLD,pop=4);return
  if at==0x20a2250:ret();return
  if at==0x1fa9d1b:
   state['ack_fields']['stock']=r(u.reg_read(UC_X86_REG_EBP)-0x2e8);u.emu_stop();return
 u.hook_add(UC_HOOK_CODE,hook)
 def run(entry,body=b'',args=(),mode='receiver'):
  state['mode']=mode;state['body']=body;state['cursor']=0
  w(SP,STOP)
  for i,v in enumerate(args):w(SP+4+i*4,v)
  for reg,val in ((UC_X86_REG_ESP,SP),(UC_X86_REG_EBP,0),(UC_X86_REG_EFLAGS,0x202),(UC_X86_REG_EBX,0),(UC_X86_REG_ESI,0),(UC_X86_REG_EDI,0),(UC_X86_REG_ECX,0)) :u.reg_write(reg,val)
  u.emu_start(entry,0,count=200000)
  if mode!='gate':assert state['cursor']==len(body),(hex(entry),state['cursor'],len(body))
  return u.reg_read(UC_X86_REG_EAX)
 # Exhaust the exact u16 gate function in each image. All prior outer gate
 # helpers and call sites are separately byte-preserved by the PE builder.
 vals=[]
 for op in range(65536):vals.append(run(0x6e83610,args=(op,),mode='gate'))
 gate_values.append(vals)
 w(NPC,VT);w(VT+0x344,GETTER);w(VT+0x284,ANIMATION)
 vector(PLAYER+0x520,0x20080000);vector(DATA+0x410,0x20090000)
 run(0x1fea440,struct.pack('<I',1003));assert state['npc_id']==1003
 decoder_cases=[]
 for count in (2,4,1,0,4):
  entries=[(3033+i,i%2,100+i,200+i,0 if i%2==0 else 3037,0 if i%2==0 else 5+i,0) for i in range(count)]
  body=struct.pack('<I',count)+b''.join(struct.pack('<IBIIIIB',*e) for e in entries)
  run(0x1ff7440,body)
  assert rows(PLAYER+0x520)==[list(e) for e in entries]
  shelf=rows(DATA+0x410)
  if path==before:assert shelf==[]
  else:assert shelf==[list(e) for e in entries]
  assert r(DATA+0x424)==count
  decoder_cases.append(dict(count=count,playerRows=len(entries),npcShelfRows=len(shelf)))
 # No actor: real handler consumes the complete body and retains player rows.
 # Take the existing no-NPC-ID branch, which avoids native factory creation.
 run(0x1fea440,struct.pack('<I',1000));assert state['npc_id']==0
 run(0x1ff7440,struct.pack('<I',1)+struct.pack('<IBIIIIB',3033,0,100,10,0,0,0))
 assert len(rows(PLAYER+0x520))==1
 # Dispatcher consumes success byte before invoking class1 receiver.
 ack=struct.pack('<IHIIBHIII',987654,52,3033,3,0,0,0xffffffff,0,98)
 assert len(ack)==29
 run(0x1fa9960,ack,args=(0,1))
 fields=state['ack_fields'];assert fields['gold']==987654 and fields['slot']==52 and fields['item']==3033 and fields['value']==3 and fields['cost']==0xffffffff and fields['stock']==98,fields
 cases.append(dict(exe=str(path),sha256=sha(path),decoderCases=decoder_cases,emptyNPCSafe=True,ack=fields,scope='Native field readers, 2921b10 transform and 25fb4a0 copy; explicit scene lookup/vector-clear/UI fixtures. No actual rendering or client execution.'))
 print('native_receiver_complete',path.name,flush=True)
delta=[op for op in range(65536) if gate_values[0][op]!=gate_values[1][op]]
assert delta==[279,280,296],delta
save(sys.argv[3],dict(at=now(),passed=True,cases=cases,actualGateExecutions=131072,changedGateAdmissions=delta,harnessSHA256=sha(__file__),headerSectionOverlap=False,clientExecuted=False,visualAcceptance=False))
print(json.dumps(dict(passed=True,changedGateAdmissions=delta,cases=len(cases))))
