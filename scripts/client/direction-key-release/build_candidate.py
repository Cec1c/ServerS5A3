"""Build one isolated, reproducible direction-release candidate from fixed v157.

Pure stdlib. Never modifies input. No network, game startup or release operations.
The helper matches the supplied 2026-10-21 receipt at its original VA. Only its
two external relative jumps change at the new, dynamically allocated VA.
"""
import argparse
import array
import hashlib
import json
from pathlib import Path
import struct
import sys

sys.dont_write_bytecode = True
BASE_SHA = "cd3b8976a586fb5870a4a0ec854e1fce86c6c50af78d5b6a816660a4130571d0"
OLD_INPUT = "6a1a912cd6ca2e0585a94a7c20182544119b7e0a8deee64fc327994ad95ec96f"
OLD_OUTPUT = "bb53566d34fbd3cb0b4df12cacfdc6b375ba75f2492407fbced1b701bb2a0a7e"
HOOK, NORMAL, SKIP = 0x6E8C255, 0x6E8C25D, 0x6E8C264
TABLE, CACHE, ASYNC_IAT = 0x6E8C000, 0x65D1D70, 0x4588548
ORIGINAL = bytes.fromhex("808c310d01000080")
LOOP_SHA = "4b549352f824c2f3b191857264355618eb0d30889d72849f89ef374df164fb2f"
TABLE_SHA = "a20efc90758e8bc76a002b0bf1a591e823bd69b73714946c17a556aef5620398"
OLD_HELPER_VA = 0x6EA9000
OLD_HELPER = bytes.fromhex("9c6083f8250f825100000083f8280f87480000008bd850ff154885580466a900800f8535000000c683701d5d06008b4c241831d2389a00c0e8060f8508000000c684110d010000004281fa000100000f82dfffffff619de90832feff619d808c310d01000080e9f231feff")
OLD_SCRIPT_SHA = "37f2e4156119a6928336142d28b3866d208ce4d4134c100d6cea32e16e27ccde"
OLD_RECEIPT_SHA = "64422bec76c4c44aa5a39bba07e975c652a9636a2e078c0e19363ff31d3baf34"

def sha(data):
    return hashlib.sha256(data).hexdigest()

def u16(data, at):
    return struct.unpack_from("<H", data, at)[0]

def u32(data, at):
    return struct.unpack_from("<I", data, at)[0]

def align(value, alignment):
    assert alignment and alignment & (alignment - 1) == 0
    return (value + alignment - 1) & ~(alignment - 1)

class PE:
    def __init__(self, data):
        self.data = data
        assert data[:2] == b"MZ"
        self.nt = u32(data, 0x3C)
        assert data[self.nt:self.nt+4] == b"PE\0\0"
        assert u16(data, self.nt + 4) == 0x14C
        self.count = u16(data, self.nt + 6)
        self.opt = self.nt + 24
        assert u16(data, self.opt) == 0x10B
        self.base = u32(data, self.opt + 28)
        self.sa, self.fa = struct.unpack_from("<II", data, self.opt + 32)
        self.st = self.opt + u16(data, self.nt + 20)
        self.sections = []
        for i in range(self.count):
            at = self.st + i * 40
            vs, rva, rs, raw = struct.unpack_from("<IIII", data, at + 8)
            self.sections.append(dict(index=i, name=data[at:at+8].rstrip(b"\0").decode("ascii"),
                                      virtual_size=vs, rva=rva, raw_size=rs, raw=raw,
                                      characteristics=u32(data, at+36)))

    def offset(self, va, size=1):
        rva = va - self.base
        for s in self.sections:
            if s["rva"] <= rva and rva + size <= s["rva"] + s["raw_size"]:
                return s["raw"] + rva - s["rva"]
        raise ValueError(f"VA not raw backed: {va:x}/{size:x}")

    def read(self, va, size):
        at = self.offset(va, size)
        return self.data[at:at+size]

    def import_name(self, iat_va):
        rva, size = struct.unpack_from("<II", self.data, self.opt + 104)
        at = self.offset(self.base+rva)
        for pos in range(at, at + size, 20):
            original, stamp, forward, name_rva, first = struct.unpack_from("<IIIII", self.data, pos)
            if not any((original, stamp, forward, name_rva, first)):
                break
            for index in range(65536):
                lookup = self.offset(self.base+(original or first)+index*4)
                name = u32(self.data, lookup)
                if not name:
                    break
                if first+index*4 == iat_va-self.base and not name & 0x80000000:
                    p = self.offset(self.base+name)+2
                    text = self.data[p:self.data.index(0, p)].decode("ascii")
                    if text == "GetAsyncKeyState":
                        dll_at = self.offset(self.base+name_rva)
                        dll = self.data[dll_at:self.data.index(0, dll_at)].decode("ascii")
                        return dll, text
        raise AssertionError("GetAsyncKeyState IAT not confirmed")

