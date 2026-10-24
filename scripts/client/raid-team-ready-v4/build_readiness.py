"""Exact-input, offline Anton readiness candidate. Never installs/deploys."""
import argparse
import ctypes
import hashlib
import json
import struct
import sys
from pathlib import Path

sys.dont_write_bytecode = True
import pe_support as pe

INPUT_SHA = '8ab4297d1e3ebe2dc9d71801ebae210cb3ec673dbcdf707b35a3d5c8faf6f790'
INPUT_SIZE = 111865856
SITES = {'reset': (0x2001639, bytes.fromhex('e8f24e2b01')),
         'mark': (0x202FCC8, bytes.fromhex('8b0d98376a05')),
         'gate': (0x192F6ED, bytes.fromhex('e8aef9ffff'))}
ANCHORS = [
    (0x192F660,218,'32e2c0093a2c82760f84e92c84163e6fd20eb526f083efc86eed82907c2401d5'),
    (0x20015F6,114,'85735dcb305abef489c637699a9cc201decb9d7d9eb9a2a01d24ffa830f9c372'),
    (0x202FCC8,114,'1c8485028568e62c3835dfa949249a4b096e53d5526de2f90154e0fd5e144364'),
    (0x192B0D0,192,'cfe33b308e76e9912c6e34ce1ccf03a07fad413961a6433a45aabcf537d866e3'),
    (0x1A553E0,32,'55a9f63aeb58ba1e6541695d35a7654daaad58464e0f8df0e2f73873acadb16b'),
    (0x32B6530,96,'af60edc167324351aa9a3b7575d033c48316cd46130e5942c0e0840792a603f4'),
    (0x32B8840,108,'4cbd9137ccf00f2fec8d8e22fa57a9e0a307774684aaabf49bc7be1e9e2fed4f'),
    (0x296DD80,170,'715d3bc8a0967113b9119e5ff8e55e7b71588a90d5e5dde7be5ca274c52f501f'),
    (0x29614E0,5,'1d1bb4451883c840c67088c6af2e4da435cd0e07289b6e29dc26aa5b8d10306f'),
    (0x1A64B80,32,'e9b68d0b2298a29a32f99b44594ae319f32bde8880c00f8a80f6b266ef5aa189'),
    (0x20014F2,37,'2d3338c10ea052b0521c10b85ad037a9b71f07148e5845f0b5501ba4751f0b37'),
    (0x192AF00,196,'8483c09c66c41899c68d48fd73eee906e738c8b1780b2cb5a6b5f50f159cbdc2'),
    (0x192AD70,256,'77960211d740ea3665a3957ac6bbb3b7689dbaf53c5ab6ed327ab334d88b984e'),
    (0x192EAE0,16,'0608b962fbe5258df95c9efec2119652887368d76008478bf832caec34af574b'),
    (0x202FC20,174,'da11876f68a1ba255807481b5ddcd3b062a3fdde687ebfbd2706b867b7774fd2'),
]


def memory_guard():
    class Memory(ctypes.Structure):
        _fields_ = [('length', ctypes.c_ulong), ('load', ctypes.c_ulong)] + [
            (name, ctypes.c_ulonglong) for name in
            ['total', 'available', 'page_total', 'page_available', 'virtual_total', 'virtual_available', 'extended']]
    m = Memory(); m.length = ctypes.sizeof(m)
    if not ctypes.windll.kernel32.GlobalMemoryStatusEx(ctypes.byref(m)):
        raise RuntimeError('memory query failed')
    if m.available < 3 * 1024**3:
        raise RuntimeError('available memory below 3 GiB')
    return m.available


class Code:
    def __init__(self, va):
        self.va, self.b, self.labels, self.fix = va, bytearray(), {}, []
    def raw(self, s): self.b.extend(bytes.fromhex(s))
    def imm(self, op, value): self.raw(op); self.b.extend(struct.pack('<I', value))
    def label(self, name): self.labels[name] = len(self.b)
    def branch(self, op, dest):
        self.raw(op); off = len(self.b); self.b.extend(bytes(4))
        self.fix.append((off, dest))
    def call(self, va): self.branch('e8', va)
    def jump(self, dest): self.branch('e9', dest)
    def je(self, dest): self.branch('0f84', dest)
    def jne(self, dest): self.branch('0f85', dest)
    def done(self):
        for off, dest in self.fix:
            target = self.va + self.labels[dest] if isinstance(dest, str) else dest
            struct.pack_into('<i', self.b, off, target - self.va - off - 4)
        return bytes(self.b)


