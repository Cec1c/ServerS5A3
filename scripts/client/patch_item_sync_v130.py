"""Admit only the current class1/868 item-space recovery acknowledgement."""
import argparse
import json
from pathlib import Path
import struct
import patch_class_gates_v129 as prior
pe = prior.pe
INPUT_SHA256 = '6e1dce9076bcd5988bf3454184436c47eec256fa6247f80dd32f5cce93625ccc'
INPUT_SIZE = 111812608
CLASS0 = ()
CLASS1 = (868,)
CALL_VA, FRAGMENT_VA, FRAGMENT_END = prior.CALL_VA, prior.FRAGMENT_VA, prior.FRAGMENT_END
NEW_VA, NEW_RAW, NEW_SIZE, NEW_RVA = prior.NEW_VA, prior.NEW_RAW, prior.NEW_SIZE, prior.NEW_RVA

def helper():
    # Reuse the audited adapter generator, retaining all v130 admissions.
    original = prior.CLASS1
    try:
        prior.CLASS1 = (*original, 868)
        return prior.helper()
    finally:
        prior.CLASS1 = original

def build(data):
    if len(data) != INPUT_SIZE or pe.sha(data) != INPUT_SHA256:
        raise ValueError('exact published v130 SHA-256 and length required')
    opt, sections = pe.headers(data)
    table = opt + struct.unpack_from('<H', data, opt - 4)[0]
    header = table + 23 * 40
    if len(sections) != 24 or sections[-1] != (NEW_VA, NEW_SIZE, NEW_RAW):
        raise ValueError('unexpected v130 section layout')
    if data[header:header+8] != b'.clsgate' or struct.unpack_from('<I',data,header+36)[0] != 0x60000020:
        raise ValueError('expected readonly executable class adapter')
    old, code = prior.helper(), helper()
    if data[NEW_RAW:NEW_RAW+NEW_SIZE] != old + bytes(NEW_SIZE-len(old)):
        raise ValueError('unexpected v130 adapter bytes')
    at = pe.offset(sections,CALL_VA,5)
    if data[at:at+5] != b'\xe8' + struct.pack('<i',NEW_VA-CALL_VA-5):
        raise ValueError('unexpected receive call')
    out = bytearray(data)
    out[NEW_RAW:NEW_RAW+NEW_SIZE] = code + bytes(NEW_SIZE-len(code))
    struct.pack_into('<I',out,header+8,len(code))
    struct.pack_into('<I',out,opt+64,pe.checksum(out,opt+64))
    result=bytes(out)
    return result,dict(input_sha256=INPUT_SHA256,output_sha256=pe.sha(result),
        input_size=len(data),output_size=len(result),class0=[],class1=[868],
        helper_va=hex(NEW_VA),helper_size=len(code),call_va=hex(CALL_VA),
        original_ranges=sorted([(opt+64,opt+68),(header+8,header+12),(NEW_RAW,NEW_RAW+len(code))]),
        shared_bitmap_and_bound_unchanged=True,full_ui_acceptance=False)

def write_candidate(source,destination):
    source,destination=Path(source).resolve(),Path(destination).resolve()
    if source==destination or destination.exists():
        raise ValueError('output must be a separate new file')
    result,receipt=build(source.read_bytes())
    destination.parent.mkdir(parents=True,exist_ok=True)
    with destination.open('xb') as stream:stream.write(result)
    if pe.sha(source.read_bytes())!=INPUT_SHA256 or pe.sha(destination.read_bytes())!=receipt['output_sha256']:
        raise RuntimeError('input/output changed')
    return receipt

if __name__=='__main__':
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--input',type=Path,required=True);p.add_argument('--output',type=Path,required=True)
    a=p.parse_args();print(json.dumps(write_candidate(a.input,a.output),indent=2))
