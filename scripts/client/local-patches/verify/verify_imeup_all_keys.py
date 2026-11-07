"""在模拟器里执行 base 与 candidate 的松键复核辅助代码，逐键比对。

对映射表里出现的每个 VK、每个映射到它的扫描码槽位，以及 GetAsyncKeyState
的四种返回值（按住 0x8000/0x8001，松开 0x0001/0x0000），从辅助代码入口一直
执行到它跳回补位循环（补位或跳过两个出口之一）。比对出口、messageCache、
DirectInput 当前帧 256 字节、全部通用寄存器与 EFLAGS。

通过条件：
- 两边都没有执行错误；寄存器与 EFLAGS 在出口处与入口一致（normal 出口的 `or` 除外，两边相同）。
- 只有 VK ∈ 0x08..0x24 ∪ 0x29..0xFE 且已松开的组合不同：base 补位（当前帧字节 |= 0x80），
  candidate 跳过补位，清 messageCache[VK] 及该 VK 全部扫描码槽位，其他字节不动。
- 方向键、按住的键、VK < 8 两边完全一致。
- 整个文件只有声明的 4 个字节和 PE 校验和不同。

GetAsyncKeyState 用桩函数代替；不模拟游戏、输入法或窗口焦点，不能替代实机验收。

    python -B verify/verify_imeup_all_keys.py --base v2.0.8.1.exe --candidate v2.0.8.2.exe
"""
import argparse
import json
import struct
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from emu import changed_offsets, map_image  # noqa: E402  (emu 会设置导入路径)
from unicorn import UC_HOOK_CODE, UcError
from unicorn.x86_const import (UC_X86_REG_EAX, UC_X86_REG_EBP, UC_X86_REG_EBX, UC_X86_REG_ECX,
                               UC_X86_REG_EDI, UC_X86_REG_EDX, UC_X86_REG_EFLAGS, UC_X86_REG_EIP,
                               UC_X86_REG_ESI, UC_X86_REG_ESP)

import imeup_all_keys as patch
from pe_image import Image, sha256, u32

SCRATCH, STUB, STATE, DEV, STACK = 0x30000000, 0x30000000, 0x30000800, 0x30001000, 0x30080000
REGS = (UC_X86_REG_EAX, UC_X86_REG_EBX, UC_X86_REG_ECX, UC_X86_REG_EDX,
        UC_X86_REG_ESI, UC_X86_REG_EDI, UC_X86_REG_EBP, UC_X86_REG_ESP)
ASYNC = (0x8000, 0x8001, 0x0001, 0x0000)


class Helper:
    def __init__(self, data):
        self.mu, img = map_image(data)
        status, info = patch.inspect(img)
        if status == 'incompatible':
            raise ValueError(info['reason'])
        self.status, va = status, int(info['helperVA'], 16)
        self.va = va
        code = img.read(va, patch.HELPER_SIZE)
        self.iat, self.cache, self.table = u32(code, 0x19), u32(code, 0x29), u32(code, 0x36)
        self.skip, self.resume = img.rel32_target(va + 0x57), img.rel32_target(va + 0x66)
        self.map = img.read(self.table, 256)
        self.mu.mem_map(SCRATCH, 0x100000)
        # 桩函数从数据区读返回值；不能每次改写代码本身，unicorn 会沿用已翻译的旧代码块
        self.mu.mem_write(STUB, b'\xa1' + struct.pack('<I', STATE) + b'\xc2\x04\x00')  # mov eax,[STATE]; ret 4
        self.mu.mem_write(self.iat, struct.pack('<I', STUB))
        self.stops = {self.skip: 'skip', self.resume: 'normal'}
        self.mu.hook_add(UC_HOOK_CODE, self._stop)
        self.exit = None

    def _stop(self, mu, address, size, _):
        if address in self.stops:
            self.exit = self.stops[address]
            mu.emu_stop()

    def __call__(self, vk, alias, state):
        mu = self.mu
        mu.mem_write(STATE, struct.pack('<I', state))
        mu.mem_write(self.cache, bytes(256))
        mu.mem_write(self.cache + vk, b'\x01')
        frame = bytes(0x80 if self.map[i] == vk else 0x40 for i in range(256))
        mu.mem_write(DEV + 0x10D, frame)
        entry = (vk, 0x11111111, DEV, 0x22222222, alias, 0x33333333, 0x44444444, STACK)
        for reg, value in zip(REGS, entry):
            mu.reg_write(reg, value)
        mu.reg_write(UC_X86_REG_EFLAGS, 0x202)
        self.exit = None
        try:
            mu.emu_start(self.va, 0, count=20000)
        except UcError as error:
            return dict(error=str(error), eip=hex(mu.reg_read(UC_X86_REG_EIP)))
        regs = tuple(mu.reg_read(r) for r in REGS)
        return dict(exit=self.exit, cache=mu.mem_read(self.cache + vk, 1)[0],
                    frame=bytes(mu.mem_read(DEV + 0x10D, 256)), before=frame,
                    regsPreserved=regs == entry, eflags=mu.reg_read(UC_X86_REG_EFLAGS))


