"""Execute the pinned native selection, snapshot and row handlers with explicit leaves."""
from pathlib import Path
import sys,struct,json,hashlib,importlib.util
import argparse
cli=argparse.ArgumentParser(description=__doc__)
cli.add_argument('--base',type=Path,required=True)
cli.add_argument('--candidate-dir',type=Path)
cli.add_argument('--candidate-image',type=Path,help='Verify a cumulative image using the paired candidate receipt.')
cli.add_argument('--output-dir',type=Path,required=True)
cliopts=cli.parse_args()
if cliopts.candidate_image is not None and cliopts.candidate_dir is None:cli.error('--candidate-image requires --candidate-dir')
B=cliopts.output_dir;B.mkdir(parents=True,exist_ok=True)
SCRIPTS=Path(__file__).resolve().parent.parent
# Requires unicorn; dependency location is supplied by the caller's environment.
from unicorn import Uc,UC_ARCH_X86,UC_MODE_32,UC_HOOK_CODE
from unicorn.x86_const import *
s=importlib.util.spec_from_file_location('buff_builder',SCRIPTS/'buff-local-snapshot/build_candidate.py');m=importlib.util.module_from_spec(s);s.loader.exec_module(m)
raw=cliopts.base.read_bytes();digest=hashlib.sha256(raw).hexdigest();assert digest=='9276348410bbf955f41a4d9e407f5288db883ff6edd68e66f30c83f56dfe3250'
pe=m.peutils.PE(raw)
def run(mode,skills,selected,rows=33,data=None,group=0,tagged=False,bound=False,fresh=False):
 image=m.peutils.PE(data) if data is not None else pe
 u=Uc(UC_ARCH_X86,UC_MODE_32);pages=set()
 def put(a,b):
  for page in range(a&~4095,(a+len(b)+4095)&~4095,4096):
   if page not in pages:u.mem_map(page,4096);pages.add(page)
  u.mem_write(a,bytes(b))
 def w(a,v):put(a,struct.pack('<I',v))
 def r(a):return struct.unpack('<I',u.mem_read(a,4))[0]
 stack,manager,actor,proxy,sentinel,record,stop=0x30008000,0x31000000,0x32000000,0x31001000,0x31002000,0x31004000,0x33000000
 put(0,bytes(4096));put(stack-4096,bytes(8192));put(manager,bytes(0x10000));put(stop,b'\xcc')
 for va,size in [(0x998530,0xc0),(0x998890,0x100),(0x9985f0,0x299),(m.ACTOR_RESET,0x19d),(m.HELPER,len(m.helper())),(0x994d20,0x60)]:put(va,image.read(va,size))
 if data is not None:
  receipt=json.loads((cliopts.candidate_dir/'build.json').read_text())
  for chunk in receipt['chunks']:put(chunk['va'],bytes.fromhex(chunk['hex']))
 w(0x55b3bdc,0x12345678);w(0x5656c08,0 if fresh else manager);w(0x57b6fb0,actor);w(0x56a3798,0x34000000);w(proxy,manager)
 for j,v in enumerate(skills):w(record+j*4,v)
 log=[];packet=bytearray([rows]);packet.extend(b''.join(bytes([1,j,0,54+group,0]) if bound and j==12 else bytes([0,j,45,255,255]) for j in range(rows)));pos=0
 if tagged:packet=bytearray([0xfe,group,selected&255,selected>>8])+packet
 trees={0:{12:54},1:{12:55}};treesBefore={k:dict(v) for k,v in trees.items()};outbound=[]
 debug=0x31006000;vtable=0x31007000;w(0x56a3790,debug);w(debug,vtable);w(vtable+8,0x34000100);w(vtable+0x24,0x34000200)
 w(0x35001000,0x35002000);w(0x35002000+0x154,0x34000300);w(0x37000000,12)
 def finish(clean=0,value=None):
  sp=u.reg_read(UC_X86_REG_ESP);dest=r(sp);u.reg_write(UC_X86_REG_ESP,sp+4+clean)
  if value is not None:u.reg_write(UC_X86_REG_EAX,value)
  u.reg_write(UC_X86_REG_ECX,0x12340000);u.reg_write(UC_X86_REG_EDX,0x12340001);u.reg_write(UC_X86_REG_EIP,dest)
 def hook(uc,a,n,_):
  nonlocal pos
  sp=uc.reg_read(UC_X86_REG_ESP)
  if a==stop:uc.emu_stop();return
  if a==0x1fa2790:finish(value=0x35000000)
  elif a==0x2526b50:finish(value=1)
  elif a==0x2676330:finish(value=1)
  elif a==0x997a10:finish(4,record)
  elif a==0x8a9480:assert r(sp+4)==0x7b4;finish(value=manager)
  elif a==0x9981f0:finish(value=manager)
  elif a==0x994d20 and data is None:
   log.append(dict(event='send1426',group=r(sp+4),skill=r(sp+8),alreadySelected=r(record+r(sp+4)*4)));finish(24)
  elif a==0x9980a0:log.append(dict(event='refresh_stats',group=r(sp+8)));finish(8)
  elif a==m.GLOBAL_RESET:raise AssertionError('unexpected global reset')
  elif a==0xab5ec0:
   put(r(sp+4),struct.pack('<III',proxy,0,manager+0x3000));finish(8,r(sp+4))
  elif a==0xa8f6c0:
   put(r(sp+4),struct.pack('<III',proxy,0,sentinel));finish(4,r(sp+4))
  elif a==0x9973e0:
   put(record,bytes(16));log.append(dict(event='erase_local_registration'));finish(4)
  elif a==0x32b5c90:log.append(dict(event='lookup_ui',window=r(sp+4)));finish(8,0x35000000)
  elif a==0x3e73cb6:finish(value=r(sp+4))
  elif a==0xc04720:log.append(dict(event='reset_ui_callback'));finish(4)
  elif a==0x37e8350:
   if mode=='snapshot':put(r(sp+4),struct.pack('<H',selected))
   else:put(r(sp+4),packet[pos:pos+2]);pos+=2
   finish()
  elif a==0x37e82e0:
   count=r(sp+8);put(r(sp+4),packet[pos:pos+count]);pos+=count;finish()
  elif a==0x2974420:finish(value=0)
  elif a==0x34000100:finish(8)
  elif a==0x34000200:finish(4)
  elif a==0x9983f0:
   g,slot=r(sp+4),r(sp+12)
   if r(sp+24):trees[g][slot]=r(sp+20)
   else:trees[g].pop(slot,None)
   log.append(dict(event='apply_row',group=g,slot=slot));finish(28)
  elif a==0x374ea10:outbound.append(r(sp+4));finish(4,0x35000000)
  elif a in [0x374f280,0x374f390,0x374f3c0]:finish()
  elif a==0x3750ac0:finish()
  elif a==0x2531a80:assert r(sp+4)==0;finish(value=0x36000000)
  elif a==0x25eb680:assert r(sp+4)==54+group;finish(4,0x35001000)
  elif a==0x34000300:finish(value=0x37000000)
  elif a==0x3296510:finish()
 leaves=[0x1fa2790,0x2526b50,0x2676330,0x997a10,0x9980a0,m.GLOBAL_RESET,0xab5ec0,0xa8f6c0,0x9973e0,0x32b5c90,0x3e73cb6,0xc04720,0x37e8350,0x37e82e0,0x2974420,0x34000100,0x34000200,0x9983f0,0x3296510,0x374ea10,0x374f280,0x374f390,0x374f3c0,0x3750ac0,0x2531a80,0x25eb680,0x34000300]
 if data is None:leaves.append(0x994d20)
 if mode=='snapshot':leaves.append(0x9985f0)
 leaves.extend([0x8a9480,0x9981f0])
 for va in leaves:put(va,b'\xcc')
 if mode=='snapshot':
  original=hook
  def hook(uc,a,n,_):
   if a==0x9985f0:log.append(dict(event='snapshot_rows'));finish();return
   original(uc,a,n,_)
 u.hook_add(UC_HOOK_CODE,hook)
 args=[stop,0x11111111,0x22222222]
 entry=0x998890 if mode=='snapshot' else 0x9985f0
 if mode=='select':entry=0x998530;args=[stop,actor,group,selected,1];u.reg_write(UC_X86_REG_ECX,manager)
 put(stack,struct.pack('<'+'I'*len(args),*args));u.reg_write(UC_X86_REG_ESP,stack)
 u.emu_start(entry,0,count=50000);assert u.reg_read(UC_X86_REG_EIP)==stop
 result=[r(record+j*4) for j in range(4)]
 if mode=='snapshot':assert result==[selected,0,0,0] and sum(x['event']=='reset_ui_callback' for x in log)==2
 elif mode=='rows':
  expected=list(skills)
  if tagged:expected[group]=selected
  assert result==expected and sum(x['event']=='apply_row' for x in log)==rows and pos==len(packet),(result,expected,pos,len(packet))
  assert all(x['group']==group for x in log if x['event']=='apply_row')
  assert trees[1-group]==treesBefore[1-group]
  if bound:assert trees[group].get(12)==54+group,trees
 elif mode=='select':
  expected=0 if selected==skills[group] else selected
  assert result[group]==expected,(selected,result,log)
  if data is None:assert any(x['event']=='send1426' and x['alreadySelected']==expected for x in log)
  else:assert outbound==[1426,0xb7,group,expected,33,45,0xffffffff],outbound
 if fresh:assert r(0x5656c08)==manager
 return dict(mode=mode,before=skills,after=result,events=log,outbound=outbound,bound=bound,group=group,freshManager=fresh)
