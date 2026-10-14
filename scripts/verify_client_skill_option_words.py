#!/usr/bin/env python3
"""Offline layout check for the per-character option words used by dnfbridge.

The bridge stores and replays the 512-byte per-character option frame (op447 C2S /
class0/390 S2C) verbatim and only decodes two field groups for logs and tests: the
skill lock list (words 128..191 tree0, 192..255 tree1) and the CD notify list (words
2..21 tree0, 22..41 tree1).

This script proves, offline and read-only, that the client functions behind those
indices still have the same shape in the client now shipped: every function is
located (same VA first, else signature search), the preserved analysis byte slice is
compared byte-for-byte, and the required instructions plus call targets are checked
with capstone. Exit code 0 means the current client passed every check.

Usage:
  verify_client_skill_option_words.py --client <DNF.exe> [--reference <analysed DNF.exe>]
                                      [--slices-dir <dir>] [--json <out.json>]
"""
import argparse
import hashlib
import json
import struct
import sys
from pathlib import Path

EMULATOR_DEPS = 'H:/86JP启动器/_dist/selftest/new-exe-loader-ipc-20261003/emulator-deps'

# name: (analysed VA, required instruction substrings, required call targets)
TARGETS = [
    ('option_word_write_raw', 0x39A6B80,
     ['cmp eax, 0x7f', 'mov word ptr [ecx + eax*2], dx'], []),
    ('option_word_write_bank', 0x39A6BA0,
     ['shl esi, 6', 'cmp esi, 0x80', 'mov word ptr [ecx + eax*2], dx', 'add eax, 2'], []),
    ('option_word_read_raw', 0x39A6BE0,
     ['cmp eax, 0x7f', 'mov ax, word ptr [ecx + eax*2]'], []),
    ('option_word_read_bank', 0x39A6C00,
     ['shl esi, 6', 'cmp esi, 0x80', 'mov ax, word ptr [ecx + eax*2]', 'add eax, 2'], []),
    ('option_lock_table_clear_upper', 0x39A6B40,
     ['push 0x100', 'add ecx, 0x100', 'push -1'], []),
    ('option_lock_table_export', 0x2255AA0,
     ['add ecx, 0x1cc'],
     ['option_word_write_raw', 'option_word_write_bank', 'option_lock_table_clear_upper',
      'character_option_c2s447_writer']),
    ('option_cd_list_export', 0x22555F0,
     ['mov dword ptr [ebp - 0xa4], 2', 'mov dword ptr [ebp - 8], 0x29',
      'add dword ptr [ebp - 0xa8], 0x14', 'cmp ebx, 0x14'],
     ['option_word_write_raw', 'character_option_c2s447_writer']),
    ('option_lock_table_restore', 0x2255880, [], []),
    ('option_cd_list_restore', 0x2254FC0, [], []),
    ('character_option_s2c390_reader', 0x2255DB0,
     ['cmp dword ptr [ebp - 4], 0x200', 'lea esi, [edi + 0x1cc]', 'push 0x200',
      'push 0x2a', 'push 0x2b'],
     ['option_lock_table_restore', 'option_cd_list_restore']),
    ('character_option_c2s447_writer', 0x2255350,
     ['push 0x1bf', 'push 0x200', 'add esi, 0x1cc'], []),
]


def parse_pe(data):
    e = struct.unpack_from('<I', data, 0x3C)[0]
    if data[e:e + 4] != b'PE' + bytes(2):
        raise SystemExit('not a PE image')
    opt = e + 24
    base = struct.unpack_from('<I', data, opt + 28)[0]
    count = struct.unpack_from('<H', data, e + 6)[0]
    table = opt + struct.unpack_from('<H', data, e + 20)[0]
    sections = []
    for index in range(count):
        entry = table + 40 * index
        sections.append({
            'name': data[entry:entry + 8].rstrip(bytes(1)).decode('latin1'),
            'va': base + struct.unpack_from('<I', data, entry + 12)[0],
            'size': struct.unpack_from('<I', data, entry + 16)[0],
            'raw': struct.unpack_from('<I', data, entry + 20)[0],
            'flags': struct.unpack_from('<I', data, entry + 36)[0],
        })
    return base, sections


def image_bytes(data, sections, va, size):
    for section in sections:
        if section['va'] <= va < section['va'] + section['size']:
            offset = section['raw'] + va - section['va']
            return data[offset:offset + size]
    return None


