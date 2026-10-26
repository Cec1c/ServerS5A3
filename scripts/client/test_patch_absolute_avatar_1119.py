"""核对1119分类门、实际调用栈与原生分流；真实EXE仅作私有只读夹具。"""
import json
import os
from pathlib import Path
import struct
import tempfile
import unittest

import patch_absolute_avatar_1119 as patch

try:
    from unicorn import Uc, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE, UC_HOOK_MEM_UNMAPPED
    from unicorn.x86_const import (UC_X86_REG_EAX, UC_X86_REG_EBP, UC_X86_REG_EBX,
                                  UC_X86_REG_ECX, UC_X86_REG_EDI, UC_X86_REG_EIP,
                                  UC_X86_REG_ESI, UC_X86_REG_ESP)
except ImportError:
    Uc = None

if os.environ.get('DNF_ABSOLUTE_1119_INPUT_EXE') and Uc is None:
    raise RuntimeError('提供真实EXE夹具时必须安装可用的unicorn，不能跳过实际分流验证')

STACK, PACKET, STOP = 0x10000800, 0x30000000, 0x20000000


class InputTests(unittest.TestCase):
    def test_invalid_input_is_rejected_before_address_access(self):
        for data in (b'', b'MZ', bytes(1024)):
            with self.subTest(size=len(data)), self.assertRaises(ValueError):
                patch.build(data)

    @unittest.skipIf(Uc is None, '分类机器码验证需要unicorn')
    def test_helper_forwards_original_argument_and_preserves_abi(self):
        vm = Uc(UC_ARCH_X86, UC_MODE_32)
        vm.mem_map(patch.FALLBACK_VA & ~4095, 4096)
        vm.mem_write(patch.HELPER_VA, patch.helper())
        # 原分类链的替身仅返回收到的完整参数，用于检查转交及ret 4。
        vm.mem_write(patch.FALLBACK_VA, bytes.fromhex('8b442404c20400'))
        vm.mem_map(STACK & ~4095, 4096)
        vm.mem_map(PACKET, 4096)
        vm.mem_map(STOP, 4096)
        saved = {UC_X86_REG_EDI: PACKET, UC_X86_REG_ESI: 0x12345678,
                 UC_X86_REG_EBX: 0x23456789, UC_X86_REG_EBP: 0x34567890,
                 UC_X86_REG_ECX: 0x456789ab}
        for packet_class in (0, 1, 2, 3, 127, 255):
            for opcode in (0, 903, 1118, 1119, 1120, 65535):
                argument = 0xa5a50000 | opcode
                vm.mem_write(PACKET, bytes([packet_class]))
                vm.mem_write(STACK, struct.pack('<II', STOP, argument))
                vm.reg_write(UC_X86_REG_ESP, STACK)
                for reg, value in saved.items():
                    vm.reg_write(reg, value)
                vm.emu_start(patch.HELPER_VA, STOP, count=40)
                expected = 1 if packet_class == 1 and opcode == 1119 else argument
                self.assertEqual(vm.reg_read(UC_X86_REG_EAX), expected)
                self.assertEqual(vm.reg_read(UC_X86_REG_ESP), STACK + 8)
                for reg, value in saved.items():
                    self.assertEqual(vm.reg_read(reg), value)


class NativeRoute:
    """执行真实接收许可链；仅跳过后续正文解码，在原生分类分派入口停止。"""
    def __init__(self, image):
        self.image, self.layout = image, patch.pe.parse(image)
        self.vm, self.pages, self.seen = Uc(UC_ARCH_X86, UC_MODE_32), set(), None
        self.vm.hook_add(UC_HOOK_MEM_UNMAPPED, self.missing)
        self.vm.hook_add(UC_HOOK_CODE, self.code)
        for va in (0x10000000, 0x10001000, STOP, PACKET):
            self.put(va, bytes(4096))
        packet = bytearray(33)
        struct.pack_into('<I', packet, 3, 33)
        self.vm.mem_write(PACKET, bytes(packet))

    def put(self, va, data):
        for page in range(va & ~4095, (va + len(data) + 4095) & ~4095, 4096):
            if page not in self.pages:
                self.vm.mem_map(page, 4096)
                self.pages.add(page)
        self.vm.mem_write(va, data)

    def missing(self, vm, access, va, size, value, user):
        page = va & ~4095
        try:
            start = patch.offset(self.layout, page, 4096)
        except ValueError:
            return False
        self.put(page, self.image[start:start + 4096])
        return True

    def code(self, vm, va, size, user):
        if va == 0x2523a30:
            vm.reg_write(UC_X86_REG_EIP, 0x2523c8a)
        elif va in (0x2523cf6, 0x2523e95, 0x2523f50, 0x2523cad):
            self.seen = {0x2523cf6: 'class1', 0x2523e95: 'class0',
                         0x2523f50: '未分派', 0x2523cad: '分类拒绝'}[va]
            vm.emu_stop()

    def prepare(self, packet_class, opcode, saved=None):
        self.seen = None
        self.vm.mem_write(PACKET, struct.pack('<BH', packet_class, opcode))
        values = {UC_X86_REG_EDI: PACKET, UC_X86_REG_EBP: 0x10001000,
                  UC_X86_REG_ESP: STACK, UC_X86_REG_EBX: 0, UC_X86_REG_ESI: 0,
                  UC_X86_REG_ECX: opcode}
        values.update(saved or {})
        for reg, value in values.items():
            self.vm.reg_write(reg, value)

    def route(self, packet_class, opcode):
        self.prepare(packet_class, opcode)
        self.vm.emu_start(0x2523a05, STOP, count=1000)
        if self.seen is None:
            raise AssertionError('实际接收链未到达预期分流点')
        return self.seen


