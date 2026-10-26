"""Actual dispatcher delta. Requires existing unicorn dependency and exact assets."""
import argparse,hashlib,json,struct
from pathlib import Path
import patch_receivers_v128 as pe
from patch_aiken_receiver import build,INPUT_SHA256
from unicorn import Uc,UC_ARCH_X86,UC_MODE_32
from unicorn.x86_const import *

def machine(data):
 u=Uc(UC_ARCH_X86,UC_MODE_32);u.mem_map(0x400000,0x7000000)
 for va,size,raw in pe.headers(data)[1]:u.mem_write(va,data[raw:raw+size])
 u.mem_map(0x10000000,0x40000);u.mem_map(0x20000000,0x20000)
 return u

def admissions(data,cases):
 u=machine(data);out=[]
 for cls,opcode in cases:
  u.mem_write(0x20000000,struct.pack('<BH',cls,opcode));u.reg_write(UC_X86_REG_EDI,0x20000000);u.reg_write(UC_X86_REG_ECX,opcode);u.reg_write(UC_X86_REG_ESP,0x10010000)
  for reg in [UC_X86_REG_EBX,UC_X86_REG_ESI,UC_X86_REG_EBP,UC_X86_REG_EDX]:u.reg_write(reg,0x10004000)
  u.emu_start(0x2523a0c,0x2523a18,count=1000)
  assert u.reg_read(UC_X86_REG_EIP)==0x2523a18 and u.reg_read(UC_X86_REG_ESP)==0x10010000
  assert u.reg_read(UC_X86_REG_EDI)==0x20000000
  assert all(u.reg_read(reg)==0x10004000 for reg in [UC_X86_REG_EBX,UC_X86_REG_ESI,UC_X86_REG_EBP])
  out.append((bool(u.reg_read(UC_X86_REG_EAX)),u.reg_read(UC_X86_REG_ECX),u.reg_read(UC_X86_REG_EDX)))
 return out

def main():
 p=argparse.ArgumentParser(description=__doc__)
 for name in ['input','candidate','receipt']:p.add_argument('--'+name,type=Path,required=True)
 a=p.parse_args()
 if a.receipt.exists() or a.receipt.resolve() in {a.input.resolve(),a.candidate.resolve()}:p.error('new independent receipt required')
 before=a.input.read_bytes();after=a.candidate.read_bytes();assert hashlib.sha256(before).hexdigest()==INPUT_SHA256
 assert build(before)[0]==after
 cases=[(c,n) for c in [0,1,2] for n in range(65536)]+[(c,1342) for c in range(3,256)]
 old,new=admissions(before,cases),admissions(after,cases)
 delta=[dict(packet_class=c,opcode=n,before=old[i][0],after=new[i][0]) for i,(c,n) in enumerate(cases) if old[i][0]!=new[i][0]]
 assert delta==[dict(packet_class=1,opcode=1342,before=False,after=True)],delta
 assert all(old[i]==new[i] for i,k in enumerate(cases) if k!=(1,1342))
 rejected=[]
 for name,data in [('truncated',before[:-1]),('unknown',bytes([before[0]^1])+before[1:]),('already_candidate',after)]:
  try:build(data)
  except ValueError:rejected.append(name)
  else:raise AssertionError(name)
 out=dict(passed=True,input_sha256=INPUT_SHA256,output_sha256=hashlib.sha256(after).hexdigest(),dispatch_cases_per_image=len(cases),delta=delta,invalid_inputs_rejected=rejected,old_paths_registers_equal=True,full_ui_acceptance=False,release_status='candidate_only')
 a.receipt.write_text(json.dumps(out,indent=2),'utf-8');print(json.dumps(out))

if __name__=='__main__':main()