def signature_search(data, sections, needle):
    hits = []
    for section in sections:
        if not section['flags'] & 0x20000000:
            continue
        start, end = section['raw'], section['raw'] + section['size']
        pos = data.find(needle, start, end)
        while pos >= 0:
            hits.append(pos - start + section['va'])
            pos = data.find(needle, pos + 1, end)
    return hits


def common_prefix(left, right):
    if not left or not right:
        return 0
    limit = min(len(left), len(right))
    index = 0
    while index < limit and left[index] == right[index]:
        index += 1
    return index


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--client', required=True)
    parser.add_argument('--reference')
    parser.add_argument('--slices-dir')
    parser.add_argument('--json')
    parser.add_argument('--emulator-deps', default=EMULATOR_DEPS,
                        help='directory holding the capstone package')
    args = parser.parse_args()

    sys.path.insert(0, args.emulator_deps)
    from capstone import Cs, CS_ARCH_X86, CS_MODE_32
    md = Cs(CS_ARCH_X86, CS_MODE_32)

    client = Path(args.client)
    data = client.read_bytes()
    base, sections = parse_pe(data)
    reference = Path(args.reference).read_bytes() if args.reference else None
    reference_sections = parse_pe(reference)[1] if reference else None
    slices = {}
    if args.slices_dir:
        for path in Path(args.slices_dir).glob('*.bin'):
            slices[int(path.stem, 16)] = path.read_bytes()

    receipt = {'client': str(client), 'client_sha256': hashlib.sha256(data).hexdigest(),
               'client_size': len(data), 'image_base': hex(base), 'targets': []}
    if reference is not None:
        receipt['reference'] = str(args.reference)
        receipt['reference_sha256'] = hashlib.sha256(reference).hexdigest()
        receipt['reference_size'] = len(reference)

    located, rows = {}, []
    for name, va, checks, calls in TARGETS:
        needle = None
        if reference_sections is not None:
            needle = image_bytes(reference, reference_sections, va, 96)
        if not needle and va in slices:
            needle = slices[va]
        row = {'name': name, 'analysed_va': hex(va)}
        if not needle:
            row['status'] = 'no_reference_bytes'
            rows.append(row)
            continue
        same_va = image_bytes(data, sections, va, len(needle))
        if same_va is not None and common_prefix(needle, same_va) >= 16:
            located[name], row['located_by'] = va, 'same_va'
        else:
            found = None
            for length in (32, 24, 16, 12):
                hits = signature_search(data, sections, needle[:length])
                if len(hits) == 1:
                    found = hits[0]
                    break
            if found is None:
                row['status'] = 'not_found'
                rows.append(row)
                continue
            located[name], row['located_by'] = found, 'signature'
        row['client_va'] = hex(located[name])
        live = image_bytes(data, sections, located[name], len(needle)) or bytes()
        row['prefix_match'] = common_prefix(needle, live)
        row['reference_bytes'] = len(needle)
        if va in slices:
            slice_bytes = image_bytes(data, sections, located[name], len(slices[va])) or bytes()
            row['slice_bytes'] = len(slices[va])
            row['slice_match'] = common_prefix(slices[va], slice_bytes)
        code = image_bytes(data, sections, located[name], 0x600) or bytes()
        text = ' ; '.join('%s %s' % (insn.mnemonic, insn.op_str) for insn in md.disasm(code, located[name]))
        row['missing_checks'] = [check for check in checks if check not in text]
        row['_text'] = text
        rows.append(row)

    for row in rows:
        text = row.pop('_text', None)
        if text is None:
            continue
        calls = next(calls for name, va, checks, calls in TARGETS if name == row['name'])
        missing_calls = []
        for call in calls:
            target = located.get(call)
            if target is None or ('call ' + hex(target)) not in text:
                missing_calls.append(call + ':' + (hex(target) if target else 'unlocated'))
        row['missing_calls'] = missing_calls
        slice_ok = ('slice_match' not in row) or row['slice_match'] == row['slice_bytes']
        row['status'] = 'ok' if (not row['missing_checks'] and not missing_calls and slice_ok) else 'mismatch'

    receipt['targets'] = rows
    receipt['all_ok'] = bool(rows) and all(row.get('status') == 'ok' for row in rows)
    if args.json:
        Path(args.json).write_text(json.dumps(receipt, indent=2), encoding='utf-8')
    print('client=' + receipt['client_sha256'] + ' size=' + str(len(data)))
    for row in rows:
        print('%-36s %s' % (row['name'], json.dumps({k: v for k, v in row.items() if k != 'name'}, ensure_ascii=False)))
    print('all_ok=' + str(receipt['all_ok']))
    return 0 if receipt['all_ok'] else 1


if __name__ == '__main__':
    raise SystemExit(main())