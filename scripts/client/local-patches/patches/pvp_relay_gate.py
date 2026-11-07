"""擂台赛接收门：放行 class-0 的 112/113（骰子结果、申请状态）。

原版接收入口 0x025239A0 读取 [edi+1] 的 u16 编号后，在 0x02523A13 调用
接收门 0x06F20400，返回 0 的报文直接丢弃，擂台赛因此收不到骰子。
本补丁把这一处 call 改指向 `.bp0` 空余区里的 37 字节辅助代码：只有
class 0 且编号为 112/113 时返回 1，其余报文跳回原接收门，判定不变。

定位依据：0x02523A13 必须是 E8 call，目标为原接收门（未打补丁）或本补丁
的辅助代码（已打补丁）。上游若移动了接收入口或接收门，这里会判为
incompatible，需要人工重新定位后再移植。

详细背景：Docs/PVP_RELAY_DICE_20261102.md
"""
import struct

from pe_image import IMAGE_BASE, Image, rel32

ID = 'pvp-relay-gate'
SUMMARY = '擂台赛接收门放行 class-0 112/113'
CALL_VA = 0x2523A13
GATE_VA = 0x6F20400
SECTION = '.bp0'
OPENED = ((0, 112), (0, 113))


def helper(helper_va):
    code = bytearray()
    code += bytes.fromhex('803f00')      # cmp byte ptr [edi], 0      ; packet class
    code += bytes.fromhex('7513')        # jne original
    code += bytes.fromhex('0fb7442404')  # movzx eax, word ptr [esp+4] ; opcode argument
    code += bytes.fromhex('83f870')      # cmp eax, 112
    code += bytes.fromhex('740e')        # je allow
    code += bytes.fromhex('83f871')      # cmp eax, 113
    code += bytes.fromhex('7409')        # je allow
    code += b'\x90' * 4                  # keep branch targets explicit
    original = helper_va + len(code)
    code += b'\xe9' + rel32(original, 5, GATE_VA)        # original: jmp gate
    code += bytes.fromhex('b801000000c20400')           # allow: mov eax,1; ret 4
    return bytes(code)


HELPER_SIZE = len(helper(0))


def _slot(img):
    """.bp0 里放辅助代码的位置：节的虚拟大小向上 16 字节对齐处。"""
    s = img.section(SECTION)
    if s is None or not s['flags'] & 0x20000000:
        return None, None
    rva = s['rva'] + ((s['vsize'] + 15) & ~15)
    return s, IMAGE_BASE + rva


def inspect(img):
    try:
        call_at = img.offset(CALL_VA, 5)
    except ValueError:
        return 'incompatible', dict(reason='接收门调用点不在文件数据内')
    if img.data[call_at] != 0xE8:
        return 'incompatible', dict(reason=f'{CALL_VA:#x} 不再是 call 指令')
    target = img.rel32_target(CALL_VA)
    if target == GATE_VA:
        s, helper_va = _slot(img)
        if s is None:
            return 'incompatible', dict(reason='.bp0 不存在或不可执行')
        rva = helper_va - IMAGE_BASE
        if rva + HELPER_SIZE > s['rva'] + s['rsize']:
            return 'incompatible', dict(reason='.bp0 空余区不够')
        if any(img.read(helper_va, HELPER_SIZE)):
            return 'incompatible', dict(reason='.bp0 预定位置已被占用')
        return 'absent', dict(helperVA=hex(helper_va))
    s = img.section_of(target)
    if s and s['name'] == SECTION and img.read(target, HELPER_SIZE) == helper(target):
        return 'applied', dict(helperVA=hex(target))
    return 'incompatible', dict(reason=f'接收门调用目标变为 {target:#x}，既不是原接收门也不是本补丁')


def apply(data):
    """在 bytearray 上打补丁，返回改动的文件偏移区间 [(start, end, 说明)]。PE 校验和由调用方重算。"""
    img = Image(data)
    s, helper_va = _slot(img)
    code = helper(helper_va)
    helper_at = s['raw'] + helper_va - IMAGE_BASE - s['rva']
    call_at = img.offset(CALL_VA, 5)
    data[helper_at:helper_at + len(code)] = code
    data[call_at + 1:call_at + 5] = rel32(CALL_VA, 5, helper_va)
    vsize_at = s['header'] + 8
    struct.pack_into('<I', data, vsize_at, helper_va - IMAGE_BASE - s['rva'] + len(code))
    return [(call_at + 1, call_at + 5, 'call 改指向辅助代码'),
            (helper_at, helper_at + len(code), f'辅助代码 {len(code)} 字节'),
            (vsize_at, vsize_at + 4, '.bp0 虚拟大小')]
