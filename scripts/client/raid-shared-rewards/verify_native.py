"""Run the actual native traversal and manual/timer consumers with shared rows.

Synthetic roster, UI objects, map iterators and final transition sinks; no GUI.
"""
from pathlib import Path
import argparse, hashlib, json, sys
def main():
 ap=argparse.ArgumentParser(description=__doc__);ap.add_argument('--candidate-dir',type=Path,required=True);a=ap.parse_args()
 d=a.candidate_dir;meta=json.loads((d/'build.json').read_text());data=(d/'DNF.exe').read_bytes();assert hashlib.sha256(data).hexdigest()==meta['outputSHA256']
 sys.path.insert(0,str(Path(__file__).resolve().parents[1]/'party-reward-row'))
 import verify_core as v
 import verify_completion as c
 v.OLD_PE=v.NEW_PE=v.PE(data)
 class Shared(c.CompletionMachine):
  def __init__(self,mode=0,patched=True):
   super().__init__('member',1,False)
   self.put(meta['payloadVA'],v.NEW_PE.read(meta['payloadVA'],meta['payloadSize']))
   if not patched:self.put(meta['hook']['va'],bytes.fromhex(meta['hook']['before']))
   self.put(0x1a553e0,b'\xc3');self.put(0x120000,bytes(0x4000));self.w(0x56a3778,0x120000)
   self.w(0x121024,mode);self.w(0x1210a4,0x122000);self.w(0x1210a8,0x1220c8)
   self.w(0x122000,19);self.w(0x122064,20)
   self.w(c.OTHER_ROW+0x3e4,19)
  def hook(self,u,address,size,arg):
   if address==0x1a553e0:self.ret(0x121000)
   else:super().hook(u,address,size,arg)
 cases=[]
 for mode in (0,1,2):
  for secondary,secondary_pending in ((1,False),(1,True),(2,False),(2,True),(None,False)):
   for own in (False,True):
    for pending in (False,True):
     for patched in (False,True):
      m=Shared(mode,patched)
      m.w(c.OWN_ROW+0x3e4,19 if own else 0xffffffff)
      m.w(v.ROW+0x3e4,0xffffffff if pending else 20)
      if secondary is None:m.w(c.ROOT2+0x3cc,m.r(c.ROOT2+0x3c8))
      else:
       m.w(c.ROOT2+0x3bc,secondary)
       m.w(c.ROOT2+0x3cc,c.MAP+0x230);m.put(0x119000,bytes(0x2000))
       m.w(c.MAP+0x228,0x119000);m.w(c.MAP+0x22c,0x11a000)
       m.w(0x11a004,2);m.w(0x11a008,1);m.w(0x1193e4,0xffffffff if secondary_pending else 20)
       m.tracked_pages.extend((0x119000,0x11a000))
      before={x:bytes(m.u.mem_read(x,4096)) for x in m.tracked_pages}
      want=own and (patched or not (pending or secondary_pending))
      assert m.invoke(0x8ed470,c.MANAGER)==want,(mode,secondary,own,pending,secondary_pending,patched)
      m.invoke(0x190cc50,c.MANAGER);assert m.transitions==[5 if want else 3]
      m.transitions.clear();m.invoke(0x190c900,c.MANAGER);assert m.transitions==([4] if want else [])
      assert m.r(c.MANAGER+0x420)==1;m.w(c.MANAGER+0x420,0)
      for x,old in before.items():assert bytes(m.u.mem_read(x,4096))==old,hex(x)
      cases.append(dict(mode=mode,secondaryCategory=secondary,ownBound=own,peerPending=pending,secondaryPeerPending=secondary_pending,patched=patched,complete=want))
 for name in ('team-self-unbound','team-self-missing','gold-self-unbound','gold-self-missing','party-self-missing','ordinary','unknown-self','foreign-roster','other-category','empty-party','bad-vector'):
  m=Shared()
  if name=='team-self-unbound':m.w(c.OTHER_ROW+0x3e4,0xffffffff)
  if name=='team-self-missing':m.w(c.OTHER_ROW+0x3e4,77)
  if name.startswith('gold-self-'):m.w(c.ROOT2+0x3bc,1);m.w(c.OTHER_ROW+0x3e4,77 if name.endswith('missing') else 0xffffffff)
  if name=='party-self-missing':m.w(c.OWN_ROW+0x3e4,77)
  if name=='ordinary':m.w(0x56a3778,0);m.w(v.ROW+0x3e4,0xffffffff)
  if name=='unknown-self':m.w(v.SCENE+0x70,0);m.w(v.ROW+0x3e4,0xffffffff)
  if name=='foreign-roster':m.w(0x122000,33);m.w(v.ROW+0x3e4,0xffffffff)
  if name=='other-category':m.w(v.ROOT_UI+0x3bc,3);m.w(v.ROW+0x3e4,0xffffffff)
  if name=='empty-party':m.w(v.ROOT_UI+0x3cc,m.r(v.ROOT_UI+0x3c8))
  if name=='bad-vector':
   # Helper must return fallback without reading the malformed vector.
   m.w(v.ROOT_UI+0x3cc,m.r(v.ROOT_UI+0x3c8)+1)
   m.w(v.STACK,v.STOP);m.w(v.STACK+4,v.ROOT_UI)
   m.u.reg_write(v.UC_X86_REG_ESP,v.STACK);m.u.emu_start(meta['exports']['OwnRootComplete'],v.STOP,count=5000)
   assert m.u.reg_read(v.UC_X86_REG_EAX)==2;cases.append(dict(boundary=name,originalFallback=True));continue
  assert m.invoke(0x8ed470,c.MANAGER)==0,name
  cases.append(dict(boundary=name,complete=False))
 for own_index in (0,19):
  for disposition in ('bound','pending','missing'):
   m=Shared();m.w(c.ROOT2+0x3c8,c.MAP+0x300);m.w(c.ROOT2+0x3cc,c.MAP+0x3a0)
   for i in range(20):
    row=0x130000+i*0x1000;m.put(row,bytes(4096));m.w(c.MAP+0x300+i*8,row);m.w(c.MAP+0x304+i*8,row+0x800)
    m.w(row+0x804,2);m.w(row+0x808,1)
    m.w(row+0x3e4,(19 if disposition=='bound' else 0xffffffff if disposition=='pending' else 77) if i==own_index else 0xffffffff)
    m.tracked_pages.append(row)
   before={x:bytes(m.u.mem_read(x,4096)) for x in m.tracked_pages}
   assert m.invoke(0x8ed470,c.MANAGER)==(disposition=='bound')
   for x,old in before.items():assert bytes(m.u.mem_read(x,4096))==old
   cases.append(dict(teamRows=20,ownIndex=own_index,ownState=disposition,peerRowsRetained=True,complete=disposition=='bound'))
 result=dict(passed=True,count=len(cases),inputSHA256=meta['outputSHA256'],cases=cases,nativeTraversal=True,nativeManualAndTimer=True,rewardBindingsRefcountsUnchanged=True,fixtures=['native map iterators','raid getter and roster','UI objects','clock','transition sink'],limits=['packet decoder/render/GPU not executed','no live gameplay acceptance'],gameAccepted=False)
 (d/'native.json').write_text(json.dumps(result,indent=2)+'\n');print(json.dumps(dict(passed=True,count=len(cases))))
if __name__=='__main__':main()
