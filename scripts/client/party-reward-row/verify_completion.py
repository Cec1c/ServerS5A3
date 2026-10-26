"""Frozen v161/v2 native completion consumers after a row render.

Real row getter/refcount and completion loops; map iterators, timer, animation
query, and final manager transition remain explicit fixtures. No live process.
"""
import json
from pathlib import Path
import verify_core as v
from verify_render_path import RenderMachine, DRAW_LEAF

MANAGER, OWN_ROW, ROW_RC, OWN_RC, ROOT2, ROOT2_RC, OTHER_ROW, OTHER_RC = range(0x110000, 0x118000, 0x1000)
MAP = 0x118000


class CompletionMachine(RenderMachine):
    def __init__(self, variant, state, patched, unbound=False, second_unbound=False):
        super().__init__(variant, patched)
        self.transitions = []
        self.unbound = unbound
        self.second_unbound = second_unbound
        self.elapsed = 501
        self.escape_ready = 0
        for address, size in {0x8ED470: 0x2A7, 0x1F19B30: 0x10,
                0x1F1BC90: 0x8A, 0x1F197B0: 0xB, 0x190CC50: 0x36,
                0x190C900: 0x64}.items():
            self.put(address, v.NEW_PE.read(address, size))
        for at in (0x8DF100, 0xA8F6C0, 0x2208E60, 0x35D4500,
                   0x190C6D0, 0x190B670, 0x3816310, 0x38168F0, 0x8ECF90):
            self.put(at, b'\xc3')
        self.put(MANAGER + 0x60, b'\x01\x01')
        self.w(MANAGER + 0x420, 0)
        self.w(v.ROW + 0x480, state)
        self.w(v.ROW + 0x3F8, v.LEAF)
        self.w(v.ROOT_UI + 0x3C0, 3)
        self.w(v.ROOT_UI + 0x3C8, MAP + 0x200)
        self.w(v.ROOT_UI + 0x3CC, MAP + 0x210)
        for idx, (row, rc, owner) in enumerate(((v.ROW, ROW_RC, 20), (OWN_ROW, OWN_RC, 19))):
            self.w(MAP + 0x200 + idx * 8, row)
            self.w(MAP + 0x204 + idx * 8, rc)
            self.w(rc + 4, 2); self.w(rc + 8, 1)
            self.w(row + 0x3E4, owner)
        if unbound: self.w(OWN_ROW + 0x3E4, 0xFFFFFFFF)
        self.w(ROOT2 + 0x3BC, 2)
        self.w(ROOT2 + 0x3C8, MAP + 0x220)
        self.w(ROOT2 + 0x3CC, MAP + 0x228)
        self.w(MAP + 0x220, OTHER_ROW); self.w(MAP + 0x224, OTHER_RC)
        self.w(OTHER_ROW + 0x3E4, 0xFFFFFFFF if second_unbound else 77)
        for rc in (ROOT2_RC, OTHER_RC):
            self.w(rc + 4, 2); self.w(rc + 8, 1)
        self.proxy, self.container = MAP, MAP + 0x20
        self.nodes = [MAP + 0x50, MAP + 0x80]
        self.end = MAP + 0xB0
        self.w(self.proxy, self.container)
        self.w(self.container + 8, MAP + 0xE0)
        for node, obj, rc in ((self.nodes[0], v.ROOT_UI, v.CONTROL), (self.nodes[1], ROOT2, ROOT2_RC)):
            self.w(node + 0x10, obj); self.w(node + 0x14, rc)
        self.tracked_pages = list(range(v.ROW, v.NODE_PAGE + 4096, 4096)) + list(range(MANAGER, MAP + 4096, 4096))
        for at in self.tracked_pages:
            if at not in self.pages: self.put(at, bytes(4096))

    def hook(self, u, address, size, data):
        sp = self.u.reg_read(v.UC_X86_REG_ESP)
        this = self.u.reg_read(v.UC_X86_REG_ECX)
        if address in (0x8DF100, 0xA8F6C0):
            assert this == MANAGER + 0x424
            out = self.r(sp + 4)
            self.w(out, self.proxy); self.w(out + 4, 0)
            self.w(out + 8, self.nodes[0] if address == 0x8DF100 else self.end)
            self.ret(out, 4)
        elif address == 0x2208E60:
            node = self.r(this + 8)
            assert node in self.nodes
            nxt = self.nodes[1] if node == self.nodes[0] else self.end
            self.w(this + 8, nxt)
            self.ret(pop=8)
        elif address == 0x35D4500:
            assert this == v.LEAF
            self.trace.append(('animation_in_progress', hex(this)))
            self.ret(0)
        elif address == 0x190C6D0:
            assert this == MANAGER
            self.transitions.append(self.r(sp + 4))
            self.ret(pop=4)
        elif address == 0x190B670:
            assert this == MANAGER
            self.ret()
        elif address == 0x3816310:
            assert this == MANAGER + 0x404
            self.ret(self.elapsed)
        elif address == 0x38168F0:
            assert this == MANAGER + 0x404 and self.r(sp + 4) == self.r(sp + 8) == 0
            self.ret(pop=8)
        elif address == 0x8ECF90:
            assert this == MANAGER
            self.ret(self.escape_ready)
        else: super().hook(u, address, size, data)

    def invoke(self, address, this):
        for reg, value in v.NONVOL.items(): self.u.reg_write(reg, value)
        self.w(v.STACK, v.STOP)
        self.u.reg_write(v.UC_X86_REG_ESP, v.STACK)
        self.u.reg_write(v.UC_X86_REG_ECX, this)
        try: self.u.emu_start(address, v.STOP, count=50000)
        except Exception:
            print(json.dumps(dict(variant=self.variant, patched=self.patched,
                eip=hex(self.u.reg_read(v.UC_X86_REG_EIP)), trace=self.trace)))
            raise
        assert self.u.reg_read(v.UC_X86_REG_EIP) == v.STOP
        assert self.u.reg_read(v.UC_X86_REG_ESP) == v.STACK + 4
        for reg, value in v.NONVOL.items(): assert self.u.reg_read(reg) == value
        return self.u.reg_read(v.UC_X86_REG_EAX) & 255

    def check(self, complete, allow):
        before = {at: bytes(self.u.mem_read(at, 4096)) for at in self.tracked_pages}
        def unchanged():
            for at, old in before.items():
                assert bytes(self.u.mem_read(at, 4096)) == old, hex(at)
        self.invoke(self.r(0x491A078), v.ROW)
        rendered = allow if self.patched else True
        state = self.r(v.ROW + 0x480)
        assert self.trace.count(('draw_leaf', hex(v.LEAF))) == int(rendered)
        assert self.trace.count(('native_path', '0x1f209d0', hex(v.ROW))) == int(rendered)
        assert self.trace.count(('native_path', '0x37ea920', hex(v.ROW))) == int(rendered)
        assert self.trace.count(('native_tick_click_query', hex(v.LEAF))) == int(rendered and state == 1)
        assert self.trace.count(('animation_in_progress', hex(v.LEAF))) == int(rendered and state == 2)
        if not rendered: assert self.trace == []
        unchanged()
        observed_complete = self.invoke(0x8ED470, MANAGER)
        assert observed_complete == int(complete)
        unchanged()
        self.invoke(0x190CC50, MANAGER)
        assert self.transitions == [5 if complete else 3]
        manual_transitions = list(self.transitions)
        unchanged()
        self.transitions.clear()
        self.invoke(0x190C900, MANAGER)
        assert self.transitions == ([4] if complete else [])
        assert self.r(MANAGER + 0x420) == 1
        self.w(MANAGER + 0x420, 0)  # only documented timer-consumer mutation
        unchanged()
        return dict(variant=self.variant, patched=self.patched, state=self.r(v.ROW + 0x480),
            caseId=f'{self.variant}/state{state}/patched{int(self.patched)}/ownUnbound{int(self.unbound)}/secondUnbound{int(self.second_unbound)}',
            expectedComplete=complete, observedComplete=bool(observed_complete),
            complete=complete, ownRowUnbound=self.unbound, secondRootRowUnbound=self.second_unbound, manualTransition=manual_transitions[0],
            timerTransitions=list(self.transitions), originalSlot=self.r(v.ROW + 0x3E0),
            refcountsAndRewardBindingsPreserved=True, renderTrace=list(self.trace))


