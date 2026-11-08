"""Bounded native CMD121 completion verification for an exact EXE pair.

The dispatcher, gate, byte readers, pending decrement, status update, and
registered callback execute as x86 instructions. Transport decoding, STL map
lookup/iterators, runtime callback-table lookup, CRT, and UI leaves are fixture
adapters; this does not test live networking, rendering, or gameplay.
"""

import argparse
import hashlib
import importlib.util
import json
from pathlib import Path
import struct
import sys


BASELINE_SHA = 'd333e4ce5f210a254932d7ba6e06e0649fc37f45ec248f033534e175e72e2d0b'
CANDIDATE_SHA = '0e4a99cb278bb4ec37fc90c027903417eb2f790915941000538fcf2209b81872'
COMBINED_SHA = '7861cd3c546e73651ddfd28047686a29e580f5bebd38b1a7fd681d3b36fc24e0'
REGISTRATION = bytes.fromhex('6a0068a056fa016a798bcee845a25600')
CALLBACK_HEAD = bytes.fromhex('558bec807d0c007534')
TRACE_ADDRESSES = {
    0x6F20400, 0x6F9DC00, 0x2523C94, 0x2523F47, 0x2523CF6,
    0x2523D2E, 0x374FC40, 0x374FC9D, 0x374FCB1, 0x2523D85,
    0x37501A0, 0x251E26C, 0x1FA56A0,
}


