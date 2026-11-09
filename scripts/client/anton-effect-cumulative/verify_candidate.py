"""Verify the on-disk cumulative candidate, then run 12 paired Unicorn cases."""
import argparse
import json
from pathlib import Path
import struct
import sys
import platform
import build_candidate as patch

STUBS = [
    {'address': '0x21000000', 'contract': 'owner virtual lookup +0x138: fixture source or null, fixture EFLAGS'},
    {'address': '0x02947D20', 'contract': 'valid modes only: effect creation stub, ret 20; EAX=result, ECX=0x12345678, EDX=0x23456789, EFLAGS=0x246'},
    {'address': '0x02930A20', 'contract': 'native_no_owner only: scalar lookup ret 4 with EAX=1; real missing-owner callee branch executes'},
    {'address': '0x0142ADB0,0x01424520,0x01FA2790,0x02047360', 'contract': 'external helpers return EAX=0; other registers/flags unchanged by fixture'},
    {'address': '0x02046D70', 'contract': 'external helper ret 4, EAX=0'},
    {'address': '0x00E33B30', 'contract': 'row accessor asserts owner+0xD8 then ret 4, EAX=fixture row'},
    {'address': '0x21000100', 'contract': 'synthetic caller return address; stops emulator'},
]


def require(condition, message):
    if not condition:
        raise ValueError(message)