# Dedicated data layout: outer, raid-manager, cached-raid, current-key, fresh-model.
# All values are weak identity tokens, never owning references or dereferenced
# after retirement. Current live managers are read first before token comparison.
def identity(c, fail):
    c.raw('8b0d98376a05 85c9'); c.je(fail)
    c.raw('3bb1ccc30000'); c.jne(fail)  # esi == current UI manager's outer
    c.raw('3b33'); c.jne(fail)
    c.raw('8b0d78376a05 85c9'); c.je(fail)
    c.raw('3b4b04'); c.jne(fail)
    c.raw('837904ff'); c.je(fail)
    c.raw('8b5104 3b530c'); c.jne(fail)
    c.raw('8b4108 85c0'); c.je(fail)
    c.raw('3b4308'); c.jne(fail)
    c.raw('83782400'); c.jne(fail)     # native raid mode0


def helpers(code_va, data_va):
    c = Code(code_va)
    c.label('reset')
    c.raw('9c60'); c.imm('bb', data_va)
    c.raw('c70300000000 c7431000000000')  # invalidate even if open/other mode
    c.raw('8b742418 85f6'); c.je('reset_done')  # saved ECX == UI manager
    c.raw('3b3598376a05'); c.jne('reset_done')
    c.raw('8bb6ccc30000 85f6'); c.je('reset_done')
    c.raw('8b0d78376a05 85c9'); c.je('reset_done')
    c.raw('837904ff'); c.je('reset_done')
    c.raw('8b4108 85c0'); c.je('reset_done')  # no added lazy getter
    c.raw('83782400'); c.jne('reset_done')
    c.raw('894b04 894308 8b5104 89530c 8933')
    c.label('reset_done'); c.raw('619d'); c.jump(0x32B6530)

    c.label('mark')
    c.raw('9c60'); c.imm('bb', data_va)
    c.raw('807dcf03'); c.jne('mark_done')
    c.raw('807dce00'); c.je('mark_done')
    c.raw('8b7db0 85ff'); c.je('mark_done')
    c.raw('837f0401'); c.jne('mark_done')
    c.raw('8b0d98376a05 85c9'); c.je('mark_done')
    c.raw('8bb1ccc30000 85f6'); c.je('mark_done')
    identity(c, 'mark_done')
    c.raw('897b10')                  # entire nonempty kind3 loop has completed
    c.label('mark_done'); c.raw('619d 8b0d98376a05'); c.jump(0x202FCCE)

    c.label('gate')
    c.raw('9c60'); c.imm('bb', data_va)
    c.raw('837c242804'); c.jne('pass')  # arg at entry esp+4, saves consume 36
    c.raw('8b742418 85f6'); c.je('pass')
    c.raw('83be2004000003'); c.jne('pass')
    c.raw('80be2804000000'); c.jne('pass')
    c.raw('8b7dec 85ff'); c.je('pass')  # exact model chosen by native tick
    c.raw('837f0401'); c.jne('pass')
    identity(c, 'pass')
    c.raw('8b0d58376a05 85c9'); c.je('pass')
    c.raw('83797000'); c.je('pass')    # don't invoke lazy local lookup if absent
    c.raw('6a00 8bcf'); c.call(0x192B0D0)
    c.raw('84c0'); c.jne('owned')
    c.raw('6a00 8bcf'); c.call(0x192B140)
    c.raw('84c0'); c.je('pass')
    c.label('owned')
    c.raw('3b7b10'); c.jne('wait')
    c.raw('6a00 6a02 8bcf'); c.call(0x192AD70)
    c.raw('85c0'); c.je('wait')
    c.label('pass'); c.raw('619d'); c.jump(0x192F0A0)
    c.label('wait'); c.raw('619d c20400')  # consume only original arg4
    result = c.done()
    return result, {name: code_va + c.labels[name] for name in SITES}


