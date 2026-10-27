"""Exercise the row's independent vtable+18 input/draw pass."""
import json
import sys
sys.dont_write_bytecode = True
import verify_core as v
import verify_render_path as render


class DirectMachine(render.RenderMachine):
    def __init__(self, variant='member', patched=True):
        super().__init__(variant, patched)
        for address, size in {0x3829F50:0x4A, 0x3829DC0:0x185,
                              0x37E9ED0:0x1A}.items():
            self.put(address, v.NEW_PE.read(address, size))
        for at in (0x3819D40, 0x3819E30): self.put(at, b'\xc3')

    def hook(self, u, address, size, data):
        if address == 0x3819D40: self.ret(1)
        elif address == 0x3819E30:
            self.ret(7)
            self.u.reg_write(v.UC_X86_REG_EDX, 11)
        else: super().hook(u, address, size, data)

    def direct_pass(self):
        self.trace.clear()
        for register, value in v.NONVOL.items(): self.u.reg_write(register, value)
        self.u.reg_write(v.UC_X86_REG_ECX, v.ROW)
        self.u.reg_write(v.UC_X86_REG_ESP, v.STACK)
        self.w(v.STACK, v.STOP)
        self.u.emu_start(self.r(0x491A074), v.STOP, count=30000)
        assert self.u.reg_read(v.UC_X86_REG_EIP) == v.STOP
        assert self.u.reg_read(v.UC_X86_REG_ESP) == v.STACK + 4
        assert self.u.reg_read(v.UC_X86_REG_EAX) == 1
        for register, value in v.NONVOL.items(): assert self.u.reg_read(register) == value
        return dict(variant=self.variant, patched=self.patched, trace=list(self.trace),
            leafHits=self.trace.count(('hit_leaf', hex(v.LEAF))),
            leafDraws=self.trace.count(('draw_leaf', hex(v.LEAF))))


def run_suite(target):
    assert not target.exists()
    cases = []
    variants = [('member', True), ('departed', False), ('no_party', False),
                ('stale_clicked', False), ('category2', True), ('no_self', True)]
    for variant, allow in variants:
        for patched in (False, True):
            case = DirectMachine(variant, patched).direct_pass()
            expected = int(allow if patched else True)
            assert case['leafHits'] == expected and case['leafDraws'] == expected, case
            cases.append(case)
    m = DirectMachine()
    for label, allow in [('initial', True), ('leave', False), ('rejoin', True),
                         ('detach', False), ('reattach', True)]:
        if label == 'leave': m.w(v.PARTY + 0x30, 0xFFFF)
        elif label == 'rejoin': m.w(v.PARTY + 0x44, 20)
        elif label == 'detach': m.w(v.SELF + 0x4C8, 0)
        elif label == 'reattach': m.w(v.SELF + 0x4C8, v.PARTY)
        case = m.direct_pass()
        assert case['leafHits'] == int(allow) and case['leafDraws'] == int(allow), case
        case['label'] = label
        cases.append(case)
    result = dict(passed=True, caseCount=len(cases), cases=cases,
        candidateSha256=v.RECEIPT['candidateSha256'],
        v1NegativeControl='local-gate-v1/direct-input-gap.json',
        v1NegativeControlRequired=False, v1NegativeControlExecuted=False,
        realExecution=['vtable+18 wrapper and original 3829F50',
            '3829DC0 independent input traversal', '37E9ED0 virtual render tail',
            'shared/namespace/member helpers, normal nested render and click tick'],
        fixtures=['input-ready query, cursor query, iterator/lock/decoder boundaries',
            'draw/event leaves and captured-click query'],
        limitations=['captured-click query is synthesized, not a live OS event',
            'physical slot compaction and GUI remain unverified'],
        deployed=False, guiAccepted=False)
    v.write_result(target, result)
    print(json.dumps(dict(passed=True, caseCount=len(cases),
                          candidateSha256=v.RECEIPT['candidateSha256'])))
    return result


if __name__ == "__main__":
    raise SystemExit(v.standalone_cli(
        run_suite, "direct_input", "native-direct-input-v2.json", 17))