class Code:
    def __init__(self, base):
        self.base, self.buf, self.labels, self.fixups = base, bytearray(), {}, []

    def emit(self, hexbytes):
        self.buf.extend(bytes.fromhex(hexbytes))

    def label(self, name):
        self.labels[name] = len(self.buf)

    def jump(self, opcode, target):
        self.emit(opcode)
        self.fixups.append((len(self.buf), target))
        self.buf.extend(bytes(4))

    def finish(self):
        for at, target in self.fixups:
            va = self.base + self.labels[target] if isinstance(target, str) else target
            struct.pack_into("<i", self.buf, at, va-(self.base+at+4))
        return bytes(self.buf)

def helper(va):
    c = Code(va)
    c.emit("9c60")                         # save flags and all general registers
    c.emit("83f825"); c.jump("0f82", "normal")
    c.emit("83f828"); c.jump("0f87", "normal")
    c.emit("8bd850ff1548855804")           # ebx=VK; push VK; call original IAT wrapper
    c.emit("66a90080"); c.jump("0f85", "normal")  # ONLY high bit means held
    c.emit("c683701d5d0600")               # messageCache[VK]=0
    c.emit("8b4c241831d2")                 # restore ECX from PUSHAD; alias=0
    c.label("alias")
    c.emit("389a00c0e806")                 # cmp byte [TABLE+edx],bl
    c.jump("0f85", "next_alias")
    c.emit("c684110d01000000")             # current[alias]=0; previous untouched
    c.label("next_alias")
    c.emit("4281fa00010000")
    c.jump("0f82", "alias")
    c.emit("619d"); c.jump("e9", SKIP)    # no merge count for rejected direction
    c.label("normal")
    c.emit("619d"); c.buf.extend(ORIGINAL)
    c.jump("e9", NORMAL)
    return c.finish()

def checksum(data, checksum_offset):
    work = bytearray(data)
    struct.pack_into("<I", work, checksum_offset, 0)
    if len(work) & 1:
        work.append(0)
    words = array.array("H", work)
    if sys.byteorder != "little":
        words.byteswap()
    total = sum(words)
    while total >> 16:
        total = (total & 0xFFFF) + (total >> 16)
    return (total + len(data)) & 0xFFFFFFFF

