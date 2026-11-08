"""Execute the old module's guards and hook ownership code without launching DNF."""
import hashlib
import json
import struct
import pefile
from unicorn import Uc, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE
from unicorn.x86_const import *
from verification_paths import BASE, D

STACK = 0x10010000
STOP = 0x10020000
REGS = (UC_X86_REG_EAX, UC_X86_REG_EBX, UC_X86_REG_ECX, UC_X86_REG_EDX,
        UC_X86_REG_ESI, UC_X86_REG_EDI, UC_X86_REG_EBP)

class Legacy:
    def __init__(self, path):
        self.raw = path.read_bytes()
        self.pe = pefile.PE(data=self.raw)
        self.base = self.pe.OPTIONAL_HEADER.ImageBase
        self.u = Uc(UC_ARCH_X86, UC_MODE_32)
        self.u.mem_map(self.base, (self.pe.OPTIONAL_HEADER.SizeOfImage + 4095) & ~4095)
        self.u.mem_write(self.base, self.pe.get_memory_mapped_image())
        self.u.mem_map(STACK - 0x10000, 0x30000)
        self.u.mem_write(STOP, b'\xc3' * 256)
        self.stop = STOP
        self.adapters = {}
        self.u.hook_add(UC_HOOK_CODE, self.hook)
        self.iat(0x6f21000, lambda: self.base, 4)  # GetModuleHandleW
        self.iat(0x6f21034, self.protect, 16)  # VirtualProtect
        self.iat(0x6f21020, lambda: 0xffffffff, 0)  # GetCurrentProcess
        self.iat(0x6f2102c, lambda: 1, 12)  # FlushInstructionCache
        self.iat(0x6f21038, self.query, 12)  # VirtualQuery
        self.adapters[0x6f189d0] = (self.readable, 0)

    def word(self, address):
        return struct.unpack('<I', self.u.mem_read(address, 4))[0]

    def arg(self, i=0):
        return self.word(self.u.reg_read(UC_X86_REG_ESP) + 4 + i * 4)

    def iat(self, address, fn, pop):
        target = STOP + 16 + len(self.adapters) * 8
        self.u.mem_write(address, struct.pack('<I', target))
        self.adapters[target] = (fn, pop)

    def readable(self):
        at, n = self.arg(), self.arg(1)
        assert self.base <= at and at + n <= self.base + self.pe.OPTIONAL_HEADER.SizeOfImage
        return 1

    def protect(self):
        assert self.arg() in (0x32becc0, 0x29bbee3) and self.arg(1) == 5
        self.u.mem_write(self.arg(3), struct.pack('<I', 0x20))
        return 1

    def query(self):
        address, out, length = self.arg(), self.arg(1), self.arg(2)
        assert length == 28 and self.base <= address < self.base + self.pe.OPTIONAL_HEADER.SizeOfImage
        self.u.mem_write(out, struct.pack('<IIIIIII', address & ~4095, self.base, 0x40,
                                         4096, 0x1000, 0x40, 0x1000000))
        return 28

    def hook(self, u, at, size, unused):
        if at == self.stop:
            u.emu_stop()
        elif at in self.adapters:
            fn, pop = self.adapters[at]
            sp = u.reg_read(UC_X86_REG_ESP)
            result = fn()
            u.reg_write(UC_X86_REG_EAX, result)
            u.reg_write(UC_X86_REG_ESP, sp + 4 + pop)
            u.reg_write(UC_X86_REG_EIP, self.word(sp))

    def start(self, address, args=(), stop=STOP):
        self.stop = stop
        self.u.mem_write(STACK - 64, b'\xa5' * 128)
        self.u.mem_write(STACK, struct.pack('<' + 'I' * (1 + len(args)), STOP, *args))
        for i, reg in enumerate(REGS):
            self.u.reg_write(reg, 0x11110000 + i * 0x100)
        self.u.reg_write(UC_X86_REG_EFLAGS, 0x247)
        self.u.reg_write(UC_X86_REG_ESP, STACK)
        self.u.emu_start(address, STOP + 256, count=100000, timeout=2000000)
        assert self.u.reg_read(UC_X86_REG_EIP) == stop, 'instruction/time budget exhausted'
        if stop == STOP:
            assert self.u.reg_read(UC_X86_REG_ESP) == STACK + 4, 'cdecl stack mismatch'
            for i in (1, 4, 5, 6):
                assert self.u.reg_read(REGS[i]) == 0x11110000 + i * 0x100
        return self.u.reg_read(UC_X86_REG_EAX) & 255

    def snapshot(self):
        sp = self.u.reg_read(UC_X86_REG_ESP)
        return dict(registers=[self.u.reg_read(r) for r in (*REGS, UC_X86_REG_ESP, UC_X86_REG_EFLAGS)],
                    stack=bytes(self.u.mem_read(sp, STACK + 16 - sp)).hex())

