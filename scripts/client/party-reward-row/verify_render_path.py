"""Extend the gate proof through real child traversal and row click tick."""
import json
from pathlib import Path
import sys
sys.dont_write_bytecode = True
import verify_core as v

DRAW_LEAF = v.EVENT + 0x10


class RenderMachine(v.Machine):
    def __init__(self, variant='member', patched=True):
        super().__init__(variant, patched=patched)
        for address, size in {0x37EA920: 0x99, 0x1F209D0: 0x3D,
                              0x1F1F370: 0xCF, 0x1F20720: 0x11C}.items():
            self.put(address, v.NEW_PE.read(address, size))
        for at in (0x391DF10, 0x224E5F0, 0x37EA2F0, 0x3826970, DRAW_LEAF):
            self.put(at, b'\xc3')
        self.w(v.NODE_PAGE + 0x81C, DRAW_LEAF)
        self.w(v.ROW + 0x3E8, v.LEAF)
        self.w(v.ROW + 0x480, 1)
        # Native animation update leaves zero-duration rows alone; row state1
        # then calls the native click consumer, whose existing callback is nil.
        self.w(v.ROW + 0x450, 0)
        self.w(v.ROW + 0x3C0, 0)
        for proxy, node, end, entry in self.iterator_parent.values():
            self.w(node, end)
            self.w(node + 8, self.r(entry))

    def hook(self, u, address, size, data):
        this = self.u.reg_read(v.UC_X86_REG_ECX)
        sp = self.u.reg_read(v.UC_X86_REG_ESP)
        if address in (0x37EA920, 0x1F209D0):
            self.trace.append(('native_path', hex(address), hex(this)))
            return
        if address in (0x391DF10, 0x224E5F0):
            proxy, node, end, entry = self.iterator_parent[this]
            out = self.r(sp + 4)
            self.w(out, proxy); self.w(out + 4, 0)
            self.w(out + 8, node if address == 0x391DF10 else end)
            self.ret(out, 4)
        elif address == 0x37EA2F0: self.ret()
        elif address == 0x3826970:
            assert this == v.LEAF
            self.trace.append(('native_tick_click_query', hex(this)))
            self.ret(int(self.variant == 'stale_clicked'))
        elif address == DRAW_LEAF:
            assert this == v.LEAF
            self.trace.append(('draw_leaf', hex(this)))
            self.ret(1)
        else: super().hook(u, address, size, data)

    def render(self, allow, label):
        self.trace.clear()
        before = [(at, bytes(self.u.mem_read(at, 4096))) for at in range(v.ROW, v.NODE_PAGE + 4096, 4096)]
        for register, value in v.NONVOL.items(): self.u.reg_write(register, value)
        self.u.reg_write(v.UC_X86_REG_ECX, v.PARENT)
        self.u.reg_write(v.UC_X86_REG_ESP, v.STACK)
        self.w(v.STACK, v.STOP)
        try: self.u.emu_start(0x37EA920, v.STOP, count=30000)
        except Exception:
            print(json.dumps(dict(variant=self.variant, label=label,
                eip=hex(self.u.reg_read(v.UC_X86_REG_EIP)), trace=self.trace)))
            raise
        assert self.u.reg_read(v.UC_X86_REG_EIP) == v.STOP
        assert self.u.reg_read(v.UC_X86_REG_EAX) == 1
        assert self.u.reg_read(v.UC_X86_REG_ESP) == v.STACK + 4
        for register, value in v.NONVOL.items(): assert self.u.reg_read(register) == value
        for at, data in before: assert bytes(self.u.mem_read(at, 4096)) == data
        executed = allow if self.patched else True
        assert self.trace.count(('native_tick_click_query', hex(v.LEAF))) == int(executed)
        assert self.trace.count(('draw_leaf', hex(v.LEAF))) == int(executed)
        return dict(variant=self.variant, label=label, patched=self.patched,
            allowed=executed, trace=list(self.trace), objectsAndRefcountsUnchanged=True)


def run_suite(target):
    assert not target.exists()
    cases = []
    for variant, allow in [('member', True), ('departed', False), ('no_party', False),
            ('self_no_party', True), ('category1', True), ('category2', True),
            ('category3', True), ('no_self', True), ('expired_parent', True),
            ('bad_owner', True), ('stale_clicked', False)]:
        for patched in (False, True):
            cases.append(RenderMachine(variant, patched).render(allow, 'single'))
    m = RenderMachine()
    cases.append(m.render(True, 'initial'))
    m.w(v.PARTY + 0x30, 0xFFFF)
    cases.append(m.render(False, 'removed'))
    m.w(v.PARTY + 0x44, 20)
    cases.append(m.render(True, 'rejoin-different-slot'))
    m.w(v.ROOT_UI + 0x3BC, 2)
    m.w(v.PARTY + 0x44, 0xFFFF)
    cases.append(m.render(True, 'frozen-team-category'))
    result = dict(passed=True, caseCount=len(cases), cases=cases,
        candidateSha256=v.RECEIPT['candidateSha256'],
        realExecution=['37EA920 parent and row child traversal',
            '3829190 generic row render', '1F209D0 native row tick',
            '1F1F370 zero-duration animation path', '1F20720 native click consumer'],
        fixtures=['synthetic objects', 'iterator/lock/decoder boundaries',
            'base prepare/render leaves', '3826970 click query and final leaf draw'],
        limitations=['nil callback branch only; reward callback is not executed',
            'no physical layout compaction, live GUI, direct update callers or GPU'],
        deployed=False, guiAccepted=False)
    v.write_result(target, result)
    print(json.dumps(dict(passed=True, caseCount=len(cases))))
    return result


if __name__ == "__main__":
    raise SystemExit(v.standalone_cli(
        run_suite, "render_path", "native-render-path-v2.json", 26))
