"""Reproduce the exact v170 default-command, inspection and title candidate."""
from pathlib import Path
import argparse
import hashlib
import importlib.util
import json
import struct
import sys

sys.dont_write_bytecode = True
import skill_default as skill
import profile_refresh as profile

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT / 'title-request'))

def module(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    value = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(value)
    return value

peutils = module('profile_pe_utils', ROOT / 'quiet-profile-refresh/build_candidate.py')
require = peutils.require
title = module('title_builder', ROOT / 'title-request/build_candidate.py')
SHA = lambda value: hashlib.sha256(value).hexdigest()

def build(data):
    if SHA(data) != skill.BASE_SHA256:
        raise ValueError('exact v170 input digest required')
    titled, title_receipt = title.build(data)
    pe = peutils.PE(titled)
    pe.require_contiguous()
    new_rva = peutils.u32(data, pe.opt + 56)
    address = pe.base + new_rva
    raw = peutils.align(len(data), pe.fa)
    header = pe.st + pe.count * 40
    if header + 40 > min(s['raw'] for s in pe.sections if s['raw_size']) or data[header:header+40] != bytes(40):
        raise ValueError('no verified empty section header')
    payload = bytearray(0x1000)
    helpers = [('skill', 0, skill.helper), ('detail', 0x100, profile.detail_helper),
               ('quiet', 0x300, profile.quiet_helper), ('lookup', 0x500, profile.lookup_helper)]
    records = {}
    for name, offset, emit in helpers:
        code = emit(address + offset)
        if len(code) > 0x100:
            raise ValueError('helper exceeded reserved slot')
        payload[offset:offset+len(code)] = code
        records[name] = dict(address=address+offset, size=len(code), sha256=SHA(code))
    edits = []
    def edit(va, expected, replacement):
        if pe.read(va,len(expected)) != expected or len(expected) != len(replacement):
            raise ValueError('unexpected hook bytes at '+hex(va))
        at = pe.offset(va,len(expected))
        edits.append((at,at+len(expected)))
        out[at:at+len(expected)] = replacement
    out = bytearray(titled)
    edit(skill.SITE, skill.ORIGINAL, skill.redirect(address))
    edit(profile.DETAIL_SITE,profile.DETAIL_OLD,profile.redirect(profile.DETAIL_SITE,address+0x100,6))
    edit(profile.QUIET_SITE,profile.QUIET_OLD,profile.redirect(profile.QUIET_SITE,address+0x300,5))
    for va in profile.PANEL_LOOKUP_SITES:
        edit(va,b'\xe8'+struct.pack('<i',0x296D730-va-5),b'\xe8'+struct.pack('<i',address+0x500-va-5))
    out.extend(bytes(raw-len(out)))
    out.extend(payload)
    out[header:header+40] = struct.pack('<8sIIIIIIHHI',b'.gamefix',0x1000,new_rva,0x1000,raw,0,0,0,0,0x60000020)
    struct.pack_into('<H',out,pe.nt+6,pe.count+1)
    struct.pack_into('<I',out,pe.opt+4,peutils.u32(out,pe.opt+4)+0x1000)
    struct.pack_into('<I',out,pe.opt+56,peutils.align(new_rva+0x1000,pe.sa))
    struct.pack_into('<I',out,pe.opt+64,peutils.pe_checksum(out,pe.opt+64))
    allowed = [(int(a,16),int(b,16)) for a,b in title_receipt['allowedChangedRanges']]
    allowed += edits + [(header,header+40),(pe.nt+6,pe.nt+8),(pe.opt+4,pe.opt+8),(pe.opt+56,pe.opt+60),(pe.opt+64,pe.opt+68)]
    cursor = 0
    for low, high in sorted(allowed):
        if low > cursor and data[cursor:low] != out[cursor:low]:
            raise ValueError('unreviewed original image change')
        cursor = max(cursor,high)
    if data[cursor:] != out[cursor:len(data)]:
        raise ValueError('unreviewed trailing original image change')
    result = bytes(out)
    final = peutils.PE(result)
    final.require_contiguous()
    for record in records.values():
        if SHA(final.read(record['address'],record['size'])) != record['sha256']:
            raise ValueError('helper readback failed')
    return result, dict(inputSha256=SHA(data),outputSha256=SHA(result),outputSize=len(result),
                        helpers=records,title=title_receipt,allowedChangedRanges=sorted(set(allowed)),
                        existingBytesPreservedOutsideWhitelist=True,guiAccepted=False)

def main():
    p=argparse.ArgumentParser();p.add_argument('--baseline',type=Path,required=True);p.add_argument('--output',type=Path,required=True)
    a=p.parse_args()
    if a.output.exists():raise ValueError('output directory already exists')
    data,receipt=build(a.baseline.read_bytes())
    a.output.mkdir()
    path=a.output/'DNF-gameplay-refresh.exe'
    path.write_bytes(data)
    if SHA(path.read_bytes())!=receipt['outputSha256']:raise ValueError('disk readback failed')
    (a.output/'build-receipt.json').write_text(json.dumps(receipt,indent=2),encoding='utf8')
    print(json.dumps(dict(sha256=receipt['outputSha256'],size=len(data),output=str(path))))

if __name__=='__main__':main()