def verify():
    original = Legacy(BASE)
    candidate = Legacy(D / 'client/DNF.exe')
    build = json.loads((D / 'client-build.json').read_text('utf8'))
    assert hashlib.sha256(candidate.raw).hexdigest() == build['outputSha256']
    # Execute byte-identical compiled CodeMatches, with only OS memory access adapted.
    assert original.pe.get_data(0x6f12ad0 - original.base, 0x150) == candidate.pe.get_data(0x6f12ad0 - candidate.base, 0x150)
    assert original.start(0x6f12ad0) == candidate.start(0x6f12ad0) == 1
    # Reproduce 007's two shared CALL edits, then restore: the old validator
    # must reject the conflict. No validator/comparison/ChangeCall is mocked.
    saved = {}
    for at, name in ((0x32becc0, 'Factory'), (0x29bbee3, 'Layout')):
        saved[at] = bytes(candidate.u.mem_read(at, 5))
        candidate.u.mem_write(at, b'\xe8' + struct.pack('<i', build['exports'][name] - at - 5))
    assert candidate.start(0x6f12ad0) == 0
    for at, data in saved.items():
        candidate.u.mem_write(at, data)
    assert candidate.start(0x6f12ad0) == 1

    # Compare displaced prologues with compiled trampolines, including flags,
    # nonvolatile registers, caller arguments and the active stack frame.
    trampoline_cases = []
    for entry, resume, name in ((0x6ef3fd1, 0x6ef3fd7, 'PreviousFactory'),
                                (0x6ef40fa, 0x6ef4101, 'PreviousLayout')):
        for value in (0, 1, 255):
            for m in (original, candidate):
                m.u.mem_write(0x6f02218, bytes([value]))
            original.start(entry, (1215, 0x2468ace0), resume)
            candidate.start(build['exports'][name], (1215, 0x2468ace0), resume)
            assert candidate.snapshot() == original.snapshot(), name
            trampoline_cases.append(dict(name=name, state=value))
    for m in (original, candidate):
        m.u.mem_write(0x6f02218, b'\0')

    # Follow the actual new dispatcher and predecessor bodies into the final
    # native endpoints. The endpoint adapters assert the untouched arguments;
    # every intermediate branch, call and epilogue executes from the image.
    fallback_cases = []
    for m in (original, candidate):
        for entry, endpoint, args in ((0x6ef3fd1, 0x3365ba0, (1215, 0x2468ace0)),
                                      (0x6ef40fa, 0x382ab90, (0, 0x2468ace0))):
            seen = []
            def native_endpoint():
                assert (m.arg(), m.arg(1)) == args
                seen.append(endpoint)
                return 1
            m.adapters[endpoint] = (native_endpoint, 0)
            assert m.start(entry, args) == 1 and seen == [endpoint]
            del m.adapters[endpoint]
            fallback_cases.append(dict(candidate=m is candidate, entry=hex(entry)))

    # Actual old statistics, ranking and battle-pass temporary install/restore
    # pairs must keep the shared sites and the new dispatcher chain intact.
    dispatch = {a: bytes(candidate.u.mem_read(a, n)) for a, n in ((0x6ef3fd1, 6), (0x6ef40fa, 7))}
    cycles = []
    for name, factory, layout in (('statistics', 0x6f14660, 0x6f154e0),
                                  ('ranking', 0x6f146d0, 0x6f15580),
                                  ('battlepass', 0x6f14760, 0x6f15630)):
        for at, target in ((0x32becc0, factory), (0x29bbee3, layout)):
            fixture = STOP + 512
            candidate.u.mem_write(fixture, saved[at])
            assert candidate.start(0x6f125f0, (at, target, fixture, 1)) == 1
            assert bytes(candidate.u.mem_read(at, 5)) == b'\xe8' + struct.pack('<i', target - at - 5)
            assert candidate.start(0x6f125f0, (at, target, fixture, 1)) == 0  # duplicate ownership rejected
            assert candidate.start(0x6f125f0, (at, target, fixture, 0)) == 1
            assert bytes(candidate.u.mem_read(at, 5)) == saved[at]
        assert candidate.start(0x6f12ad0) == 1
        for at, data in dispatch.items():
            assert bytes(candidate.u.mem_read(at, len(data))) == data
        cycles.append(name)
    result = dict(passed=True, candidateSha256=build['outputSha256'], realLegacyCodeMatches=True,
                  conflictingCallsRejected=True, displacedInstructionsEquivalent=trampoline_cases,
                  actualLegacyInstallRestore=cycles, duplicateOwnershipRejected=True,
                  actualDispatcherFallback=fallback_cases,
                  newDispatcherSurvivesLegacyRestore=True, gameExecuted=False,
                  adapters=['GetModuleHandleW', 'VirtualQuery', 'VirtualProtect', 'GetCurrentProcess', 'FlushInstructionCache', 'Readable mapped-range assertion'])
    (D / 'cumulative-hooks-tests.json').write_text(json.dumps(result, indent=2), 'utf8')
    print(json.dumps(result, indent=2))

if __name__ == '__main__':
    verify()
