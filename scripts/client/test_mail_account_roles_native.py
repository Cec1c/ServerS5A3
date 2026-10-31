"""Native op789 sender and class0/718 parser; cache/widgets/strings are adapters.

Tests stop at the UI row insertion boundary, not a rendered or logged-in game.
The original sender, scalar writers, dispatcher, count loop and tree-forwarder
execute x86 from the exact client. Go fixtures use synthetic accounts only.
"""
import json
import os
from pathlib import Path
import struct
import unittest

from unicorn.x86_const import *
import patch_mail_recipient_default_region as patch
from test_mail_recipient_default_region_native import MailNative


class AccountRolesNative(MailNative):
    TREE, EVENT = 0x2001B000, 0x2001C000

    def __init__(self, data, window=True):
        super().__init__(data)
        self.cache, self.rows, self.cleared = {}, [], []
        self.children = 0
        self.put(self.WINDOW+0x24C, self.TREE)
        self.hooks.update({
            0x1E93430: self.tree_reference,
            0x3873640: lambda: self.ret(4, self.children),
            0x25244A0: lambda: self.ret(value=self.NETWORK),
            0x2524210: lambda: self.ret(value=self.NETWORK),
            0x2523FF0: lambda: self.ret(4, 0x2000C000),
            0x2321490: lambda: self.ret(),
            0x29EC690: self.clear_region,
            0x29EC670: self.add_cached_role,
            0x32B5C90: lambda: self.ret(8, self.WINDOW if window else 0),
            0x2534710: lambda: self.ret(value=self.NAME),
            0x38759B0: self.insert_row,
        })

    def tree_reference(self):
        # Shared-pointer and widget lookup only; sender decisions remain native.
        at = self.reg(UC_X86_REG_ECX)
        self.put(at, self.TREE, 0)
        self.ret(4, at)

    def request_roles(self, region, populated=False):
        self.children = int(populated)
        self.put(self.EVENT, 123, region, self.WINDOW)
        self.u.mem_write(self.EVENT+0x14, b'\1')
        self.run(0x29DB230, (self.EVENT,), pop=0)
        return self.sent

    def clear_region(self):
        region = self.arg()
        self.cleared.append(region)
        self.cache[region] = []
        self.ret()

    def add_cached_role(self):
        region, name, special = self.arg(), self.wide(self.arg(1)).decode('utf-16le'), self.arg(2)
        self.cache.setdefault(region, []).append((name, special))
        self.ret()

    def insert_row(self):
        assert self.reg(UC_X86_REG_ECX) == self.TREE
        self.rows.append((self.arg(3), self.wide(self.arg(1)).decode('utf-16le')))
        self.assert_row_flags = (self.arg(), self.arg(4), self.arg(5))
        self.ret(24)

    def receive_roles(self, body):
        wire = bytearray(16)+body
        struct.pack_into('<H', wire, 1, 718)
        struct.pack_into('<I', wire, 3, len(wire))
        self.u.mem_write(self.PACKET, bytes(wire))
        self.put(0x2000C000, 0x2032F40, 0)
        self.run(0x25239A0, (0, self.PACKET, 0), ecx=self.NETWORK)
        return self.read(0x57F9AD4)


def list_body(region, names):
    body = bytearray([region, len(names)])
    for number, name in enumerate(names, 100):
        raw = name.encode('gb18030')
        body.extend(struct.pack('<II', number, len(raw))+raw+b'\0')
    return bytes(body)


class MailAccountRolesTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        name = os.environ.get('DNF_MAIL_RECIPIENT_CLIENT')
        if not name:
            raise unittest.SkipTest('requires exact reviewed cumulative client before mail patch')
        cls.before = Path(name).read_bytes()
        cls.after, _ = patch.build(cls.before, dict(inputSha256=patch.pe.sha(cls.before), inputSize=len(cls.before)))

    def test_original_dispatcher_blocks_account_list(self):
        lab = AccountRolesNative(self.before)
        lab.receive_roles(list_body(0, ['来来来']))
        self.assertEqual((lab.cache, lab.rows, lab.cleared), ({}, [], []))

    def test_original_sender_emits_real_server_id_and_avoids_populated_refetch(self):
        for region in (0, 1, 118, 249):
            lab = AccountRolesNative(self.after)
            self.assertEqual(lab.request_roles(region), [(789, bytes([region]))])
            lab = AccountRolesNative(self.after)
            self.assertEqual(lab.request_roles(region, populated=True), [])

    def test_native_list_parser_and_tree_row_forwarder(self):
        for region in (0, 1, 118, 249):
            lab = AccountRolesNative(self.after)
            self.assertEqual(lab.receive_roles(list_body(region, ['来来来', 'Test角色'])), 0)
            self.assertEqual(lab.cleared, [region])
            self.assertEqual(lab.cache[region], [('来来来', 0), ('Test角色', 0)])
            self.assertEqual(lab.rows, [(region, '来来来'), (region, 'Test角色')])
            self.assertEqual(lab.assert_row_flags, (0, 0xFFFFFFFF, 1))

    def test_empty_repeat_and_other_region_do_not_keep_stale_names(self):
        lab = AccountRolesNative(self.after)
        lab.receive_roles(list_body(0, ['旧角色']))
        lab.receive_roles(list_body(1, ['另一角色']))
        lab.receive_roles(list_body(0, ['新角色']))
        self.assertEqual(lab.cache, {0: [('新角色', 0)], 1: [('另一角色', 0)]})
        self.assertEqual(lab.receive_roles(list_body(0, [])), 0)
        self.assertEqual(lab.cache, {0: [], 1: [('另一角色', 0)]})

    def test_closed_dropdown_updates_cache_without_widget_access(self):
        lab = AccountRolesNative(self.after, window=False)
        self.assertEqual(lab.receive_roles(list_body(0, ['来来来'])), 0)
        self.assertEqual(lab.cache, {0: [('来来来', 0)]})
        self.assertEqual(lab.rows, [])

    def test_go_handler_fixtures_consumed_by_native_parser(self):
        directory = os.environ.get('DNF_MAIL_ACCOUNT_ROLES_NATIVE_DIR')
        if not directory:
            self.skipTest('set fixture export directory for actual Go handler replies')
        files = sorted(Path(directory).glob('*-718.bin'))
        self.assertEqual(len(files), 4)
        proofs = []
        for file in files:
            body = file.read_bytes()
            lab = AccountRolesNative(self.after)
            self.assertEqual(lab.receive_roles(body), 0)
            self.assertEqual(lab.cache[body[0]], [('来来来', 0)])
            self.assertEqual(lab.rows, [(body[0], '来来来')])
            proofs.append(dict(file=file.name, body=body.hex(), rows=lab.rows))
        Path(directory, 'native-account-roles.json').write_text(json.dumps(proofs, indent=2, ensure_ascii=False))


if __name__ == '__main__':
    unittest.main()