def structure(original, candidate):
    require(len(original) == 112607232 and patch.pe.sha(original) == patch.INPUT_SHA256, 'exact v4 required')
    require(len(candidate) == len(original), 'file length changed')
    # Independent review constants, not the builder's emitted bytes or range list.
    regions = [(216, 220), (3944, 3948), (12674225, 12674232), (112535696, 112535728)]
    prior = 0
    for lo, hi in regions:
        require(original[prior:lo] == candidate[prior:lo], 'outside approved change ranges')
        prior = hi
    require(original[prior:] == candidate[prior:], 'outside approved change ranges')
    opt, sections = patch.pe.headers(candidate)
    require(struct.unpack_from('<I', candidate, 3944)[0] == 0x8B0, 'VirtualSize incorrect')
    require(candidate[12674225] == 0xE9 and candidate[12674230:12674232] == b'\x90\x90', 'hook opcode')
    require(0x10164B1 + 5 + struct.unpack_from('<i', candidate, 12674226)[0] == 0x6F88890, 'hook target')
    code = candidate[112535696:112535728]
    # Preserve every non-relocated opcode and the local branch from PR180.
    masked = bytearray(code)
    for at in (13, 18, 28):
        masked[at:at + 4] = bytes(4)
    require(masked.hex() == '9c85c00f840d0000009d8bc8e800000000e9000000009d8d642414e900000000', 'helper instruction contract')
    for at, target in ((13, 0x2947D20), (18, 0x10164B8), (28, 0x10164B8)):
        require(0x6F88890 + at + 4 + struct.unpack_from('<i', code, at)[0] == target, 'rel32 target')
    require(0x6F88890 + 9 + struct.unpack_from('<i', code, 5)[0] == 0x6F88890 + 22, 'null branch target')
    # Independent, chunk-folded PE checksum calculation.
    value = 0
    for at in range(0, len(candidate), 65536):
        words = struct.unpack('<' + str(len(candidate[at:at + 65536]) // 2) + 'H', candidate[at:at + 65536])
        value += sum(words)
        if at == 0:
            value -= words[108] + words[109]
        value = (value & 0xFFFF) + (value >> 16)
    while value >> 16:
        value = (value & 0xFFFF) + (value >> 16)
    require(struct.unpack_from('<I', candidate, opt + 64)[0] == value + len(candidate), 'checksum incorrect')
    preserved = []
    for va, size in ((0x1FF2F34, 5), (0x6F88600, 649), (0x6EB0100, 36)):
        at = patch.pe.offset(sections, va, size)
        require(original[at:at + size] == candidate[at:at + size], 'model bytes changed')
        preserved.append(dict(va=hex(va), size=size, sha256=patch.pe.sha(candidate[at:at + size])))
    return dict(allowedRanges=regions, allOtherBytesIdentical=True, modelPreserved=preserved,
                branchesDecoded=True, checksumIndependentlyVerified=True)


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--input', type=Path, required=True)
    p.add_argument('--candidate', type=Path, required=True)
    p.add_argument('--manifest', type=Path, required=True)
    p.add_argument('--report', type=Path, required=True)
    p.add_argument('--unicorn-path', type=Path, help='existing local package directory, no installation')
    a = p.parse_args()
    if sys.flags.optimize:
        raise ValueError('assertions must be enabled for inherited native harness')
    if a.report.exists() or a.report.resolve() in [x.resolve() for x in (a.input, a.candidate, a.manifest)]:
        raise ValueError('new separate report required')
    if a.unicorn_path:
        sys.path.insert(0, str(a.unicorn_path.resolve()))
    import unicorn
    from native_window import Native
    from unicorn.x86_const import UC_X86_REG_EAX, UC_X86_REG_EFLAGS, UC_X86_REG_ESP

    class TracedNative(Native):
        def __init__(self, *args):
            self.helper_hits = 0
            super().__init__(*args)

        def hook(self, u, at, size, context):
            if at == 0x6F88890:
                self.helper_hits += 1
            super().hook(u, at, size, context)

    original, candidate = a.input.read_bytes(), a.candidate.read_bytes()
    checks = structure(original, candidate)
    rebuilt, _ = patch.build(original)
    require(rebuilt == candidate, 'deterministic rebuild differs from on-disk candidate')
    del rebuilt
    manifest = json.loads(a.manifest.read_text(encoding='utf8'))
    require(manifest['inputSha256'] == patch.pe.sha(original) and manifest['outputSha256'] == patch.pe.sha(candidate), 'manifest identity mismatch')
    for name, digest in manifest['recipeFiles'].items():
        require(patch.pe.sha((Path(__file__).parent / name).read_bytes()) == digest, 'recipe identity mismatch: ' + name)
    cases = []
    for flags in (0x202, 0x246, 0xAD7):
        for mode, result in [('null', 0), ('valid', 0x20005000), ('valid', 0), ('native_no_owner', 0)]:
            old_vm = TracedNative(original, mode, flags, result)
            old = old_vm.run()
            new_vm = TracedNative(candidate, mode, flags, result)
            new = new_vm.run()
            require(old_vm.helper_hits == 0 and new_vm.helper_hits == 1, 'actual cumulative helper must execute exactly once')
            require(new['finished'] and not new['faults'], 'candidate failed native return')
            require(new_vm.reg(UC_X86_REG_ESP) == Native.STACK + 20, 'outer stack cleanup')
            require(new_vm.reg(UC_X86_REG_EAX) == Native.ROW, 'native final return register')
            require(new['rowResult'] == hex(result if mode == 'valid' else 0), 'row result')
            if mode == 'null':
                require(old['faults'] == [dict(ip='0x37df6a3', address='0xf9c', size=4)], 'baseline crash signature')
                require(not new['calls'], 'null path entered effect callee')
                require(new['tail']['registers'][0] == 0, 'null EAX at continuation')
                require(new['tail']['registers'][-1] == flags, 'null EFLAGS restoration')
                require(new['tail']['registers'][-2] == Native.STACK - 12, 'five-argument cleanup')
            else:
                require(old == new, 'non-null full register/stack/call/row behavior changed')
                require(new['calls'][0]['this'] == hex(Native.SOURCE), 'thiscall source register')
                require(new['calls'][0]['flags'] == flags, 'callee entry flags')
            cases.append(dict(mode=mode, flags=hex(flags), result=hex(result), passed=True,
                              cumulativeHelperExecutions=new_vm.helper_hits, baseline=old, cumulative=new))
            del old_vm, new_vm
    negatives = []
    corrupted = bytearray(original)
    corrupted[-1] ^= 1
    for name, bad in [('unknown_input_sha', corrupted), ('already_cumulative_input', candidate)]:
        try:
            patch.build(bad)
        except ValueError as error:
            negatives.append(dict(case=name, rejected=True, reason=str(error)))
        else:
            raise AssertionError('invalid input accepted')
    del corrupted
    for name, offset in [('out_of_scope_change', len(candidate) - 1), ('model_code_change', 112535040),
                         ('bad_rel32', 112535709), ('bad_checksum', 216)]:
        bad = bytearray(candidate)
        bad[offset] ^= 1
        try:
            structure(original, bad)
        except ValueError as error:
            negatives.append(dict(case=name, rejected=True, reason=str(error)))
        else:
            raise AssertionError('corrupt candidate accepted')
        del bad
    report = dict(passed=True, pairedCases=len(cases), actualUnicornRuns=2 * len(cases), cases=cases,
                  inputSha256=patch.pe.sha(original), candidateSha256=patch.pe.sha(candidate),
                  inputPath=str(a.input.resolve()), candidatePath=str(a.candidate.resolve()),
                  manifestSha256=patch.pe.sha(a.manifest.read_bytes()), structuralChecks=checks,
                  deterministicRebuild=True, negativeChecks=negatives, stubs=STUBS,
                  environment=dict(python=sys.version, executable=sys.executable, platform=platform.platform(),
                                   unicornVersion=unicorn.__version__, unicornPath=unicorn.__file__),
                  boundaries=['12 bounded caller-window cases, not complete native effect/rendering validation',
                              'Unicorn maps image memory permissively; PE RX flags checked statically, not Windows loader execution',
                              'fixture object/stack contents and stop address; no game process or real player data',
                              'v4 model bytes preserved; cumulative model behavioral validation belongs to main agent',
                              'no installation, server deployment, PVF/NPK changes, or live acceptance'])
    require(patch.pe.sha(a.input.read_bytes()) == report['inputSha256'] and patch.pe.sha(a.candidate.read_bytes()) == report['candidateSha256'], 'input/candidate changed during verification')
    with a.report.open('x', encoding='utf8') as f:
        json.dump(report, f, indent=2)
        f.write('\n')
    print(json.dumps(dict(passed=True, pairedCases=12, actualUnicornRuns=24, negativeChecks=len(negatives), candidateSha256=report['candidateSha256'])))


if __name__ == '__main__':
    main()