def load_module(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def verify(baseline, candidate, fixture_root):
    builder = load_module('host_completion_builder', Path(__file__).with_name('build_candidate.py'))
    assert hashlib.sha256(baseline).hexdigest() == BASELINE_SHA, 'baseline SHA mismatch'
    candidate_sha = hashlib.sha256(candidate).hexdigest()
    assert candidate_sha in (CANDIDATE_SHA, COMBINED_SHA), 'candidate SHA mismatch'
    rebuilt, recipe = builder.build(baseline)
    assert recipe['outputSha256'] == CANDIDATE_SHA, 'completion intermediate mismatch'
    if candidate_sha == COMBINED_SHA:
        display = load_module('host_display_builder', fixture_root/'party-host-authority/build_candidate.py')
        rebuilt, _ = display.build(rebuilt)
    assert rebuilt == candidate, 'candidate recipe mismatch'

    sys.path.insert(0, str(fixture_root))
    from test_trade_invitation import TradeNative
    from patch_guild_war_refresh_v173 import pe
    from unicorn.x86_const import UC_X86_REG_ECX

    _, sections = pe.headers(baseline)
    for va, expected in ((0x1FBA04B, REGISTRATION), (0x1FA56A0, CALLBACK_HEAD)):
        off = pe.offset(sections, va, len(expected))
        assert baseline[off:off + len(expected)] == expected, f'baseline byte mismatch at {va:#x}'
        assert candidate[off:off + len(expected)] == expected, f'candidate byte mismatch at {va:#x}'

    class NativeCompletion(TradeNative):
        PEND = 0x2000F000
        STATUS = 0x2000F100
        ITER = 0x2000F200
        END = 0x2000F300

        def __init__(self, image, pending, status=7):
            super().__init__(image)
            self.trace = []
            self.callback_args = []
            self.ui_calls = []
            # These two functions must run from the mapped EXE.
            self.hooks.pop(0x374FC40, None)
            self.hooks.pop(0x37501A0, None)
            self.hooks[0xB46290] = self.map_find
            self.hooks[0xA8F6C0] = self.map_end
            self.hooks[0x2523FF0] = lambda: self.ret(value=0x2000C000)
            self.hooks[0x2524210] = lambda: self.ret(4, self.NETWORK)
            self.hooks[0x32BEA50] = self.ui_call
            # Runtime registration is adapted only after verifying its raw bytes.
            self.put(0x2000C000, 0x1FA56A0, 0)
            self.put(self.PEND, self.PEND, 121, 0, 0, pending)
            self.put(self.STATUS, self.STATUS, 121, 0, 0, status)
            self.put(self.ITER, self.PEND)
            self.put(self.END, self.PEND, 0, 0)
            self.put(0x56A3798, self.CHAT)

        def map_find(self):
            map_address = self.reg(UC_X86_REG_ECX)
            key = self.read(self.arg(1))
            assert key == 121, (hex(map_address), key)
            nodes = {self.NETWORK + 0x2BD094: self.PEND,
                     self.NETWORK + 0x2BD0A8: self.STATUS}
            node = nodes[map_address]
            out = self.arg()
            self.put(out, self.ITER, 0, node)
            self.put(self.ITER, node)
            self.put(self.END, node, 0, 0)
            self.trace.append(['map_find', hex(map_address), hex(node)])
            self.ret(8, out)

        def map_end(self):
            out = self.arg()
            self.put(out, self.END, 0, 0)
            self.ret(4, self.END)

        def ui_call(self):
            self.ui_calls.append([self.arg(i) for i in range(3)])
            self.ret(12)

        def hook(self, u, at, size, ctx):
            if at in TRACE_ADDRESSES:
                self.trace.append(hex(at))
            if at == 0x1FA56A0:
                self.callback_args.append([self.arg(i) for i in range(4)])
            super().hook(u, at, size, ctx)

        def receive121(self, body):
            wire = bytearray(16) + body
            wire[0] = 1
            struct.pack_into('<H', wire, 1, 121)
            struct.pack_into('<I', wire, 3, len(wire))
            self.u.mem_write(self.PACKET, bytes(wire))
            before = [self.read(self.PEND + 0x10), self.read(self.STATUS + 0x10)]
            marks = len(self.trace), len(self.callback_args), len(self.messages), len(self.ui_calls)
            self.run(0x25239A0, (0, self.PACKET, 0), ecx=self.NETWORK)
            t, c, m, q = marks
            return dict(body=body.hex(), before=before,
                        after=[self.read(self.PEND + 0x10), self.read(self.STATUS + 0x10)],
                        remaining=self.read(0x57F9AD4),
                        callback=self.callback_args[c:], messages=self.messages[m:],
                        uiCalls=self.ui_calls[q:], trace=self.trace[t:])

    results = {}
    for label, image in (('baseline', baseline), ('candidate', candidate)):
        lab = NativeCompletion(image, 2)
        successes = [lab.receive121(b'\x01') for _ in range(2)]
        failed = NativeCompletion(image, 1).receive121(b'\x00\x03')
        zero = NativeCompletion(image, 0).receive121(b'\x01')
        errors = {}
        for code in (23, 95):
            errors[str(code)] = NativeCompletion(image, 1).receive121(bytes((0, code)))
        results[label] = dict(successes=successes, failure3=failed,
                              pendingZero=zero, errors=errors)

        if label == 'baseline':
            assert [r['after'] for r in successes] == [[2, 7], [2, 7]]
            assert all(r['callback'] == [] and r['remaining'] == 1 for r in successes)
            assert failed['after'] == [1, 7] and failed['remaining'] == 2
            assert zero['after'] == [0, 7] and zero['callback'] == []
            assert all(r['callback'] == [] and r['uiCalls'] == [] for r in errors.values())
        else:
            assert [r['after'] for r in successes] == [[1, 0], [0, 0]]
            assert all(r['callback'] == [[121, 1, 0, 0]] and r['remaining'] == 0
                       and r['trace'].count('0x374fcb1') == 1
                       and r['trace'].count('0x1fa56a0') == 1 for r in successes)
            assert failed['after'] == [0, 8] and failed['callback'] == [[121, 0, 3, 0]]
            assert failed['remaining'] == 0 and failed['messages'] == [] and failed['uiCalls'] == []
            assert zero['after'][0] == 0 and zero['callback'] == [[121, 1, 0, 0]]
            assert zero['remaining'] == 0 and '0x374fcb1' not in zero['trace']
            for code, message_id in ((23, 0x8DC0), (95, 0x56F6)):
                r = errors[str(code)]
                assert r['after'] == [0, 8] and r['remaining'] == 0
                assert r['callback'] == [[121, 0, code, 0]]
                assert r['messages'] == [message_id]
                # The pointer is the fixture's synthetic UI string object.
                assert r['uiCalls'] == [[0x2D6, 0x2000B800, 0]]
    return dict(passed=True, baselineSha256=BASELINE_SHA, candidateSha256=candidate_sha,
                deterministicRecipe=True, callbackRegistration=hex(0x1FBA04B),
                instructionLimitPerCall=100000, fixtureAdapters=[
                    'decoded transport', 'STL map lookup/iterators',
                    'runtime callback-table lookup', 'CRT/string/UI leaves'],
                liveClientAcceptance=False, cases=results)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--baseline', type=Path, required=True)
    parser.add_argument('--candidate', type=Path, required=True)
    parser.add_argument('--python-deps', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    sys.path.insert(0, str(args.python_deps.resolve()))
    fixture_root = Path(__file__).resolve().parent.parent
    result = verify(args.baseline.read_bytes(), args.candidate.read_bytes(), fixture_root)
    with args.output.open('x', encoding='utf-8') as stream:
        json.dump(result, stream, ensure_ascii=False, indent=2)
    print(json.dumps({k: v for k, v in result.items() if k != 'cases'}))


if __name__ == '__main__':
    main()