def run_suite(target):
    assert not target.exists()
    cases = []
    for variant, allow in [('member', True), ('departed', False), ('no_party', False), ('category2', True)]:
        for state in (0, 1, 2, 3, 4):
            for patched in (False, True):
                for unbound, second_unbound in ((False, False), (True, False), (False, True)):
                    m = CompletionMachine(variant, state, patched, unbound, second_unbound)
                    cases.append(m.check(not (unbound or second_unbound), allow))
    assert len(cases) == len({case['caseId'] for case in cases}) == 120
    result = dict(passed=True, caseCount=len(cases), callsPerCase=4,
        candidateSha256=v.RECEIPT['candidateSha256'], cases=cases,
        realExecution=['outer render gate and real render/row tick',
            '8ED470 complete root/row enumeration and owner binding predicate',
            '1F19B30 row count, 1F1BC90 row shared getter and reference helpers',
            '190CC50 manual and 190C900 timer transition decisions'],
        fixtures=['synthetic objects and map/render iterators', 'animation status false',
            'timer, input, alternate advance query', '190C6D0 transition sink',
            'render leaves and click query (nil callback)',
            'after unchanged render/predicate/manual snapshots and timer counter=1 assertion, restore only timer counter for final comparison'],
        limitations=['transition target internals and complete GUI lifecycle not executed',
            'no live resource/GPU or physical slot compaction'], deployed=False, guiAccepted=False)
    target.write_text(json.dumps(result, indent=2) + '\n', encoding='utf8')
    print(json.dumps(dict(passed=True, caseCount=len(cases), target=str(target))))
    return result


if __name__ == "__main__":
    raise SystemExit(v.standalone_cli(run_suite, "completion", "native-completion-v1.json", 120))
