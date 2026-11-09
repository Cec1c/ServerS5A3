"""Build an independent candidate for the already-open raid information table.

Accept only the exact reviewed cumulative client; never install or run it.
"""
from pathlib import Path
import argparse,hashlib,json,struct,sys
sys.path.insert(0,str(Path(__file__).resolve().parent.parent/'local-patches'))
from pe_image import Image,rel32,pe_checksum

INPUT_SHA='748406242876acbddfecbe7b74d528a1b78b775e984e725e0ac51df55b5bec82'
SIZE=112607232
SITE=0x2024B07
ORIGINAL=0x1A618E0
ENTRY=0x6F88CD0

def sha(data):return hashlib.sha256(data).hexdigest()

def helper():
 code=bytearray();labels={};fixes=[]
 def emit(s):code.extend(bytes.fromhex(s))
 def call(a):code.extend(b'\xe8'+rel32(ENTRY+len(code),5,a))
 def branch(op,label):
  emit(op);fixes.append((len(code),label));code.extend(bytes(4))
 # Preserve the original thiscall's one argument and all of its returned
 # registers/flags. EBP still belongs to NOTI591's validated mode3 frame;
 # [EBP-14h] is that notification's decoded raid key.
 emit('ff742404');call(ORIGINAL);emit('9c60')
 emit('8b0d98376a05 85c9');branch('0f84','done')
 emit('6a7a');call(0x32B55E0);emit('84c0');branch('0f84','done')
 # Copy ACK1359's existing-window getter and checked dynamic_cast. Do not
 # create a window or synthesize an info response for an outstanding request.
 emit('8b0d98376a05 6a00 686ce45605 6894365605 6a00 6a00 6a7a')
 call(0x32B5C90);emit('50');call(0x3E73CB6);emit('83c414 85c0');branch('0f84','done')
 emit('8bf0 8bc8');call(0x1C6B5A0);emit('3b45ec');branch('0f85','done')
 emit('8bce');call(0x1DF4D80)
 labels['done']=len(code);emit('619dc20400')
 for at,label in fixes:struct.pack_into('<i',code,at,labels[label]-at-4)
 return bytes(code)

def build(raw):
 if len(raw)!=SIZE or sha(raw)!=INPUT_SHA:raise ValueError('exact reviewed input required')
 image=Image(raw);section=image.section('.gld127');body=helper()
 if section is None or section['flags']!=0x60000020 or image.rel32_target(SITE)!=ORIGINAL:
  raise ValueError('reviewed section or mode3 refresh call changed')
 delta=ENTRY-0x400000-section['rva'];offset=image.offset(ENTRY,len(body))
 if delta!=((section['vsize']+15)&~15) or delta+len(body)>section['rsize'] or raw[offset:offset+len(body)]!=bytes(len(body)):
  raise ValueError('owned RX padding changed')
 out=bytearray(raw);ranges=[]
 def write(at,value):out[at:at+len(value)]=value;ranges.append([at,at+len(value)])
 write(image.offset(SITE,5),b'\xe8'+rel32(SITE,5,ENTRY));write(offset,body)
 write(section['header']+8,struct.pack('<I',delta+len(body)))
 write(image.checksum_at,struct.pack('<I',pe_checksum(out,image.checksum_at)))
 restored=bytearray(out)
 for a,b in ranges:restored[a:b]=raw[a:b]
 if restored!=raw:raise ValueError('unexpected byte changes')
 receipt=dict(inputSHA256=INPUT_SHA,outputSHA256=sha(out),bytes=len(out),site=hex(SITE),helperVA=hex(ENTRY),helperBytes=len(body),original=hex(ORIGINAL),changedRanges=ranges,allOtherBytesPreserved=True,installed=False,gameExecuted=False,realGameAcceptance=False)
 return bytes(out),receipt

def main():
 p=argparse.ArgumentParser(description=__doc__);p.add_argument('--input',type=Path,required=True);p.add_argument('--output-dir',type=Path,required=True);a=p.parse_args()
 if a.output_dir.exists():p.error('new independent output directory required')
 raw=a.input.read_bytes();out,receipt=build(raw)
 if sha(a.input.read_bytes())!=INPUT_SHA:raise ValueError('input changed during build')
 a.output_dir.mkdir(parents=True,exist_ok=False)
 with (a.output_dir/'DNF.exe').open('xb') as f:f.write(out)
 (a.output_dir/'build.json').write_text(json.dumps(receipt,indent=2)+'\n','utf8')
 print(json.dumps(receipt))
if __name__=='__main__':main()
