"""Build only the archived cumulative client; never edit an installed EXE."""
import hashlib,json,sys
from pathlib import Path
INPUT_SHA='e142a7e6d530a0e369c722cb7fcc90c18f2d18884a343096d88336aed29f75e4'
OFFSET=0x2eb6551
BEFORE=bytes.fromhex('a198376a05')
AFTER=bytes.fromhex('e91f000000')
def build(source,destination):
 data=Path(source).read_bytes()
 if hashlib.sha256(data).hexdigest()!=INPUT_SHA:raise ValueError('unsupported complete input SHA256')
 if data[OFFSET:OFFSET+5]!=BEFORE:raise ValueError('instruction mismatch')
 out=data[:OFFSET]+AFTER+data[OFFSET+5:]
 with Path(destination).open('xb') as f:f.write(out)
 return dict(inputSha256=INPUT_SHA,outputSha256=hashlib.sha256(out).hexdigest(),bytes=len(out),offset=OFFSET,before=BEFORE.hex(),after=AFTER.hex())
if __name__=='__main__':print(json.dumps(build(*sys.argv[1:])))
