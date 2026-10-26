"""Verify the v168-based candidate's bytes, guarded lookup and native draw cleanup."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import struct
import sys

sys.dont_write_bytecode = True
import build_candidate as pe
import verify_packaged as h
import guarded_cases
from native_render import verify_native_render

INPUT_SHA = "4474c31236f2f38de1c772f0ac7f1f267d5d52b9dbed33c4925c1df91353cc62"
PAYLOAD_SHA = "78c48cc5ed4b8227d40a0d078109d71f7a6cc3d0059184def76c932293855a7c"
SIZE, BASE, HELPER_SIZE = 111906816, 0x6EC6000, 993


def package(original, candidate, manifest):
    pe.require(len(original) == SIZE and pe.sha(original) == INPUT_SHA, "v168 input differs")
    pe.require(len(candidate) == SIZE + 4096, "candidate size differs")
    before, after = pe.PE(original), pe.PE(candidate)
    pe.require(before.count == 47 and after.count == 48, "section count differs")
    pe.require(after.sections[:-1] == before.sections, "original sections changed")
    pe.require(after.sections[-1] == dict(name=b".rport\0\0", vsize=HELPER_SIZE,
               rva=0x6AC6000, raw_size=4096, raw=SIZE, flags=0x60000020), "helper section differs")
    pe.require(pe.sha(after.read(BASE, HELPER_SIZE)) == PAYLOAD_SHA, "helper bytes differ")
    pe.require(candidate[SIZE+HELPER_SIZE:] == bytes(4096-HELPER_SIZE), "helper padding differs")
    branch = b"\x0f\x84" + struct.pack("<i", BASE - (h.SITE+6))
    pe.require(after.read(h.SITE, 6) == branch, "null-actor branch differs")
    allowed = [(0x136,2), (0x14C,4), (0x180,4), (0x188,4), (0x980,40), (h.SITE_OFFSET,6)]
    restored = bytearray(candidate[:SIZE])
    pe.require(len(manifest["allowed_original_byte_ranges"]) == len(allowed), "edit list differs")
    for (offset,length), edit in zip(allowed,manifest["allowed_original_byte_ranges"]):
        pe.require((edit["offset"],edit["end_exclusive"]) == (offset,offset+length), "edit range differs")
        pe.require(edit["before_hex"] == original[offset:offset+length].hex() and
                   edit["after_hex"] == candidate[offset:offset+length].hex(), "edit bytes differ")
        restored[offset:offset+length] = original[offset:offset+length]
    pe.require(pe.sha(restored) == INPUT_SHA, "unapproved byte changes")
    pe.require(pe.u32(candidate,after.opt+64) == pe.checksum(candidate,after.opt+64), "checksum differs")
    pe.require(manifest["input"]["sha256"] == INPUT_SHA and manifest["output"]["sha256"] == pe.sha(candidate), "manifest digest differs")
    pe.require(manifest["helper"]["sha256"] == PAYLOAD_SHA, "manifest helper digest differs")
    blobs = {address:after.read(address,size) for address,(size,_) in h.WINDOWS.items()}
    for address,(_,digest) in h.WINDOWS.items():
        pe.require(pe.sha(blobs[address]) == digest, "native lookup window changed")
    blobs[BASE] = after.read(BASE, HELPER_SIZE)
    blobs[h.SITE-2] = after.read(h.SITE-2, 8)
    return after,blobs


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--original",type=Path,required=True)
    parser.add_argument("--candidate",type=Path,required=True)
    parser.add_argument("--manifest",type=Path,required=True)
    parser.add_argument("--deps",type=Path)
    parser.add_argument("--report",type=Path,required=True)
    args=parser.parse_args()
    pe.require(not os.path.lexists(args.report), "report already exists")
    if args.deps:sys.path.insert(0,str(args.deps.resolve(strict=True)))
    import unicorn
    from unicorn import x86_const
    pe.require(unicorn.__version__ == "2.1.4", "reviewed Unicorn version is 2.1.4")
    h.UC,h.X86=unicorn,x86_const
    h.HELPER_BASE,h.HELPER_SIZE=BASE,HELPER_SIZE
    original=args.original.read_bytes();candidate=args.candidate.read_bytes()
    manifest_bytes=args.manifest.read_bytes();manifest=json.loads(manifest_bytes)
    report=dict(passed=False,candidateSha256=pe.sha(candidate),manifestSha256=pe.sha(manifest_bytes),
                gameAccepted=False,published=False,stubs="map find/end; see renderer report for resource/graphics fixtures")
    try:
        image,guarded_cases.blobs=package(original,candidate,manifest)
        cases=guarded_cases.run_cases()
        pe.require(len(cases)==72 and all(x["passed"] for x in cases),"guarded cases failed")
        report["guardedCases"]=cases
        report["nativeRenderer"]=verify_native_render(image.read)
        pe.require(report["nativeRenderer"]["passed"]==64,"native renderer cases missing")
        pe.require(args.candidate.read_bytes()==candidate and args.original.read_bytes()==original
                   and args.manifest.read_bytes()==manifest_bytes,"input changed during verification")
        report.update(passed=True,guardedPassed=72,renderPassed=64,failed=0,inputUnchanged=True)
    except Exception as error:
        report["error"]=f"{type(error).__name__}: {error}"
    with args.report.open("x",encoding="utf-8") as stream:
        json.dump(report,stream,indent=2);stream.write("\n")
    print(json.dumps({k:v for k,v in report.items() if k not in ("guardedCases","nativeRenderer")}))
    return 0 if report["passed"] else 1


if __name__=="__main__":
    raise SystemExit(main())
