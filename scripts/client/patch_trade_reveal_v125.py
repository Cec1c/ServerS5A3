"""Reveal peer offers after both native trade locks; hide again on unlock.

Exact v125 input, separate output only. Keeps the existing packet receivers,
offer lists, lock notifications and explicit final-consent path unchanged.
"""
import argparse
import hashlib
import importlib.util
import json
from pathlib import Path
import struct

_spec=importlib.util.spec_from_file_location('guild_pe_helpers',Path(__file__).with_name('patch_guild_zero_region_v123.py'))
_pe=importlib.util.module_from_spec(_spec);_spec.loader.exec_module(_pe)
headers,align,offset_for_rva,checksum=_pe.headers,_pe.align,_pe.offset_for_rva,_pe.checksum
BASE=0x400000
INPUT_SHA256='c16e142ca99b2e6bdf48dae8d0b414fc5b7401cbacc62d601567476148a4f34b'
STORE_VA=0x260619c
REVEAL_VA=0x26061fc
OLD_STORE=bytes.fromhex('895cbe0885ff')
OLD_REVEAL=bytes.fromhex('c7465000000000')
SECTION=b'.trdrevl'

def sha(data):return hashlib.sha256(data).hexdigest()
def jump(source,dest):return b'\xe9'+struct.pack('<i',dest-source-5)

def build(data):
 if sha(data)!=INPUT_SHA256:raise ValueError('exact v125 input SHA-256 mismatch')
 pe,opt,table,sections=headers(data)
 if struct.unpack_from('<I',data,opt+28)[0]!=BASE or len(sections)!=20:raise ValueError('unexpected v125 image')
 if any(struct.unpack_from('<II',data,opt+96+4*8)):raise ValueError('certificate requires separate signing workflow')
 end=table+40*len(sections)
 if any(data[end:end+40]) or end+40>min(s['offset']for s in sections if s['rawsize']):raise ValueError('no zero section-header space')
 sa,fa=struct.unpack_from('<II',data,opt+32)
 va=align(max(s['rva']+max(s['size'],s['rawsize'])for s in sections),sa)+BASE
 raw=align(len(data),fa)
 if len(data)!=max(s['offset']+s['rawsize']for s in sections):raise ValueError('unexpected overlay')
 # Replay state store; only explicit unlock resets reveal. The original TEST
 # flags are recreated immediately before returning to the original branch.
 store=bytes.fromhex('895cbe0885db7504c646540085ff')
 store+=jump(va+len(store),STORE_VA+len(OLD_STORE))
 reveal_va=va+align(len(store),16)
 # This path is reached only after both native states are 1 AND the existing
 # partner lookup succeeds. Keep the original phase write and UI side effects.
 reveal=OLD_REVEAL+bytes.fromhex('c6465401')
 reveal+=jump(reveal_va+len(reveal),REVEAL_VA+len(OLD_REVEAL))
 payload=store+b'\x90'*(reveal_va-va-len(store))+reveal
 out=bytearray(data);ranges=[]
 for address,old,target in ((STORE_VA,OLD_STORE,va),(REVEAL_VA,OLD_REVEAL,reveal_va)):
  at=offset_for_rva(sections,address-BASE,len(old))
  if data[at:at+len(old)]!=old:raise ValueError('unexpected trade instructions')
  out[at:at+len(old)]=jump(address,target)+b'\x90'*(len(old)-5);ranges.append([at,at+len(old)])
 rawsize=align(len(payload),fa)
 section=struct.pack('<8sIIIIIIHHI',SECTION,len(payload),va-BASE,rawsize,raw,0,0,0,0,0x60000020)
 out[end:end+40]=section;ranges.append([end,end+40])
 struct.pack_into('<H',out,pe+6,len(sections)+1);ranges.append([pe+6,pe+8])
 struct.pack_into('<I',out,opt+4,struct.unpack_from('<I',data,opt+4)[0]+rawsize);ranges.append([opt+4,opt+8])
 struct.pack_into('<I',out,opt+56,align(va-BASE+len(payload),sa));ranges.append([opt+56,opt+60])
 out+=b'\0'*(raw-len(out))+payload+b'\0'*(rawsize-len(payload))
 struct.pack_into('<I',out,opt+64,checksum(out,opt+64));ranges.append([opt+64,opt+68])
 result=bytes(out)
 return result,dict(input_sha256=INPUT_SHA256,output_sha256=sha(result),allowed_original_changed_ranges=ranges,appended_range=[len(data),len(result)],store_trampoline=hex(va),reveal_trampoline=hex(reveal_va),enabled_opcodes=[],full_ui_acceptance=False)

def write_candidate(source,destination):
 source,destination=Path(source).resolve(),Path(destination).resolve()
 if source==destination or destination.exists():raise ValueError('output must be a separate new file')
 result,receipt=build(source.read_bytes());destination.parent.mkdir(parents=True,exist_ok=True)
 with destination.open('xb')as f:f.write(result)
 if sha(source.read_bytes())!=INPUT_SHA256 or sha(destination.read_bytes())!=receipt['output_sha256']:raise RuntimeError('input/output changed')
 return receipt

if __name__=='__main__':
 p=argparse.ArgumentParser(description=__doc__);p.add_argument('--input',type=Path,required=True);p.add_argument('--output',type=Path,required=True);a=p.parse_args();print(json.dumps(write_candidate(a.input,a.output),indent=2))
