"""Non-nil reward callback gate regression; final callable and RTTI are fixtures."""
import hashlib
import json
import verify_core as v
from verify_direct_input import DirectMachine

CALLBACK = v.EVENT + 0x100
ROW_CONTROL = v.NODE_PAGE + 0xC00
FUNCTION_TABLE = v.NODE_PAGE + 0xD00


class CallbackMachine(DirectMachine):
    def __init__(self, variant='member', patched=True):
        super().__init__(variant, patched)
        self.callback_count = 0
        self.clicked = True
        for address, size in {0x1F1FAF0: 0xEC, 0x3604820: 0x6D,
                              0x8CC280: 0xA9, 0x1F1B910: 0x9F}.items():
            self.put(address, v.NEW_PE.read(address, size))
        self.put(0x3E73CB6, b'\xc3')
        self.put(CALLBACK, b'\xc3')
        self.w(v.ROW + 0x20, v.ROW)
        self.w(v.ROW + 0x24, ROW_CONTROL)
        self.w(ROW_CONTROL + 4, 2)
        self.w(ROW_CONTROL + 8, 1)
        self.w(v.ROW + 0x3C0, FUNCTION_TABLE | 1)
        self.w(FUNCTION_TABLE + 4, CALLBACK)

    def hook(self, u, address, size, data):
        sp = self.u.reg_read(v.UC_X86_REG_ESP)
        this = self.u.reg_read(v.UC_X86_REG_ECX)
        if address == 0x3826970:
            assert this == v.LEAF
            self.trace.append(('native_tick_click_query', hex(this)))
            self.ret(int(self.clicked))
        elif address == 0x3E73CB6:
            assert self.r(sp + 4) == v.ROW and self.r(sp + 8) == 0
            assert self.r(sp + 12) == 0x5564250 and self.r(sp + 16) == 0x558D354
            assert self.r(sp + 20) == 0
            self.ret(v.ROW)
        elif address == CALLBACK:
            assert self.r(sp + 4) == v.ROW + 0x3C8
            assert self.r(sp + 8) == v.ROW and self.r(sp + 12) == ROW_CONTROL
            # The cdecl final callable owns the by-value shared argument.
            # Model its normal release while leaving all native intermediary
            # promotion, copies, and destruction in the real executable code.
            assert self.r(ROW_CONTROL + 4) == 5
            self.w(ROW_CONTROL + 4, self.r(ROW_CONTROL + 4) - 1)
            self.callback_count += 1
            self.ret()
        else: super().hook(u, address, size, data)

    def check(self, allow, mode, label):
        self.trace.clear()
        self.callback_count = 0
        before = [(at, bytes(self.u.mem_read(at, 4096))) for at in range(v.ROW, v.NODE_PAGE + 4096, 4096)]
        for register, value in v.NONVOL.items(): self.u.reg_write(register, value)
        self.u.reg_write(v.UC_X86_REG_ECX, v.ROW)
        self.u.reg_write(v.UC_X86_REG_ESP, v.STACK)
        self.w(v.STACK, v.STOP)
        start = self.r(0x491A074 if mode == 'direct' else 0x491A078)
        try: self.u.emu_start(start, v.STOP, count=50000)
        except Exception:
            print(json.dumps(dict(variant=self.variant, mode=mode, patched=self.patched,
                eip=hex(self.u.reg_read(v.UC_X86_REG_EIP)), trace=self.trace)))
            raise
        assert self.u.reg_read(v.UC_X86_REG_EIP) == v.STOP
        assert self.u.reg_read(v.UC_X86_REG_ESP) == v.STACK + 4
        assert self.u.reg_read(v.UC_X86_REG_EAX) == 1
        for register, value in v.NONVOL.items(): assert self.u.reg_read(register) == value
        for at, old in before: assert bytes(self.u.mem_read(at, 4096)) == old, hex(at)
        expected = int((allow or not self.patched) and self.clicked)
        assert self.callback_count == expected
        return dict(variant=self.variant, mode=mode, label=label, patched=self.patched,
            clicked=self.clicked, callbackCount=self.callback_count, trace=list(self.trace),
            objectsAndRefcountsUnchanged=True)


def run_suite(target):
    assert not target.exists()
    cases = []
    for variant, allow in [('member', True), ('departed', False), ('no_party', False),
                            ('category2', True), ('stale_clicked', False), ('no_self', True)]:
        for patched in (False, True):
            for mode in ('draw', 'direct'):
                m = CallbackMachine(variant, patched)
                cases.append(m.check(allow, mode, 'single'))
    m = CallbackMachine()
    for label, allow in [('initial', True), ('departed', False), ('rejoined', True), ('not_clicked', True)]:
        if label == 'departed': m.w(v.PARTY + 0x30, 0xFFFF)
        elif label == 'rejoined': m.w(v.PARTY + 0x44, 20)
        elif label == 'not_clicked': m.clicked = False
        cases.append(m.check(allow, 'direct', label))
    result = dict(passed=True, caseCount=len(cases), cases=cases,
        candidateSha256=v.RECEIPT['candidateSha256'],
        realExecution=['1F20720 non-nil click callback path', '3604820 and 8CC280 row weak promotion',
            '1F1B910 cast wrapper and shared ownership', '1F1FAF0 callable dispatcher',
            'existing real render/direct input and gate paths'],
        fixtures=['RTTI result', 'final callable with by-value shared release',
            'synthetic click/objects/iterators/namespace/render leaves'],
        limitations=['final reward request logic, exception unwind, GUI and GPU not executed'],
        deployed=False, guiAccepted=False)
    target.write_text(json.dumps(result, indent=2) + '\n', encoding='utf8')
    print(json.dumps(dict(passed=True, caseCount=len(cases), target=str(target))))
    return result


if __name__ == "__main__":
    raise SystemExit(v.standalone_cli(run_suite, "callback", "native-callback-v1.json", 28))
