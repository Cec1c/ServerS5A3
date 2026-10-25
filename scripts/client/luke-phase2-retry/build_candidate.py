"""Exact v155 offline Luke phase1 retry candidate; no deployment or live access."""
import argparse, ctypes, hashlib, json, struct, sys
from pathlib import Path
import pe_support as pe
sys.dont_write_bytecode=True
SHA='0762198fdc0c56a35305325c52ef876a05cc75c8fa849bb6a816deebba352bff'
SIZE=111874048
MAP_MANIFEST_SHA='9ec2ea43b5cc475acc3f62d9ea4b426da0aa6f40fec1f61fa1bede6b48dac43b'
RESOURCE_SHA='981baeaa6697d1b97d3cfc61ca118fd3c2151f1bf03ad630e609b578ac3f5097'
PVF_SHA='94a2549598816198115340f84c0be2d31603ddeea58f70f27afe6f73b25e3df7'
MAPS=(5057,5058,5059,5060,5061,5062,5063,5064,5065,5066,5311,5313)
SITES={'show':(0x35A9170,bytes.fromhex('e8ab0f37fd')),
       'refresh':(0x359EE40,bytes.fromhex('558bec6aff')),
       'input':(0x359BAA0,bytes.fromhex('558bec5153')),
       'default_input':(0x359C042,bytes.fromhex('6a48e8c7291b00'))}
def memory_guard():
    class M(ctypes.Structure):
        _fields_=[('length',ctypes.c_ulong),('load',ctypes.c_ulong)]+[(k,ctypes.c_ulonglong) for k in ('total','available','pt','pa','vt','va','ex')]
    m=M();m.length=ctypes.sizeof(m)
    assert ctypes.windll.kernel32.GlobalMemoryStatusEx(ctypes.byref(m)) and m.available>=3*1024**3
    return m.available
class Code:
    def __init__(self,va):self.va,self.b,self.labels,self.fix=va,bytearray(),{},[]
    def raw(self,s):self.b.extend(bytes.fromhex(s))
    def imm(self,op,v):self.raw(op);self.b.extend(struct.pack('<I',v))
    def label(self,n):assert n not in self.labels;self.labels[n]=len(self.b)
    def branch(self,op,d):self.raw(op);self.fix.append((len(self.b),d));self.b.extend(bytes(4))
    def call(self,d):self.branch('e8',d)
    def jump(self,d):self.branch('e9',d)
    def je(self,d):self.branch('0f84',d)
    def jne(self,d):self.branch('0f85',d)
    def done(self):
        for off,d in self.fix:
            dest=self.va+self.labels[d] if isinstance(d,str) else d
            struct.pack_into('<i',self.b,off,dest-self.va-off-4)
        return bytes(self.b)
def resource():
    # Public semantic subset of a read-only extraction, not bundled PVF contents.
    data=Path(__file__).with_name('phase_maps.json').read_bytes()
    assert pe.sha(data)==MAP_MANIFEST_SHA
    receipt=json.loads(data)
    assert receipt['pvfSha256']==PVF_SHA and receipt['textSha256']==RESOURCE_SHA
    assert receipt['path']=='etc/raid/luke/luke.etc' and receipt['index']==905111
    maps={int(k):v for k,v in receipt['phaseMaps'].items()}
    assert sorted(maps[0])==[5054,5055,5056] and tuple(sorted(maps[1]))==MAPS
    assert not set(maps[0])&set(MAPS)
    return maps

