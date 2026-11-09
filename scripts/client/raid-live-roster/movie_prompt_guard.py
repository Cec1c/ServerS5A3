"""Keep an existing skip confirmation when a retried NOTI599 arrives.

Only the reviewed local cumulative client is accepted. No install or execution.
"""
from pathlib import Path
import argparse,json,struct,hashlib,sys
sys.path.insert(0,str(Path(__file__).resolve().parent.parent/'local-patches'))
from pe_image import Image,rel32,pe_checksum
INPUT_SHA='1bdff5b5da1bfd54a1005a555e89a905cbedee3d1884ffe7ca22a54dfad381c9'
SITE=0x20105D6
ORIGINAL=0x18FBE70
ENTRY=0x6F88D50
def helper():
 b=bytearray();fix=[]
 def emit(s):b.extend(bytes.fromhex(s))
 def branch(s,label):emit(s);fix.append((len(b),label));b.extend(bytes(4))
 emit('9c60 8b0d98376a05 85c9');branch('0f84','original')
 emit('6832030000');b.extend(b'\xe8'+rel32(ENTRY+len(b),5,0x32B55E0))
 emit('84c0');branch('0f84','original')
 # Original is void and the caller only performs shared_ptr cleanup. On
 # duplicate input preserve both its live registers and the open UI object.
 emit('619dc3');original=len(b);emit('619d')
 b.extend(b'\xe9'+rel32(ENTRY+len(b),5,ORIGINAL))
 for at,label in fix:struct.pack_into('<i',b,at,original-at-4)
 return bytes(b)
def build(raw):
 if len(raw)!=112607232 or hashlib.sha256(raw).hexdigest()!=INPUT_SHA:raise ValueError('exact reviewed roster client required')
 im=Image(raw);s=im.section('.gld127');body=helper()
 if s is None or s['flags']!=0x60000020 or im.rel32_target(SITE)!=ORIGINAL:raise ValueError('native NOTI599 call changed')
 off=im.offset(ENTRY,len(body));delta=ENTRY-0x400000-s['rva']
 if delta!=((s['vsize']+15)&~15) or delta+len(body)>s['rsize'] or raw[off:off+len(body)]!=bytes(len(body)):raise ValueError('RX owned padding changed')
 # Scope is the single network prompt call, not a global movie/window hook.
 if im.read(0x23FB11D,4)!=bytes.fromhex('84c07414') or im.rel32_target(0x23FB130)!=0x32B94A0:raise ValueError('native existing-confirmation branch changed')
 out=bytearray(raw);ranges=[]
 def put(at,b):out[at:at+len(b)]=b;ranges.append([at,at+len(b)])
 put(im.offset(SITE,5),b'\xe8'+rel32(SITE,5,ENTRY));put(off,body);put(s['header']+8,struct.pack('<I',delta+len(body)));put(im.checksum_at,struct.pack('<I',pe_checksum(out,im.checksum_at)))
 restored=bytearray(out)
 for a,z in ranges:restored[a:z]=raw[a:z]
 assert restored==raw
 return bytes(out),dict(inputSHA256=INPUT_SHA,outputSHA256=hashlib.sha256(out).hexdigest(),site=hex(SITE),helperVA=hex(ENTRY),helperBytes=len(body),changedRanges=ranges,allOtherBytesPreserved=True,originalMovieStateMachinePreserved=True,installed=False,gameAcceptance=False)
if __name__=='__main__':
 p=argparse.ArgumentParser(description=__doc__);p.add_argument('--input',type=Path,required=True);p.add_argument('--output-dir',type=Path,required=True);a=p.parse_args()
 assert not a.output_dir.exists();out,r=build(a.input.read_bytes());a.output_dir.mkdir(parents=True);(a.output_dir/'DNF.exe').write_bytes(out);(a.output_dir/'build.json').write_text(json.dumps(r,indent=2),'utf8');print(json.dumps(r))
