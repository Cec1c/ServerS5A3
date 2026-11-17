"""Derive the marriage-request client from either reviewed formal baseline."""
import argparse
from array import array
import gzip
import hashlib
import json
from pathlib import Path
import struct
import sys
import time
import zipfile

BASE_SHA = '28e04d1d1c148c841318d533ed8ccb3e426ba5a5f24328b3c913b81303b12a84'
TAG_BUFF_BASE_SHA = '40657c19955dc2f594fc2221ef91044170d86406ba6fd193cdb1839877f28cb0'
LENGTH = 112816128
PATCH = 0x016cf5a9
VA = 0x01acf5a9
DESTINATION = 0x01acf5b7
STABLE = (0x01665320, 92)
PVF = '664829766ab016ad6935370281bb24820e40990a6db4d607607aa67b49d8da4b'
VARIANTS = {
    BASE_SHA: dict(sha256='f16d3bf48b6b802378fb066eaab96add8132c6406d7941a6d8d60328e47f6d8b',
                  pvfSha256=PVF, peChecksum=112871077, gzipLevel=1,
                  blobSha256='502887e0f23d0eb6b9e74160531b1859251c5edf2f1857db89de13fcc92666e2', blobBytes=40568037),
    TAG_BUFF_BASE_SHA: dict(sha256='58e51fba3258a9499e77caedaea16a6d9f38904054e10edd91564e24894547c4',
                  pvfSha256=PVF,
                  peChecksum=112825162, gzipLevel=9,
                  blobSha256='7feba90a33930c63f092e2a1a0f68dfcb8319d4c523462317ed80b8b9d53461d', blobBytes=37225730),
}
DEPENDENCIES = {
    'CombatPower.dll': ('140ff544f282e401801a5d46d067a74867b43a2ecce7d89b5abe065a00a59f77', 706048),
    'DpsMeter.dll': ('6e907abd58551c06e58242d31e4a8a62c57d7d2440d88b7184f9844c34be5a17', 493056),
    'ImagePacks2/sprite_interface_private_features20261113.NPK': ('fc8d96ccb192e3bdaed211b2bfb81a59ee96daab50e9d69e495766019c56ac42', 125034),
}


def digest(data): return hashlib.sha256(data).hexdigest()


def checksum_offset(data):
    pe = struct.unpack_from('<I', data, 0x3c)[0]
    if data[:2] != b'MZ' or data[pe:pe+4] != b'PE\0\0': raise ValueError('invalid PE')
    return pe + 24 + 64


def pe_checksum(data):
    raw = bytearray(data); offset = checksum_offset(raw)
    raw[offset:offset+4] = bytes(4)
    if len(raw) % 2: raw.append(0)
    words = array('H', raw)
    if sys.byteorder != 'little': words.byteswap()
    total = sum(words)
    while total >> 16: total = (total & 0xffff) + (total >> 16)
    return total + len(data)


def patch_client(original):
    variant = VARIANTS.get(digest(original))
    if len(original) != LENGTH or variant is None: raise ValueError('require exact formal 28e or 406 baseline')
    if original[PATCH:PATCH+14] != bytes.fromhex('75 0c 6a 00 68 67 1c 01 00 e9 a4 01 00 00'):
        raise ValueError('marriage branch and unique popup signature differ')
    result = bytearray(original); result[PATCH] = 0xeb
    struct.pack_into('<I', result, checksum_offset(result), pe_checksum(result))
    verify_difference(original, result)
    if digest(result) != variant['sha256'] or struct.unpack_from('<I',result,checksum_offset(result))[0] != variant['peChecksum']:
        raise ValueError('derived client differs from the reviewed variant')
    return bytes(result)


def verify_difference(original, candidate):
    if len(original) != LENGTH or len(candidate) != LENGTH: raise ValueError('client length differs')
    offset=checksum_offset(original)
    if candidate[PATCH:PATCH+2] != b'\xeb\x0c': raise ValueError('branch differs')
    # Compare complete contiguous regions, not samples or only section hashes.
    expected=bytearray(original);expected[PATCH]=0xeb
    expected[offset:offset+4]=candidate[offset:offset+4]
    if expected != candidate: raise ValueError('unapproved byte change')
    start,size=STABLE
    if original[start:start+size] != candidate[start:start+size]: raise ValueError('StableMemoryRegions changed')
    if struct.unpack_from('<I',candidate,offset)[0] != pe_checksum(candidate): raise ValueError('invalid PE checksum')


def branch_evidence(candidate):
    from capstone import Cs, CS_ARCH_X86, CS_MODE_32
    instruction=list(Cs(CS_ARCH_X86,CS_MODE_32).disasm(candidate[PATCH:PATCH+2],VA))[0]
    if instruction.mnemonic != 'jmp' or int(instruction.op_str,16) != DESTINATION: raise ValueError('wrong branch')
    try:
        from unicorn import Uc, UC_ARCH_X86, UC_MODE_32
        from unicorn.x86_const import UC_X86_REG_EFLAGS, UC_X86_REG_EIP
    except ImportError:
        Uc=None
    rows=[]
    for first,second in ((False,True),(True,False),(True,True),(False,False)):
        if Uc:
            vm=Uc(UC_ARCH_X86,UC_MODE_32);page=VA & ~0xfff;vm.mem_map(page,0x1000)
            vm.mem_write(VA,candidate[PATCH:PATCH+2]);vm.reg_write(UC_X86_REG_EFLAGS,0x2 | (0x40 if first==second else 0))
            vm.emu_start(VA,DESTINATION,count=1);target=vm.reg_read(UC_X86_REG_EIP)
        else:
            # EB cb is unconditional in 32-bit x86; ZF cannot affect it.
            target=VA+2+struct.unpack('b',candidate[PATCH+1:PATCH+2])[0]
        if target != DESTINATION: raise ValueError('gender pair reached the popup')
        rows.append(dict(female=[first,second],destination=target,popupReached=False))
    return dict(mode='capstone-unicorn' if Uc else 'capstone-exact-branch-semantics',cases=rows)