def build(data):
    assert sha(data) == BASE_SHA, "input must be exact v157; refuse changed/previously patched EXE"
    p = PE(data)
    assert p.base == 0x400000 and p.count == 40
    assert not u16(data, p.opt+70) & 0x40, "ASLR needs relocations; refuse"
    assert struct.unpack_from("<II", data, p.opt+128) == (0, 0), "signed input needs separate design"
    assert p.read(HOOK, 8) == ORIGINAL
    assert sha(p.read(0x6E8C200, 0x7D)) == LOOP_SHA
    assert sha(p.read(TABLE, 256)) == TABLE_SHA
    dll, imported = p.import_name(0x6E866A0)
    assert dll.lower() == "user32.dll"
    # v157 startup resolves the real API, then preserves the pre-existing mouse
    # trace wrapper in the legacy IAT slot. Directions pass through unchanged.
    assert p.read(0x6E89025,20) == bytes.fromhex("a1a066e806a39033e806b8b033e806a348855804")
    assert sha(p.read(0x6E833B0,0x52)) == "8e77dd44319f844a28f20079c7b66ca6d9d790f083a122ce15f2597ce9ce4342"
    assert helper(OLD_HELPER_VA) == OLD_HELPER, "historical helper byte identity"
    assert all(s["name"] != ".imeup" for s in p.sections)
    header = p.st + p.count * 40
    assert header+40 <= min(s["raw"] for s in p.sections if s["raw_size"])
    assert header+40 <= u32(data, p.opt+60)
    assert data[header:header+40] == bytes(40), "do not overwrite existing header data"
    end = max(s["raw"]+s["raw_size"] for s in p.sections)
    assert end == len(data), "overlay requires separate handling"
    rva = align(max(s["rva"]+max(s["virtual_size"],s["raw_size"]) for s in p.sections), p.sa)
    assert rva >= u32(data, p.opt+56)
    raw = align(len(data), p.fa)
    code = helper(p.base+rva)
    raw_size = align(len(code), p.fa)
    result = bytearray(data)
    result.extend(bytes(raw+raw_size-len(result)))
    result[raw:raw+len(code)] = code
    struct.pack_into("<8sIIIIIIHHI", result, header, b".imeup\0\0", len(code), rva,
                     raw_size, raw, 0, 0, 0, 0, 0x60000020)
    struct.pack_into("<H", result, p.nt+6, p.count+1)
    struct.pack_into("<I", result, p.opt+4, u32(data,p.opt+4)+raw_size)
    struct.pack_into("<I", result, p.opt+56, align(rva+len(code),p.sa))
    hook = p.offset(HOOK,8)
    result[hook:hook+8] = b"\xe9"+struct.pack("<i",p.base+rva-HOOK-5)+b"\x90"*3
    struct.pack_into("<I",result,p.opt+64,checksum(result,p.opt+64))
    allowed = sorted([(p.nt+6,2,"section_count"),(p.opt+4,4,"SizeOfCode"),
                      (p.opt+56,4,"SizeOfImage"),(p.opt+64,4,"CheckSum"),
                      (header,40,"new_section_header"),(hook,8,"direction_hook")])
    cursor = 0
    for start,size,name in allowed:
        assert result[cursor:start] == data[cursor:start], f"unexpected existing-byte change before {name}"
        cursor = start+size
    assert result[cursor:len(data)] == data[cursor:]
    section_checks = []
    for s in p.sections:
        a,b = s["raw"],s["raw"]+s["raw_size"]
        normalized = bytearray(result[a:b])
        if a <= hook < b:
            normalized[hook-a:hook-a+8] = ORIGINAL
        assert normalized == data[a:b]
        section_checks.append({"index":s["index"],"name":s["name"],"sha256_before":sha(data[a:b]),
                               "sha256_after_normalizing_only_hook":sha(normalized),"preserved":True})
    report = {"input_sha256":BASE_SHA,"output_sha256":sha(result),"input_size":len(data),
              "output_size":len(result),"prior_fix_present":False,"helper_va":hex(p.base+rva),
              "helper_hex":code.hex(),"helper_size":len(code),"hook_before":ORIGINAL.hex(),
              "hook_after":bytes(result[hook:hook+8]).hex(),"new_section":{"rva":hex(rva),"raw":hex(raw),
              "raw_size":raw_size,"name":".imeup","characteristics":"0x60000020"},
              "imports_confirmed":[dll,imported],"legacy_iat_chain":{"iat":"0x4588548","wrapper":"0x6e833b0","resolved_api_slot":"0x6e83390","loader_iat":"0x6e866a0","startup_assignment":"0x6e89025"},
              "allowed_existing_changes":[{"offset":hex(a),"size":n,"name":k,
              "before_hex":bytes(data[a:a+n]).hex(),"after_hex":bytes(result[a:a+n]).hex(),
              "changed_byte_count":sum(x!=y for x,y in zip(data[a:a+n],result[a:a+n]))} for a,n,k in allowed],
              "all_40_existing_sections_preserved_except_hook":True,"sections":section_checks,
              "historical_source":{"builder_found":True,"receipt_found":True,"builder_sha256":OLD_SCRIPT_SHA,
              "receipt_sha256":OLD_RECEIPT_SHA,"supplied_old_input":OLD_INPUT,"supplied_old_output":OLD_OUTPUT,
              "helper_at_old_va_exact_match":True,"original_test_vectors_available":False,
              "provenance":"original helper reproduced byte-for-byte at old VA; v157 PE construction standalone; original native test vectors not included in supplied files"},
              "production_replaced":False,"published":False}
    return bytes(result),report

def main():
    ap=argparse.ArgumentParser()
    ap.add_argument("--input",type=Path,required=True)
    ap.add_argument("--output-dir",type=Path,required=True)
    args=ap.parse_args()
    dst=args.output_dir.resolve()
    dst.mkdir(parents=True,exist_ok=True)
    target=dst/"DNF-v157-direction-release-candidate.exe"
    assert target.resolve()!=args.input.resolve()
    source=args.input.read_bytes()
    candidate,report=build(source)
    if target.exists():
        assert sha(target.read_bytes())==report["output_sha256"], "refuse overwrite of different candidate"
    else:
        with target.open("xb") as f:f.write(candidate)
    assert sha(args.input.read_bytes())==BASE_SHA,"source changed concurrently; do not release"
    report["input_path"]=str(args.input.resolve());report["candidate_path"]=str(target)
    (dst/"build-receipt.json").write_text(json.dumps(report,indent=2)+"\n",encoding="utf8")
    print(json.dumps({k:report[k] for k in ("input_sha256","output_sha256","helper_va","helper_size","all_40_existing_sections_preserved_except_hook")}))

if __name__=="__main__":main()
