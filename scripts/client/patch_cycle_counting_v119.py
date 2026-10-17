"""Enable the already registered current-client NOTI656 receiver only."""
from pathlib import Path
import argparse,hashlib,json,struct

INPUT_SHA256='009730b047c31675ba7db8f2b75a74b896ee9c47e4cd8c02f1d49c52aed98fc3'
OUTPUT_SHA256='c8871a888d4807c962adac72509fb15b9be204e5183beff610bf55cb57115bdf'
OPCODE=656
BITMAP_VA=0x6e92300

def sha(data):return hashlib.sha256(data).hexdigest()

def bitmap_offset(data):
 pe=struct.unpack_from('<I',data,60)[0];opt=pe+24
 if data[:2]!=b'MZ' or data[pe:pe+4]!=b'PE\0\0' or struct.unpack_from('<H',data,opt)[0]!=0x10b:
  raise ValueError('unsupported PE image')
 base=struct.unpack_from('<I',data,opt+28)[0];table=opt+struct.unpack_from('<H',data,pe+20)[0]
 rva=BITMAP_VA-base+OPCODE//8
 for i in range(struct.unpack_from('<H',data,pe+6)[0]):
  _,start,size,raw=struct.unpack_from('<IIII',data,table+40*i+8)
  if start<=rva<start+size:return raw+rva-start
 raise ValueError('notification bitmap is not file-backed')

def patch(data):
 if sha(data)!=INPUT_SHA256:raise ValueError('current v119 input SHA-256 mismatch')
 offset=bitmap_offset(data)
 if offset!=111747922 or data[offset]!=0x18:raise ValueError('unexpected notification bitmap byte')
 out=bytearray(data);out[offset]|=1<<(OPCODE%8)
 result=bytes(out)
 if sha(result)!=OUTPUT_SHA256:raise ValueError('candidate SHA-256 mismatch')
 return result,dict(opcode=OPCODE,file_offset=offset,before=data[offset],after=out[offset],input_sha256=INPUT_SHA256,output_sha256=OUTPUT_SHA256,changed_bytes=1)

def main():
 parser=argparse.ArgumentParser(description=__doc__);parser.add_argument('--input',required=True,type=Path);parser.add_argument('--output',required=True,type=Path)
 args=parser.parse_args();result,receipt=patch(args.input.read_bytes())
 args.output.parent.mkdir(parents=True,exist_ok=True)
 with args.output.open('xb') as f:f.write(result)
 print(json.dumps(receipt))

if __name__=='__main__':main()