def helpers(va):
    c=Code(va)
    # Caller saves volatile registers/flags; predicate preserves callee-saved registers.
    c.label('predicate');c.raw('535657 8bf9 85ff');c.je('no')
    c.raw('837f080a');c.jne('no') # shared result panel state10
    c.raw('8b0d98376a05 85c9');c.je('no')
    c.call(0x32B49C0);c.raw('3bc7');c.jne('no')
    c.raw('8b1d78376a05 85db');c.je('no')
    c.raw('837b04ff');c.je('no')
    c.raw('8b7308 85f6');c.je('no')
    c.raw('837e2401');c.jne('no')
    c.call(0x1FA2790);c.raw('85c0');c.je('no')
    c.raw('8bcb');c.call(0x1A55570);c.raw('84c0');c.je('no')
    c.call(0x1FA2790);c.raw('8bc8');c.call(0x2525F90)
    c.raw('85c0');c.je('no')
    c.raw('8b809c010000') # dungeon object's record ID (+140 +5C)
    for dungeon in MAPS:c.imm('3d',dungeon);c.je('yes')
    c.label('no');c.raw('33c0 5f5e5b c3')
    c.label('yes');c.raw('b801000000 5f5e5b c3')
    c.label('apply');c.raw('56 8bf1');c.call('predicate');c.raw('85c0');c.je('apply_done')
    c.raw('8b8e0c070000 85c9');c.je('apply_done')
    c.raw('8b01 6a00 ff5008')
    c.raw('8b8e0c070000 85c9');c.je('apply_done')
    c.raw('8b01 6a00 ff500c')
    c.label('apply_done');c.raw('5e c3')
    c.label('show');c.raw('9c60 8bce');c.call('apply');c.raw('619d');c.jump(0x91A120)
    # Run the untouched refresh first; afterwards enforce only the targeted retry state.
    c.label('refresh');c.raw('51');c.call('refresh_original');c.raw('9c60 8b4c2424')
    c.call('apply');c.raw('619d 8d642404 c3')
    c.label('refresh_original');c.raw(SITES['refresh'][1].hex());c.jump(0x359EE45)
    c.label('input');c.raw('9c60 837c242800');c.jne('input_pass')
    c.call('predicate');c.raw('85c0');c.je('input_pass')
    c.raw('619d c20800')
    c.label('input_pass');c.raw('619d');c.raw(SITES['input'][1].hex());c.jump(0x359BAA5)
    # EBX is the final native-selected action index, EDI is the panel.
    # Preserve index2/town, including special-mode fallback. No entry guard.
    c.label('default_input');c.raw('9c60 85db');c.jne('default_pass')
    c.raw('8bcf');c.call('predicate');c.raw('85c0');c.je('default_pass')
    c.raw('619d')
    # Cancel pending selection rather than leave +734=1 waiting for a reply
    # to a request that was intentionally not sent. Keep native UI cleanup.
    c.raw('c78730070000ff000000 c7873407000000000000');c.jump(0x359C0B4)
    c.label('default_pass');c.raw('619d 6a48');c.call(0x374EA10);c.jump(0x359C049)
    code=c.done();return code,{k:va+v for k,v in c.labels.items()}
def build(src):
    available=memory_guard();maps=resource()
    assert len(src)==SIZE and pe.sha(src)==SHA,'exact v155 required'
    opt,sec=pe.headers(src);p=struct.unpack_from('<I',src,0x3c)[0];count=struct.unpack_from('<H',src,p+6)[0]
    hdr=opt+struct.unpack_from('<H',src,p+20)[0]+count*40
    assert count==39 and hdr+40<=4096 and not any(src[hdr:hdr+40])
    assert struct.unpack_from('<II',src,opt+32)==(4096,4096)
    assert struct.unpack_from('<I',src,opt+56)[0]==SIZE
    assert not struct.unpack_from('<H',src,opt+70)[0]&0x40
    code,entries=helpers(pe.IMAGE_BASE+SIZE);assert len(code)<=4096
    out=bytearray(src)+code+bytes(4096-len(code))
    ranges=[(p+6,p+8),(opt+4,opt+8),(opt+56,opt+60),(opt+64,opt+68),(hdr,hdr+40)]
    for k,(va,original) in SITES.items():
        off=pe.offset(sec,va,len(original));assert src[off:off+len(original)]==original
        out[off:off+len(original)]=(b'\xe8' if k=='show' else b'\xe9')+struct.pack('<i',entries[k]-va-5)+b'\x90'*(len(original)-5)
        ranges.append((off,off+len(original)))
    struct.pack_into('<H',out,p+6,count+1)
    for field in (opt+4,opt+56):struct.pack_into('<I',out,field,struct.unpack_from('<I',src,field)[0]+4096)
    struct.pack_into('<8sIIIIIIHHI',out,hdr,b'.lkrtry',len(code),SIZE,4096,SIZE,0,0,0,0,0x60000020)
    struct.pack_into('<I',out,opt+64,pe.checksum(out,opt+64))
    last=0
    for lo,hi in sorted(ranges):assert src[last:lo]==out[last:lo];last=hi
    assert src[last:]==out[last:SIZE]
    return out,{'input_sha256':SHA,'output_sha256':pe.sha(out),'input_size':SIZE,'output_size':len(out),'code_size':len(code),'entries':entries,'sites':{k:hex(v[0]) for k,v in SITES.items()},'allowed_ranges':sorted(ranges),'resource_sha256':RESOURCE_SHA,'pvf_sha256':PVF_SHA,'resource_receipt_sha256':pe.sha(Path(__file__).with_name('phase_maps.json').read_bytes()),'phase_maps':maps,'available_bytes':available,'status':'offline_candidate_not_deployed','scope':'Current shared panel state10, valid native Luke raid mode1, exact current-PVF phase1 map IDs. Default submission rejects final index0 only; native town branch preserved. No raid phase/state/asset modification.'}
if __name__=='__main__':
    ap=argparse.ArgumentParser();ap.add_argument('--input',type=Path,required=True);ap.add_argument('--output',type=Path,required=True);a=ap.parse_args()
    assert not a.output.exists(),'new output directory required'
    data,r=build(a.input.read_bytes());a.output.mkdir(parents=True)
    (a.output/'candidate.exe').write_bytes(data);(a.output/'build.json').write_text(json.dumps(r,indent=2)+'\n',encoding='utf-8')
    print(json.dumps(r))
