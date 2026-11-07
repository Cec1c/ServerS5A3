"""松键复核覆盖全部键盘键：修 WASD/技能键漏收 KEYUP 后一直被当成按下。

游戏在 WM_KEYDOWN 时把 messageCache[VK]（0x065D1D70）置 1、WM_KEYUP 时清 0，
每帧补位循环对 messageCache 非 0 的键执行 `or [ecx+esi+0x10d], 0x80`，
强制当成本帧按下。上游的 `.imeup` 辅助代码在补位前用 GetAsyncKeyState
复核“是否真的按着”，松开了就清掉缓存和 DirectInput 当前帧字节——但它只
检查方向键 0x25..0x28。WASD、字母技能键一旦漏收 KEYUP（输入法、焦点切换），
就会每帧被强制按下：角色一直走、技能反复放。

本补丁只改辅助代码开头的范围判断，其余逻辑原样保留：
    cmp eax, 0x25 ; jb normal   ->  cmp eax, 0x08 ; jb normal
    cmp eax, 0x28 ; ja normal   ->  cmp al, 0xfe ; nop ; ja normal
eax 由 movzx 读出（0..255），`cmp al` 与 `cmp eax` 等价。0x01..0x07 是
鼠标键和 VK_CANCEL，继续走原路径（旧 IAT 包装器只对 VK=1 计数，不受影响）。
仍按住的键 GetAsyncKeyState 最高位为 1，照旧补位，连发插件不受影响。

刻意不做的事：不在 messageCache 为 0 时用 GetAsyncKeyState 去清 DirectInput
字节。游戏的映射表把小键盘 7/8/9/4/5/6/1/2/3/0/. 映射到 HOME/UP/…/DEL，
数字锁定打开时这些键的 GetAsyncKeyState 为 0，那样会让小键盘失灵。

定位依据：按辅助代码的字节特征搜索（地址无关），要求全文件唯一，且分支
目标、返回跳转、补位循环里的挂钩都对得上。
"""
from pe_image import Image, rel32

ID = 'imeup-all-keys'
SUMMARY = '松键复核从方向键扩大到全部键盘键（0x08..0xFE）'

_BODY = ('0f 82 ?? ?? ?? ?? {upper} 0f 87 ?? ?? ?? ?? 8b d8 50 ff 15 ?? ?? ?? ?? '
         '66 a9 00 80 0f 85 ?? ?? ?? ?? c6 83 ?? ?? ?? ?? 00 8b 4c 24 18 31 d2 '
         '38 9a ?? ?? ?? ?? 0f 85 08 00 00 00 c6 84 11 0d 01 00 00 00 42 '
         '81 fa 00 01 00 00 0f 82 df ff ff ff 61 9d e9 ?? ?? ?? ?? '
         '61 9d 80 8c 31 0d 01 00 00 80 e9 ?? ?? ?? ??')
ORIGINAL = '9c 60 83 f8 25 ' + _BODY.format(upper='83 f8 28')
PATCHED = '9c 60 83 f8 08 ' + _BODY.format(upper='3c fe 90')
HELPER_SIZE = 0x6B
NORMAL = 0x5C          # 辅助代码内 “popal; popfd; or …0x80” 的位置
LOWER_AT, UPPER_AT = 4, 0x0B
LOWER = (bytes([0x25]), bytes([0x08]))
UPPER = (bytes.fromhex('83f828'), bytes.fromhex('3cfe90'))


def _check_wiring(img, at):
    """确认匹配到的那段代码真的是补位循环挂进来的辅助代码。"""
    va = img.va(at)
    for branch in (0x05, 0x0E, 0x21):            # jb / ja / jne(按住) 都去 normal
        target = va + branch + 6 + int.from_bytes(img.data[at + branch + 2:at + branch + 6], 'little', signed=True)
        if target != va + NORMAL:
            return f'辅助代码 +{branch:#x} 的分支目标不是 normal'
    resume = img.rel32_target(va + 0x66)         # normal 末尾跳回补位循环
    hook_va = resume - 8                         # 原 8 字节 `or [ecx+esi+0x10d],0x80` 的位置
    if img.read(hook_va, 8) != b'\xe9' + rel32(hook_va, 5, va) + b'\x90' * 3:
        return f'补位循环 {hook_va:#x} 没有挂到 {va:#x}'
    return None


def _locate(img):
    original, patched = img.find(ORIGINAL), img.find(PATCHED)
    if len(original) + len(patched) != 1:
        return None, None, f'辅助代码特征匹配 {len(original)} 处原版、{len(patched)} 处已改版，要求恰好 1 处'
    at, state = (original[0], 'absent') if original else (patched[0], 'applied')
    return at, state, _check_wiring(Image(img.data), at)


def inspect(img):
    at, state, problem = _locate(img)
    if problem:
        return 'incompatible', dict(reason=problem)
    return state, dict(helperVA=hex(img.va(at)))


def apply(data):
    img = Image(data)
    at, state, problem = _locate(img)
    assert state == 'absent' and not problem
    lo, hi = at + LOWER_AT, at + UPPER_AT
    assert bytes(data[lo:lo + 1]) == LOWER[0] and bytes(data[hi:hi + 3]) == UPPER[0]
    data[lo:lo + 1] = LOWER[1]
    data[hi:hi + 3] = UPPER[1]
    return [(lo, lo + 1, '下限 0x25 -> 0x08'), (hi, hi + 3, '上限 cmp eax,0x28 -> cmp al,0xfe; nop')]