def build(data):
    available = memory_guard()
    if len(data) != INPUT_SIZE or pe.sha(data) != INPUT_SHA:
        raise ValueError('exact local-face 8ab4297d input required')
    opt, sections = pe.headers(data)
    for va,size,digest in ANCHORS:
        off=pe.offset(sections,va,size)
        if pe.sha(data[off:off+size]) != digest:
            raise ValueError(f'native anchor mismatch {va:08x}')
    p = struct.unpack_from('<I', data, 0x3c)[0]
    n = struct.unpack_from('<H', data, p+6)[0]
    table = opt + struct.unpack_from('<H', data, p+20)[0]
    header = table + 40*n
    if n != 37 or header+80 > 4096 or any(data[header:header+80]):
        raise ValueError('two empty section header slots required')
    if struct.unpack_from('<II', data, opt+32) != (4096,4096):
        raise ValueError('unexpected alignment')
    if struct.unpack_from('<I', data, opt+56)[0] != INPUT_SIZE:
        raise ValueError('unexpected image size')
    if struct.unpack_from('<H', data, opt+70)[0] & 0x40:
        raise ValueError('absolute helpers require non-ASLR fixed image')
    code_va = pe.IMAGE_BASE + INPUT_SIZE
    data_va = code_va + 4096
    code, entries = helpers(code_va, data_va)
    if len(code)>4096: raise ValueError('code section overflow')
    out = bytearray(data)
    out.extend(code + bytes(8192-len(code)))
    ranges = [(p+6,p+8),(opt+4,opt+12),(opt+56,opt+60),(opt+64,opt+68),(header,header+80)]
    for name,(va, original) in SITES.items():
        off=pe.offset(sections,va,len(original))
        if data[off:off+len(original)] != original: raise ValueError('site changed')
        opcode = b'\xe9' if name=='mark' else b'\xe8'
        out[off:off+len(original)] = opcode + struct.pack('<i',entries[name]-va-5) + b'\x90'*(len(original)-5)
        ranges.append((off,off+len(original)))
    struct.pack_into('<H',out,p+6,n+2)
    for field in (opt+4,opt+8): struct.pack_into('<I',out,field,struct.unpack_from('<I',data,field)[0]+4096)
    struct.pack_into('<I',out,opt+56,INPUT_SIZE+8192)
    for i,(name,sz,flags) in enumerate([(b'.rdyrx',len(code),0x60000020),(b'.rdyrw',20,0xC0000040)]):
        rva=INPUT_SIZE+i*4096
        struct.pack_into('<8sIIIIIIHHI',out,header+i*40,name,sz,rva,4096,rva,0,0,0,0,flags)
    struct.pack_into('<I',out,opt+64,pe.checksum(out,opt+64))
    cursor=0
    for lo,hi in sorted(ranges):
        assert data[cursor:lo]==out[cursor:lo]
        cursor=hi
    assert data[cursor:]==out[cursor:INPUT_SIZE]
    return out, {'input_sha256':INPUT_SHA,'output_sha256':pe.sha(out),
        'input_size':len(data),'output_size':len(out),'code_va':code_va,'data_va':data_va,
        'code_size':len(code),'entries':entries,'allowed_original_ranges':sorted(ranges),
        'native_anchors':[{'va':hex(va),'size':size,'sha256':sha} for va,size,sha in ANCHORS],
        'protectionFloorGiB':3,'available_bytes_before':available,
        'deployed':False,'gui_accepted':False,'requires_main_thread_review':True}


def main():
    ap=argparse.ArgumentParser(); ap.add_argument('--input',type=Path,required=True)
    ap.add_argument('--output-dir',type=Path,required=True); a=ap.parse_args()
    a.output_dir.mkdir(parents=True,exist_ok=True)
    output=a.output_dir/'DNF-r195-readiness.exe'; receipt=a.output_dir/'build.json'
    if output.exists() or receipt.exists(): raise SystemExit('new output paths required')
    image, result=build(a.input.read_bytes())
    output.write_bytes(image); receipt.write_text(json.dumps(result,indent=2)+'\n',encoding='utf-8')
    print(json.dumps(result))

if __name__=='__main__': main()
