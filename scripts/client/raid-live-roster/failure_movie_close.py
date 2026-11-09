"""Close a confirmed Anton failure movie using its native state transition.

Exact cumulative input only. Runs offline; no process or installation access.
"""
from pathlib import Path
import argparse,hashlib,json,struct,sys
sys.path.insert(0,str(Path(__file__).resolve().parent.parent/'local-patches'))
from pe_image import Image,rel32,pe_checksum
INPUT_SHA='d323e8cba1116b87a99716b92da10f36bc29999ad740fcada2fc8143e721ceda'
SITE=0x23FB368
ORIGINAL=0x18FBED0
ENTRY=0x6F88D80
def helper():
 b=bytearray();labels={};fix=[]
 def emit(s):b.extend(bytes.fromhex(s))
 def call(dest):b.extend(b'\xe8'+rel32(ENTRY+len(b),5,dest))
 def branch(s,label):emit(s);fix.append((len(b),label));b.extend(bytes(4))
 # Only the skip confirmation's callback is wrapped. Preserve the original
 # arg, caller registers and flags; first execute its normal 659 completion.
 emit('9c60 8b742418 807c242801');branch('0f85','original')
 emit('85f6');branch('0f84','original')
 # Native movie stores the selected content in its UTF16 SSO string. Accept
 # exactly Anton phase1/phase2 failure paths, never success or other movies.
 emit('83bef00300001b');branch('0f85','original')
 emit('8d96e0030000 83bef403000008');branch('0f82','inline')
 emit('8b12')
 labels['inline']=len(b)
 for off,text in [(0,'Vide'),(4,'o/An'),(8,'tonC'),(12,'utSc'),(16,'ene/')]:
  # Compare pairs of UTF16 code units, not bytes from an ANSI assumption.
  for n in (0,2):
   chunk=text[n:n+2].encode('utf-16le');emit('817a'+f'{(off+n)*2:02x}');b.extend(chunk);branch('0f85','original')
 emit('66837a2831');branch('0f84','phase_ok')
 emit('66837a2832');branch('0f85','original')
 labels['phase_ok']=len(b)
 emit('817a2a2d004600');branch('0f85','original')
 emit('817a2e2e006100');branch('0f85','original')
 emit('817a3276006900');branch('0f85','original')
 emit('66837a3600');branch('0f85','original')
 emit('80befc03000000');branch('0f84','original')
 # Require this exact movie to remain the active UI movie; a retired dialog
 # must not close a replacement movie or unrelated native window.
 emit('8b0d98376a05 85c9');branch('0f84','original')
 emit('3bb1dcc30000');branch('0f85','original')
 emit('6a01 8bce');call(ORIGINAL)
 emit('6a03 8bce');call(0x18FC6F0)
 branch('e9','done')
 labels['original']=len(b);emit('ff742428 8bce');call(ORIGINAL)
 labels['done']=len(b);emit('619dc20400')
 for at,label in fix:struct.pack_into('<i',b,at,labels[label]-at-4)
 return bytes(b)
def build(raw):
 if len(raw)!=112607232 or hashlib.sha256(raw).hexdigest()!=INPUT_SHA:raise ValueError('exact reviewed confirmation-guard input required')
 im=Image(raw);s=im.section('.gld127');body=helper()
 if im.rel32_target(SITE)!=ORIGINAL or s is None or s['flags']!=0x60000020:raise ValueError('native confirmation call changed')
 off=im.offset(ENTRY,len(body));delta=ENTRY-0x400000-s['rva']
 if delta!=((s['vsize']+15)&~15) or delta+len(body)>s['rsize'] or raw[off:off+len(body)]!=bytes(len(body)):raise ValueError('RX owned padding changed')
 if im.read(0x18FC769,6)!=bytes.fromhex('8b0d98376a05') or im.rel32_target(0x18FC78C)!=0x32B94A0:raise ValueError('native state3 movie close changed')
 out=bytearray(raw);ranges=[]
 def put(at,b):out[at:at+len(b)]=b;ranges.append([at,at+len(b)])
 put(im.offset(SITE,5),b'\xe8'+rel32(SITE,5,ENTRY));put(off,body);put(s['header']+8,struct.pack('<I',delta+len(body)));put(im.checksum_at,struct.pack('<I',pe_checksum(out,im.checksum_at)))
 restored=bytearray(out)
 for a,z in ranges:restored[a:z]=raw[a:z]
 assert restored==raw
 return bytes(out),dict(inputSHA256=INPUT_SHA,outputSHA256=hashlib.sha256(out).hexdigest(),site=hex(SITE),helperVA=hex(ENTRY),helperBytes=len(body),changedRanges=ranges,allOtherBytesPreserved=True,scope='confirmed current Anton1-F/2-F movie only; native completion then state3',installed=False,gameAcceptance=False)
if __name__=='__main__':
 p=argparse.ArgumentParser(description=__doc__);p.add_argument('--input',type=Path,required=True);p.add_argument('--output-dir',type=Path,required=True);a=p.parse_args();assert not a.output_dir.exists();out,r=build(a.input.read_bytes());a.output_dir.mkdir(parents=True);(a.output_dir/'DNF.exe').write_bytes(out);(a.output_dir/'build.json').write_text(json.dumps(r,indent=2),'utf8');print(json.dumps(r))
