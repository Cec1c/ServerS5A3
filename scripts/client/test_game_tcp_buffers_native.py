"""Offline native checks for the exact V209 game TCP buffer candidate.

The fixture executes the actual image instructions in 0x38c8250. Only external
Winsock calls and log leaves are replaced so their arguments can be observed.
"""
import hashlib
import json
import os
from pathlib import Path
import struct
import tempfile
import unittest
from unittest import mock

from unicorn import x86_const as X

import patch_game_tcp_buffers as patch
from patch_receivers_v128 import headers
from test_upgrade_equipped_warning_native import Native


CLIENT_ENV = 'DNF_GAME_TCP_BUFFER_CLIENT'


@unittest.skipUnless(os.environ.get(CLIENT_ENV), 'exact V209 input required')
class GameTcpBufferTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.source = Path(os.environ[CLIENT_ENV])
        cls.before = cls.source.read_bytes()
        cls.after, cls.receipt = patch.build(cls.before)

    @classmethod
    def execute_connect(cls, image):
        n = Native(image)
        n.ensure(patch.CONNECT_FUNCTION_VA, patch.CONNECT_FUNCTION_SIZE)

        manager = 0x11000000
        address_object = 0x11001000
        window_handle = 0x11002000
        ip_text = 0x11003000
        log_text = 0x11004000
        n.ensure(manager, 0x5000)
        n.u.mem_write(manager, bytes(0x5000))
        n.u.mem_write(manager + 4, b'\x01')
        n.put(manager + 0xC, window_handle)
        n.put(manager + 0x1C0, 0)
        n.put(manager + 0x1C4, 0xFFFFFFFF)
        n.u.mem_write(address_object, bytes(range(0x20)))
        n.u.mem_write(window_handle, b'X' * 0x464)
        n.u.mem_write(ip_text, b'10.20.30.40\x00')

        stubs = {
            'socket': 0x13100100,
            'last_error': 0x13100200,
            'setsockopt': 0x13100300,
            'async_select': 0x13100400,
            'htons': 0x13100500,
            'inet_addr': 0x13100600,
            'connect': 0x13100700,
        }
        for address in stubs.values():
            n.ensure(address, 1)
            n.u.mem_write(address, b'\xC3')

        pointer_slots = {
            0x45886F0: stubs['socket'],
            0x45886B4: stubs['last_error'],
            0x45886BC: stubs['setsockopt'],
            0x45886C0: stubs['async_select'],
            0x4588694: stubs['htons'],
            0x45886B8: stubs['inet_addr'],
            0x4588698: stubs['connect'],
        }
        for slot, address in pointer_slots.items():
            n.put(slot, address)

        records = {
            'socket': [],
            'setsockopt': [],
            'async_select': [],
            'htons': [],
            'address_object': [],
            'inet_text': [],
            'connect': [],
        }
        socket_handle = 0x321
        port = 0x1234
        address_value = 0x01020304

        def socket_call():
            records['socket'].append(tuple(n.arg(i) for i in range(3)))
            n.ret(socket_handle, 12)

        def last_error():
            n.ret(10000)

        def setsockopt_call():
            sock, level, option = (n.arg(i) for i in range(3))
            value_pointer, value_length = n.arg(3), n.arg(4)
            value = struct.unpack('<I', n.u.mem_read(value_pointer, 4))[0]
            records['setsockopt'].append(
                (sock, level, option, value, value_length)
            )
            n.ret(0, 20)

        def async_select_call():
            records['async_select'].append(tuple(n.arg(i) for i in range(4)))
            n.ret(0x464, 16)

        def htons_call():
            value = n.arg()
            records['htons'].append(value)
            n.ret(((value & 0xFF) << 8) | (value >> 8), 4)

        def inet_addr_call():
            text_pointer = n.arg()
            records['inet_text'].append(bytes(n.u.mem_read(text_pointer, 12)))
            n.ret(address_value, 4)

        def connect_call():
            sockaddr_pointer = n.arg(1)
            records['connect'].append((
                n.arg(0),
                bytes(n.u.mem_read(sockaddr_pointer, 16)),
                n.arg(2),
            ))
            n.ret(0, 12)

        def copy_leaf():
            destination, size, source = n.arg(0), n.arg(1), n.arg(2)
            n.u.mem_write(destination, bytes(n.u.mem_read(source, size)))
            n.ret(0, 12)

        def address_string():
            records['address_object'].append(n.arg())
            n.ret(ip_text)

        def preserve_exit_value():
            value = n.u.reg_read(X.UC_X86_REG_EAX)
            n.ret(value)

        n.hooks.update({
            stubs['socket']: socket_call,
            stubs['last_error']: last_error,
            stubs['setsockopt']: setsockopt_call,
            stubs['async_select']: async_select_call,
            stubs['htons']: htons_call,
            stubs['inet_addr']: inet_addr_call,
            stubs['connect']: connect_call,
            0xC38670: copy_leaf,
            0x37E0A70: address_string,
            0x37DE240: lambda: n.ret(log_text),
            0x37DD6C0: lambda: n.ret(),
            0x3E72CE4: preserve_exit_value,
        })

        saved = {
            X.UC_X86_REG_EBX: 0x12345678,
            X.UC_X86_REG_ESI: 0x23456789,
            X.UC_X86_REG_EDI: 0x3456789A,
        }
        n.put(n.STACK, n.STOP, address_object, port)
        n.u.reg_write(X.UC_X86_REG_ESP, n.STACK)
        n.u.reg_write(X.UC_X86_REG_EBP, n.STACK + 0x1000)
        n.u.reg_write(X.UC_X86_REG_ECX, manager)
        for register, value in saved.items():
            n.u.reg_write(register, value)
        n.u.emu_start(patch.CONNECT_FUNCTION_VA, 0, count=100000)

        return {
            'records': records,
            'eip': n.u.reg_read(X.UC_X86_REG_EIP),
            'esp': n.u.reg_read(X.UC_X86_REG_ESP),
            'ebp': n.u.reg_read(X.UC_X86_REG_EBP),
            'eax': n.u.reg_read(X.UC_X86_REG_EAX),
            'saved': {register: n.u.reg_read(register)
                      for register in saved},
            'socket_field': n.read(manager + 0x1C4),
            'port_field': n.read(manager + 0x1C0),
            'address_field': bytes(n.u.mem_read(manager + 0x1A0, 0x20)),
            'manager': manager,
            'window_handle': window_handle,
            'port': port,
        }

    def test_native_path_uses_1024_then_262144_and_preserves_connection(self):
        before = self.execute_connect(self.before)
        after = self.execute_connect(self.after)

        expected_layout = {
            'eip': Native.STOP,
            'esp': Native.STACK + 12,
            'ebp': Native.STACK + 0x1000,
            'eax': 1,
            'saved': {
                X.UC_X86_REG_EBX: 0x12345678,
                X.UC_X86_REG_ESI: 0x23456789,
                X.UC_X86_REG_EDI: 0x3456789A,
            },
            'socket_field': 0x321,
            'port_field': 0x1234,
            'address_field': bytes(range(0x20)),
        }
        for result in (before, after):
            for key, value in expected_layout.items():
                self.assertEqual(result[key], value, key)
            self.assertEqual(result['records']['socket'], [(2, 1, 6)])

        self.assertEqual(
            [call[1:] for call in before['records']['setsockopt']],
            [(0xFFFF, 0x1001, 1024, 4),
             (0xFFFF, 0x1002, 1024, 4),
             (6, 1, 1, 4)],
        )
        self.assertEqual(
            [call[1:] for call in after['records']['setsockopt']],
            [(0xFFFF, 0x1001, 262144, 4),
             (0xFFFF, 0x1002, 262144, 4),
             (6, 1, 1, 4)],
        )

        unchanged_keys = ('socket', 'async_select', 'htons', 'address_object',
                          'inet_text', 'connect', 'socket_field', 'port_field',
                          'address_field', 'eip', 'esp', 'ebp', 'eax', 'saved')
        for key in unchanged_keys:
            if key in before['records']:
                self.assertEqual(before['records'][key], after['records'][key], key)
            else:
                self.assertEqual(before[key], after[key], key)

        self.assertEqual(before['records']['async_select'], [
            (0x321, before['window_handle'], 0x464, 0x10)
        ])
        self.assertEqual(before['records']['htons'], [0x1234])
        self.assertEqual(before['records']['address_object'], [0x110001A0])
        self.assertEqual(before['records']['inet_text'], [b'10.20.30.40\x00'])
        self.assertEqual(before['records']['connect'], [(
            0x321,
            bytes.fromhex('02001234040302010000000000000000'),
            16,
        )])

    def test_only_buffer_immediate_and_checksum_change(self):
        self.assertEqual(len(self.before), len(self.after))
        self.assertEqual(headers(self.before), headers(self.after))
        instruction = self.receipt['buffer_instruction_file_offset']
        checksum_offset = headers(self.before)[0] + 64
        changed = {
            index for index, (before, after)
            in enumerate(zip(self.before, self.after)) if before != after
        }
        self.assertEqual(
            changed - set(range(checksum_offset, checksum_offset + 4)),
            {instruction + 2, instruction + 3},
        )
        self.assertTrue(changed.intersection(range(checksum_offset,
                                                   checksum_offset + 4)))
        self.assertEqual(self.after[instruction:instruction + 5],
                         patch.BUFFER_INSTRUCTION_AFTER)
        self.assertEqual(self.receipt['changed_ranges'], [
            [instruction + 2, instruction + 4],
            [checksum_offset, checksum_offset + 4],
        ])
        checksum_value = struct.unpack_from('<I', self.after,
                                            checksum_offset)[0]
        self.assertEqual(checksum_value,
                         patch.checksum(self.after, checksum_offset))

    def test_native_output_is_deterministic_and_separate(self):
        rebuilt, receipt = patch.build(self.before)
        self.assertEqual(rebuilt, self.after)
        self.assertEqual(receipt, self.receipt)
        self.assertEqual(self.source.read_bytes(), self.before)

        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory) / 'DNF.exe'
            output = Path(directory) / 'DNF-tcp-256k.exe'
            source.write_bytes(self.before)
            written = patch.write_candidate(source, output)
            self.assertEqual(written, self.receipt)
            self.assertEqual(source.read_bytes(), self.before)
            self.assertEqual(output.read_bytes(), self.after)

            preserved = Path(directory) / 'existing.exe'
            preserved.write_bytes(b'preserve')
            with self.assertRaises(ValueError):
                patch.write_candidate(source, preserved)
            self.assertEqual(preserved.read_bytes(), b'preserve')
            with self.assertRaises(ValueError):
                patch.write_candidate(source, source)

    def test_wrong_identity_and_repeat_application_are_rejected(self):
        bad_inputs = (
            b'',
            self.before[:-1],
            self.before[:-1] + bytes([self.before[-1] ^ 1]),
            self.after,
        )
        for bad in bad_inputs:
            with self.assertRaises(ValueError):
                patch.build(bad)

    def test_exact_raw_byte_gates_reject_injected_variants(self):
        variants = (
            (patch.DIRECT_CALL_VA, b'\x90', 'connection entry call'),
            (patch.SOCKET_SEQUENCE_VA, b'\x07', 'AF_INET'),
            (patch.SEND_BUFFER_SEQUENCE_VA + 2, b'\x01', 'send-buffer'),
            (patch.RECEIVE_BUFFER_SEQUENCE_VA + 2, b'\x01', 'receive-buffer'),
            (patch.TCP_NODELAY_SEQUENCE_VA + 2, b'\x01', 'TCP_NODELAY'),
        )
        for va, replacement, message in variants:
            variant = bytearray(self.before)
            file_offset = patch.offset(headers(self.before)[1], va, 1)
            variant[file_offset:file_offset + len(replacement)] = replacement
            variant = bytes(variant)
            with mock.patch.object(patch, 'INPUT_SHA256',
                                   hashlib.sha256(variant).hexdigest()):
                with self.assertRaisesRegex(ValueError, message):
                    patch.build(variant)

    def test_receipt_is_json_serializable_and_does_not_claim_online_work(self):
        encoded = json.dumps(self.receipt)
        decoded = json.loads(encoded)
        self.assertEqual(
            decoded['output_sha256'],
            hashlib.sha256(self.after).hexdigest(),
        )
        self.assertEqual(decoded['input_size'], patch.INPUT_SIZE)
        self.assertEqual(decoded['output_size'], patch.INPUT_SIZE)
        self.assertTrue(decoded['length_unchanged'])
        self.assertEqual(decoded['scope'], 'client_exe_only')
        self.assertFalse(decoded['online_verified'])


if __name__ == '__main__':
    unittest.main(verbosity=2)