def build(base, output):
    started=time.perf_counter(); original=(base/'DNF.exe').read_bytes(); candidate=patch_client(original)
    base_sha=digest(original);variant=VARIANTS[base_sha]
    dependencies={name:(base/name).read_bytes() for name in DEPENDENCIES}
    for name,data in dependencies.items():
        if (digest(data),len(data)) != DEPENDENCIES[name]: raise ValueError('formal dependency differs: '+name)
    source=json.loads((base/'source.json').read_text('utf-8-sig'))
    if source.get('gamePvfSha256') != variant['pvfSha256']: raise ValueError('baseline PVF differs')
    if source.get('clientFiles',{}).get('DNF.exe') != dict(sha256=base_sha,bytes=LENGTH):
        raise ValueError('baseline source EXE identity differs')
    output.mkdir(parents=True,exist_ok=False)
    (output/'DNF.exe').write_bytes(candidate)
    with (output/'DNF.exe.gz').open('wb') as raw, gzip.GzipFile(fileobj=raw,mode='wb',filename='',mtime=0,compresslevel=variant['gzipLevel']) as packed:
        packed.write(candidate)
    if digest((output/'DNF.exe.gz').read_bytes()) != variant['blobSha256'] or (output/'DNF.exe.gz').stat().st_size != variant['blobBytes']:
        raise ValueError('derived gzip differs from the reviewed variant')
    h=hashlib.sha256();length=0
    with gzip.open(output/'DNF.exe.gz','rb') as stream:
        for block in iter(lambda:stream.read(1024*1024),b''):h.update(block);length+=len(block)
    if h.hexdigest()!=digest(candidate) or length!=LENGTH:raise ValueError('gzip full roundtrip failed')
    # Prior native verification belongs to the base, not this new candidate.
    source['parentSource']=json.loads((base/'source.json').read_text('utf-8-sig'))
    for key in ('nativeVerificationSHA256','sourceReviewSHA256','stableLeafSHA256','productionExe','sourceCommit'):
        source.pop(key,None)
    source.update(clientVersion='WEDDING-FEMALE-FORMAL-20261117' if base_sha==BASE_SHA else 'WEDDING-FEMALE-CUMULATIVE-406-20261117',baseExeSha256=base_sha,
                  sourceRecipe='resource-recipes/20261117-wedding-female-client/build.py',nativeAcceptance='pending',
                  deployed=False,playerChannelChanged=False)
    source['clientFiles']['DNF.exe']=dict(sha256=digest(candidate),bytes=LENGTH)
    source_raw=(json.dumps(source,ensure_ascii=False,indent=2)+'\n').encode('utf8')
    (output/'source.json').write_bytes(source_raw)
    archive=output/('client-wedding-female-20261117.zip' if base_sha==BASE_SHA else 'client-tag-buff-wedding-20261117.zip')
    with zipfile.ZipFile(archive,'w',compression=zipfile.ZIP_DEFLATED,compresslevel=1) as zipped:
        for name,data in sorted(dict(dependencies,**{'DNF.exe':candidate,'source.json':source_raw}).items()):
            info=zipfile.ZipInfo(name,(2026,11,17,0,0,0));info.compress_type=zipfile.ZIP_DEFLATED
            info.external_attr=0o100644<<16;zipped.writestr(info,data,compresslevel=1)
    with zipfile.ZipFile(archive) as zipped:
        for name,data in dict(dependencies,**{'DNF.exe':candidate,'source.json':source_raw}).items():
            if digest(zipped.read(name))!=digest(data):raise ValueError('archive roundtrip failed: '+name)
    receipt=dict(baseSha256=base_sha,pvfSha256=variant['pvfSha256'],sha256=digest(candidate),bytes=LENGTH,peChecksum=struct.unpack_from('<I',candidate,checksum_offset(candidate))[0],
                 patchOffset=PATCH,stableMemoryRegionsUnchanged=True,branch=branch_evidence(candidate),
                 blobSha256=digest((output/'DNF.exe.gz').read_bytes()),blobBytes=(output/'DNF.exe.gz').stat().st_size,
                 archive=str(archive.resolve()),archiveSha256=digest(archive.read_bytes()),archiveBytes=archive.stat().st_size,
                 gzipFullRoundtrip=True,archiveFullRoundtrip=True,elapsedSeconds=round(time.perf_counter()-started,6))
    (output/'client-receipt.json').write_text(json.dumps(receipt,indent=2)+'\n',encoding='utf8')
    return receipt


if __name__=='__main__':
    parser=argparse.ArgumentParser(description=__doc__);parser.add_argument('--base',required=True,type=Path);parser.add_argument('--output',required=True,type=Path)
    args=parser.parse_args();print(json.dumps(build(args.base,args.output),indent=2))