@unittest.skipUnless(os.environ.get('DNF_ABSOLUTE_1119_INPUT_EXE'), '需提供固定V162私有输入夹具')
class FrozenImageTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.source = Path(os.environ['DNF_ABSOLUTE_1119_INPUT_EXE'])
        cls.before = cls.source.read_bytes()
        cls.after, cls.receipt = patch.build(cls.before)

    def test_exact_output_ranges_checksum_and_reader_preservation(self):
        self.assertEqual(patch.sha(self.after), patch.OUTPUT_SHA256)
        self.assertEqual(len(self.before), len(self.after))
        normalized = bytearray(self.after)
        for first, last in self.receipt['original_ranges']:
            normalized[first:last] = self.before[first:last]
        self.assertEqual(normalized, self.before)
        layout = patch.pe.parse(self.after)
        self.assertEqual(len(layout['sections']), 44)
        self.assertEqual(patch.pe.discontinuities(layout), [])
        checksum = layout['opt'] + 64
        self.assertEqual(struct.unpack_from('<I', self.after, checksum)[0],
                         patch.pe.checksum(self.after, checksum))
        for name, va, size, digest in patch.NATIVE_ANCHORS:
            first = patch.offset(layout, va, size)
            self.assertEqual(self.before[first:first + size], self.after[first:first + size], name)
            self.assertEqual(patch.sha(self.after[first:first + size]), digest)

    @unittest.skipIf(Uc is None, '实际分流验证需要unicorn')
    def test_all_u16_actual_classification_decisions(self):
        before, after = NativeRoute(self.before), NativeRoute(self.after)
        decisions = {}
        for packet_class in (0, 1, 2, 255):
            changed = []
            for opcode in range(65536):
                a, b = before.route(packet_class, opcode), after.route(packet_class, opcode)
                if a != b:
                    self.assertEqual((a, b), ('未分派', 'class1'))
                    changed.append(opcode)
            self.assertEqual(changed, [1119] if packet_class == 1 else [])
            decisions[str(packet_class)] = changed
            print('已完成实际分类'+str(packet_class)+'全部16位编号对照', flush=True)
        result = dict(通过=True, 分类编号对照数=262144, 实际执行数=524288,
                      唯一行为变化=decisions, 正文解码与业务读者已执行=False,
                      完整游戏界面验收=False)
        if os.environ.get('DNF_ABSOLUTE_1119_GATE_RECEIPT'):
            Path(os.environ['DNF_ABSOLUTE_1119_GATE_RECEIPT']).write_text(
                json.dumps(result, ensure_ascii=False, indent=2)+'\n', encoding='utf-8')

    @unittest.skipIf(Uc is None, '真实调用ABI验证需要unicorn')
    def test_actual_callsite_stack_and_callee_saved_registers(self):
        for image in (self.before, self.after):
            route = NativeRoute(image)
            saved = {UC_X86_REG_EDI: PACKET, UC_X86_REG_ESI: 0x12345678,
                     UC_X86_REG_EBX: 0x23456789, UC_X86_REG_EBP: 0x10001000}
            for packet_class in (0, 1, 2, 255):
                for opcode in (0, 903, 1118, 1119, 1120, 65535):
                    route.prepare(packet_class, opcode, saved)
                    route.vm.emu_start(patch.FRAGMENT_VA, patch.FRAGMENT_END, count=300)
                    self.assertEqual(route.vm.reg_read(UC_X86_REG_EIP), patch.FRAGMENT_END)
                    self.assertEqual(route.vm.reg_read(UC_X86_REG_ESP), STACK)
                    for reg, value in saved.items():
                        self.assertEqual(route.vm.reg_read(reg), value)

    def test_wrong_image_repeat_and_existing_output_rejected(self):
        for data in (self.after, self.before[:-1] + bytes([self.before[-1] ^ 1])):
            with self.assertRaises(ValueError):
                patch.build(data)
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory) / 'DNF.exe'
            target.write_bytes(b'preserve')
            with self.assertRaises(ValueError):
                patch.write_candidate(self.source, target)
            self.assertEqual(target.read_bytes(), b'preserve')
        with self.assertRaises(ValueError):
            patch.write_candidate(self.source, self.source)
        self.assertEqual(patch.sha(self.source.read_bytes()), patch.INPUT_SHA256)

    @unittest.skipUnless(os.name == 'nt', 'Windows映像验证需要Windows')
    def test_windows_accepts_both_images_without_execution(self):
        self.assertTrue(patch.pe.probe_image(self.source)['mappingSucceeded'])
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory) / 'DNF.exe'
            target.write_bytes(self.after)
            probe = patch.pe.probe_image(target)
            self.assertTrue(probe['mappingSucceeded'])
            self.assertFalse(probe['executed'])


if __name__ == '__main__':
    unittest.main(verbosity=2)
