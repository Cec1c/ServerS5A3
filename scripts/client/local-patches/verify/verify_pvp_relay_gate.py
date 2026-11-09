"""在模拟器里执行 base 与 candidate 的接收门，覆盖 class 0~2 × 全部 65536 个编号。

从接收入口的那次 call 目标（原接收门或补丁辅助代码）一直执行到返回；只有报文
头缓冲、栈和返回哨兵是夹具。

通过条件：没有执行错误，并且
- base 未打本补丁、candidate 已打：只有 (0,112)、(0,113) 从 0 变为 1；
- 两边都已打（例如 v2.0.8.1 → v2.0.8.2 只叠加了别的补丁）：判定完全一致。
控制组 (0,44)、(0,45)、(0,561)、(0,110)、(0,252)、(1,112)、(1,113) 不变。
这是接收门的静态检查，不运行游戏。

    python -B verify/verify_pvp_relay_gate.py --base 上游V208.exe --candidate v2.0.8.1.exe
"""
import argparse
import json
import struct
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from emu import map_image  # noqa: E402  (emu 会设置导入路径)
from unicorn import UcError  # noqa: E402
from unicorn.x86_const import (UC_X86_REG_EAX, UC_X86_REG_EBP, UC_X86_REG_ECX, UC_X86_REG_EDI,  # noqa: E402
                               UC_X86_REG_EIP, UC_X86_REG_ESP)

import pvp_relay_gate as patch  # noqa: E402
from pe_image import sha256  # noqa: E402

GATE_OBJECT_POINTER = 0x57EFA44
CONTROLS = ((0, 44), (0, 45), (0, 561), (0, 110), (0, 252), (1, 112), (1, 113))


class Gate:
    def __init__(self, data):
        self.mu, img = map_image(data)
        self.status = patch.inspect(img)[0]
        if self.status == 'incompatible':
            raise ValueError('接收门调用点与补丁特征不符')
        self.stack = 0x20000000
        self.mu.mem_map(self.stack, 0x100000)
        self.header = self.stack + 0x80000
        self.sentinel = self.stack + 0xF0000
        self.entry = img.rel32_target(patch.CALL_VA)
        self.ecx = struct.unpack('<I', bytes(self.mu.mem_read(GATE_OBJECT_POINTER, 4)))[0] or self.stack + 0x90000

    def __call__(self, klass, opcode):
        sp = self.stack + 0x7F000
        self.mu.mem_write(sp, struct.pack('<II', self.sentinel, opcode))
        self.mu.mem_write(self.header, bytes([klass]) + struct.pack('<H', opcode))
        for reg, value in ((UC_X86_REG_ESP, sp), (UC_X86_REG_EDI, self.header), (UC_X86_REG_EBP, sp + 0x1000),
                           (UC_X86_REG_ECX, self.ecx), (UC_X86_REG_EAX, 0)):
            self.mu.reg_write(reg, value)
        try:
            self.mu.emu_start(self.entry, self.sentinel, count=30000)
        except UcError:
            return 'ERR', self.mu.reg_read(UC_X86_REG_EIP)
        return self.mu.reg_read(UC_X86_REG_EAX), self.mu.reg_read(UC_X86_REG_ESP), self.mu.reg_read(UC_X86_REG_EDI)


def verify(base_path, candidate_path):
    base_data, candidate_data = Path(base_path).read_bytes(), Path(candidate_path).read_bytes()
    base, candidate = Gate(base_data), Gate(candidate_data)
    if candidate.status != 'applied':
        raise ValueError('candidate 必须已包含本补丁')
    expected = [] if base.status == 'applied' else [[k, o, 0, 1] for k, o in patch.OPENED]
    differences, errors, checked = [], 0, 0
    for klass in (0, 1, 2):
        for opcode in range(0x10000):
            a, b = base(klass, opcode), candidate(klass, opcode)
            checked += 1
            errors += a[0] == 'ERR' or b[0] == 'ERR'
            if a != b:
                differences.append([klass, opcode, a[0], b[0]])
    controls = [[k, o, base(k, o)[0], candidate(k, o)[0]] for k, o in CONTROLS]
    receipt = dict(baseSHA256=sha256(base_data), candidateSHA256=sha256(candidate_data),
                   baseStatus=base.status, baseGate=hex(base.entry), candidateGate=hex(candidate.entry),
                   checked=checked, errors=errors, differences=differences, expectedDifferences=expected,
                   controls=controls, gameExecuted=False)
    receipt['passed'] = not errors and differences == expected and all(a == b for _, _, a, b in controls)
    return receipt


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument('--base', type=Path, required=True)
    parser.add_argument('--candidate', type=Path, required=True)
    args = parser.parse_args()
    result = verify(args.base, args.candidate)
    print(json.dumps(result, ensure_ascii=False, indent=2))
    raise SystemExit(0 if result['passed'] else 1)