def expected_change(vk, state):
    return (0x08 <= vk <= 0x24 or 0x29 <= vk <= 0xFE) and not state & 0x8000


def verify(base_path, candidate_path):
    a_data, b_data = Path(base_path).read_bytes(), Path(candidate_path).read_bytes()
    base, cand = Helper(a_data), Helper(b_data)
    problems = []
    if (base.status, cand.status) != ('absent', 'applied'):
        problems.append(f'状态应为 absent→applied，实际 {base.status}→{cand.status}')
    if (base.va, base.table, base.cache, base.iat) != (cand.va, cand.table, cand.cache, cand.iat):
        problems.append('两边辅助代码的地址或引用的数据地址不同')

    offsets = changed_offsets(a_data, b_data)
    img = Image(a_data)
    at = img.offset(base.va)
    declared = {at + patch.LOWER_AT, *range(at + patch.UPPER_AT, at + patch.UPPER_AT + 3),
                *range(img.checksum_at, img.checksum_at + 4)}
    if not set(offsets) <= declared or at + patch.LOWER_AT not in offsets:
        problems.append(f'文件差异超出声明范围：{[hex(o) for o in offsets]}')

    cases = changed = 0
    vks = sorted(set(base.map) - {0})
    for vk in vks:
        for alias in [i for i in range(256) if base.map[i] == vk]:
            for state in ASYNC:
                cases += 1
                x, y = base(vk, alias, state), cand(vk, alias, state)
                tag = f'vk={vk:#04x} alias={alias:#04x} async={state:#06x}'
                if 'error' in x or 'error' in y:
                    problems.append(f'{tag} 执行错误 {x.get("error")} / {y.get("error")}')
                    continue
                if not (x['regsPreserved'] and y['regsPreserved']):
                    problems.append(f'{tag} 寄存器未还原')
                if not expected_change(vk, state):
                    if {k: x[k] for k in ('exit', 'cache', 'frame', 'eflags')} != \
                       {k: y[k] for k in ('exit', 'cache', 'frame', 'eflags')}:
                        problems.append(f'{tag} 不应有差异，却不同')
                    continue
                changed += 1
                want_base = bytearray(x['before']); want_base[alias] |= 0x80
                want_cand = bytes(0 if base.map[i] == vk else x['before'][i] for i in range(256))
                if (x['exit'], x['cache'], x['frame']) != ('normal', 1, bytes(want_base)):
                    problems.append(f'{tag} base 行为与预期（补位）不符')
                if (y['exit'], y['cache'], y['frame']) != ('skip', 0, want_cand):
                    problems.append(f'{tag} candidate 行为与预期（清缓存、清槽位、跳过补位）不符')
    receipt = dict(baseSHA256=sha256(a_data), candidateSHA256=sha256(b_data), helperVA=hex(base.va),
                   tableVA=hex(base.table), cacheVA=hex(base.cache), fileDiffOffsets=[hex(o) for o in offsets],
                   vkInTable=len(vks), cases=cases, changedCases=changed,
                   unchangedCases=cases - changed, problems=problems[:50], gameExecuted=False)
    receipt['passed'] = not problems
    return receipt


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument('--base', type=Path, required=True)
    parser.add_argument('--candidate', type=Path, required=True)
    args = parser.parse_args()
    result = verify(args.base, args.candidate)
    print(json.dumps(result, ensure_ascii=False, indent=2))
    raise SystemExit(0 if result['passed'] else 1)