if __name__=='__main__':
 paired=cliopts.candidate_dir is not None
 if paired:
  data=(cliopts.candidate_image or cliopts.candidate_dir/'DNF.exe').read_bytes();cases=[run(mode,[38,39,40,41],selected,rows,data=data,group=group,tagged=mode=='rows') for mode in ['rows','select'] for group in [0,1] for selected in [0,14,38,39] for rows in ([1,33] if mode=='rows' else [33])]
  # Repeated restoration of an identical selected skill must never toggle it.
  cases.extend(run('rows',[38,39,0,0],skill,33,data=data,group=g,tagged=True) for g,skill in [(0,38),(1,39)])
  cases.extend(run('rows',[38,39,0,0],skill,33,data=data,group=g,tagged=True,bound=True) for g,skill in [(0,38),(1,39),(0,40),(1,41)])
  cases.extend(run('rows',[38,39,0,0],38,rows,data=data,group=0,tagged=False) for rows in [1,33])
  cases.extend(run('rows',[0,0,0,0],skill,33,data=data,group=g,tagged=True,fresh=True,bound=True) for g,skill in [(0,38),(1,39)])
  name='native-group-verification.json'
 else:
  cases=[run(mode,[38,39,40,41],selected,rows) for mode in ['snapshot','rows','select'] for selected in [0,14,38,39] for rows in ([1,33] if mode=='rows' else [33])];name='native-selection-verification.json'
 result=dict(passed=True,exeSHA256=hashlib.sha256(data).hexdigest() if paired else digest,cases=cases,limitations=['Executes pinned x86 selection/snapshot/row parsing; STL storage, inventory lookups, packet I/O, UI callbacks and stat refresh are explicit leaves.','The visual close/render and gameplay effects require user testing.'])
 (B/name).write_text(json.dumps(result,indent=2),'utf8');print(json.dumps(dict(passed=True,cases=len(cases),exeSHA256=result['exeSHA256'],limitations=result['limitations'])))
