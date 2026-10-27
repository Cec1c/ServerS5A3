"""Allow the named guild item-link receiver on the cumulative social client."""
import argparse,json,struct
from pathlib import Path
import patch_receivers_v128 as pe
from patch_main_whisper_v179 import rel
INPUT_SHA256='504c4e80ddeb45001934a5122c9f9cbc15606bfec2570a630f7a371977f2755c'
INPUT_SIZE=111984640
CALL_VA,HELPER_VA,PREVIOUS_GATE=0x2523A13,0x6EADD00,0x6EADC00

def helper():
 code=bytearray.fromhex('803f00')
 code.extend(rel(b'\x0f\x85',HELPER_VA+3,PREVIOUS_GATE))
 code.extend(bytes.fromhex('0fb74424043d73010000'))
 code.extend(rel(b'\x0f\x85',HELPER_VA+19,PREVIOUS_GATE))
 code.extend(bytes.fromhex('b801000000c20400'))
 return bytes(code)

def build(data):
 if len(data)!=INPUT_SIZE or pe.sha(data)!=INPUT_SHA256:raise ValueError('exact cumulative social input required')
 opt,sections=pe.headers(data);out=bytearray(data);code=helper();at=pe.offset(sections,HELPER_VA,len(code));call=pe.offset(sections,CALL_VA,5)
 if data[call:call+5]!=rel(b'\xe8',CALL_VA,PREVIOUS_GATE) or any(data[at:at+len(code)]):raise ValueError('receive hook or reviewed padding changed')
 # D00 is inside the existing game RX section, between social and whisper helpers.
 table=opt+struct.unpack_from('<H',data,opt-4)[0]
 matched=False
 for i in range(len(sections)):
  h=table+40*i
  if data[h:h+8]!=b'.r195rx\0':continue
  size,rva,rawsize,raw=struct.unpack_from('<IIII',data,h+8)
  matched=(rva+pe.IMAGE_BASE==0x6EAD000 and size>0xE80 and rawsize==4096 and struct.unpack_from('<I',data,h+36)[0]==0x60000020)
 if not matched:raise ValueError('game receive section changed')
 out[at:at+len(code)]=code;out[call:call+5]=rel(b'\xe8',CALL_VA,HELPER_VA)
 struct.pack_into('<I',out,opt+64,pe.checksum(out,opt+64))
 return bytes(out),dict(inputSha256=INPUT_SHA256,outputSha256=pe.sha(out),size=len(out),changedRanges=sorted([(at,at+len(code)),(call,call+5),(opt+64,opt+68)]),class0Added=[371],fallbackVA=hex(PREVIOUS_GATE),noticeBytesPreserved=True)

def write_candidate(source,destination):
 source,destination=Path(source).resolve(),Path(destination).resolve()
 if source==destination or destination.exists():raise ValueError('separate new output required')
 out,receipt=build(source.read_bytes());destination.parent.mkdir(parents=True,exist_ok=True)
 with destination.open('xb') as stream:stream.write(out)
 if pe.sha(source.read_bytes())!=INPUT_SHA256 or pe.sha(destination.read_bytes())!=receipt['outputSha256']:raise RuntimeError('readback differs')
 return receipt

if __name__=='__main__':
 parser=argparse.ArgumentParser(description=__doc__);parser.add_argument('--input',required=True,type=Path);parser.add_argument('--output',required=True,type=Path);args=parser.parse_args();print(json.dumps(write_candidate(args.input,args.output),indent=2))
