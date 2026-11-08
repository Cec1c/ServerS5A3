"""Rebuild both exact client fixes from the cumulative recovery executable."""
from pathlib import Path
import argparse,hashlib,importlib.util,json
ROOT=Path(__file__).resolve().parents[2]
INPUT='e142a7e6d530a0e369c722cb7fcc90c18f2d18884a343096d88336aed29f75e4'
OUTPUT='d333e4ce5f210a254932d7ba6e06e0649fc37f45ec248f033534e175e72e2d0b'

def module(name,path):
 spec=importlib.util.spec_from_file_location(name,path)
 value=importlib.util.module_from_spec(spec);spec.loader.exec_module(value);return value

def build(source,output):
 data=Path(source).read_bytes()
 if hashlib.sha256(data).hexdigest()!=INPUT:raise ValueError('exact cumulative recovery input required')
 offset=0x2eb6551
 if data[offset:offset+5]!=bytes.fromhex('a198376a05'):raise ValueError('solo Anton site mismatch')
 anton=data[:offset]+bytes.fromhex('e91f000000')+data[offset+5:]
 if hashlib.sha256(anton).hexdigest()!='fbb0b5d6c12387242745c15ea947e4cf2651aa2d80ce99d9e3256b18c3e8100d':raise ValueError('solo Anton intermediate mismatch')
 buff=module('reviewed_peer_buff',ROOT/'scripts/client/buff-peer-registration/build_candidate.py')
 candidate,receipt=buff.build(anton)
 if hashlib.sha256(candidate).hexdigest()!=OUTPUT or len(candidate)!=len(data):raise ValueError('combined cumulative output mismatch')
 with Path(output).open('xb') as stream:stream.write(candidate)
 return dict(inputSHA256=INPUT,outputSHA256=OUTPUT,bytes=len(candidate),soloAntonIntermediateSHA256=hashlib.sha256(anton).hexdigest(),buff=receipt)

if __name__=='__main__':
 parser=argparse.ArgumentParser(description=__doc__)
 parser.add_argument('--input',type=Path,required=True);parser.add_argument('--output',type=Path,required=True)
 args=parser.parse_args();print(json.dumps(build(args.input,args.output),indent=2))
