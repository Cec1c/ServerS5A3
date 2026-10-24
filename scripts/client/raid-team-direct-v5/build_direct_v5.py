"""Exact-v156 offline builder; never installs or touches a running process."""
import argparse
import json
import struct
import sys
import types
from pathlib import Path

sys.dont_write_bytecode = True
SUPPORT = Path(__file__).resolve().parent.parent / 'raid-team-ready-v4'
sys.path.insert(0, str(SUPPORT))
import build_readiness_v4 as v4

INPUT_SHA = '3fdc7c17584731cae240ca5e54122a4180485aab05b7fff8114a14d7822bf2ad'
OUTPUT_SHA = 'cd3b8976a586fb5870a4a0ec854e1fce86c6c50af78d5b6a816660a4130571d0'
DIRECT_SITE = 0x192F60D
CODE_VA = v4.pe.IMAGE_BASE + v4.SIZE
DATA_VA = CODE_VA + 4096


def helpers():
    # Reuse every v4 identity/lifecycle rule. Fail closed if this exact source
    # anchor changes; no neighbouring public file is modified.
    path = SUPPORT / 'build_readiness_v4.py'
    source = path.read_text(encoding='utf8')
    before = "c.raw('83be2004000003');c.jne('pass')"
    after = ("c.raw('83be2004000002');c.je('valid_preteam_state')\n"
             "    c.raw('83be2004000003');c.jne('pass')\n"
             "    c.label('valid_preteam_state')")
    assert source.count(before) == 1, 'unexpected v4 helper source'
    module = types.ModuleType('direct_gate_helpers')
    module.__file__ = str(path)
    exec(compile(source.replace(before, after), str(path), 'exec'), module.__dict__)
    return module.helpers(CODE_VA, DATA_VA)


def baseline_metadata():
    code, entries = v4.helpers(CODE_VA, DATA_VA)
    return dict(code_va=CODE_VA, data_va=DATA_VA, code_size=len(code), entries=entries)


def build(image):
    available = v4.old.memory_guard()
    assert v4.pe.sha(image) == INPUT_SHA, 'fixed v156 SHA256 required'
    code, entries = helpers()
    assert len(code) < 4096
    opt, sections = v4.pe.headers(image)
    out = bytearray(image)
    off = v4.pe.offset(sections, CODE_VA, 4096)
    oldcode, oldentries = v4.helpers(CODE_VA, DATA_VA)
    assert image[off:off+len(oldcode)] == oldcode
    assert not any(image[off+len(oldcode):off+4096])
    out[off:off+4096] = code + bytes(4096-len(code))
    ranges = [(off, off+4096), (opt+64, opt+68)]
    for name, (va, raw) in v4.SITES.items():
        pos = v4.pe.offset(sections, va, len(raw))
        opcode = b'\xe8' if name == 'gate' else b'\xe9'
        assert image[pos:pos+len(raw)] == opcode + struct.pack('<i', oldentries[name]-va-5) + b'\x90'*(len(raw)-5)
        out[pos:pos+len(raw)] = opcode + struct.pack('<i', entries[name]-va-5) + b'\x90'*(len(raw)-5)
        ranges.append((pos, pos+len(raw)))
    pos = v4.pe.offset(sections, DIRECT_SITE, 5)
    assert image[pos:pos+5] == bytes.fromhex('e88efaffff')
    out[pos:pos+5] = b'\xe8' + struct.pack('<i', entries['gate']-DIRECT_SITE-5)
    ranges.append((pos, pos+5))
    peoff = struct.unpack_from('<I', image, 0x3c)[0]
    table = opt + struct.unpack_from('<H', image, peoff+20)[0]
    count = struct.unpack_from('<H', image, peoff+6)[0]
    for i in range(count):
        sh = table+i*40
        if image[sh:sh+8].rstrip(b'\0') == b'.a4rx':
            assert struct.unpack_from('<I', image, sh+8)[0] == len(oldcode)
            struct.pack_into('<I', out, sh+8, len(code))
            ranges.append((sh+8, sh+12))
            break
    else:
        raise AssertionError('missing .a4rx section')
    struct.pack_into('<I', out, opt+64, v4.pe.checksum(out, opt+64))
    last = 0
    for lo, hi in sorted(ranges):
        assert image[last:lo] == out[last:lo]
        last = hi
    assert image[last:] == out[last:]
    assert v4.pe.sha(out) == OUTPUT_SHA, 'output differs from reviewed candidate'
    return out, dict(input_sha256=INPUT_SHA, output_sha256=OUTPUT_SHA,
                     code_va=CODE_VA, data_va=DATA_VA, code_size=len(code),
                     entries=entries, changed_ranges=sorted(ranges),
                     direct_gate_site=DIRECT_SITE, available_before=available,
                     protection_floor_gib=3, deployed=False,
                     status='offline_candidate_not_deployed')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True, help='new output directory')
    args = parser.parse_args()
    assert not args.output.exists(), 'output directory must not exist'
    out, record = build(args.input.read_bytes())
    args.output.mkdir(parents=True)
    (args.output/'candidate.exe').write_bytes(out)
    (args.output/'build.json').write_text(json.dumps(record, indent=2)+'\n', encoding='utf8')
    print(json.dumps(dict(sha256=record['output_sha256'], output=str(args.output))))


if __name__ == '__main__':
    main()
