"""Extend pinned V158/V165 story gates to all verified chapter endpoints.

Builds an independent fixed-input EXE. Reuses the existing character-owned
snapshot state and lifecycle invalidation; preserves the original playback.
"""
import argparse
import json
from pathlib import Path
import struct

import pefile
import story_digest_gate as original

SOURCE_SHA256 = 'a751cccfe3fb9f7a3730393fdfb32d0546222b55a5a254fc4f2416f6869fbdf6'
SUPPORTED_SOURCES = frozenset((SOURCE_SHA256,
    'a2f67ad258e78c37d9824e22620748634d9f0eff5adaccabe861a86f7defaddc'))
CATALOG = Path(__file__).with_name('story_digest_chapters.json')
CLONE = 0x06E9F000
OLD_GATE = CLONE + 0x280
OLD_SNAPSHOT = CLONE + 0x400
STATE = 0x06EA0000
GATE_CALL = CLONE + 0x129
Code, align, rel, sha256 = original.Code, original.align, original.rel, original.sha256


def load_catalog(path=CATALOG):
    data = json.loads(Path(path).read_text(encoding='utf-8'))
    if data.get('schema') != 1 or [c['scene'] for c in data['chapters']] != list(range(1, 18)):
        raise ValueError('expected ordered chapters 1 through 17')
    for chapter in data['chapters']:
        ids = chapter['any_completed']
        if not ids or len(set(ids)) != len(ids) or any(type(q) is not int or not 0 < q < 30000 for q in ids):
            raise ValueError('invalid completed-quest alternatives')
    for key, chapters in data['endings'].items():
        if not 900 <= int(key) <= 917 or len(set(chapters)) != len(chapters):
            raise ValueError('invalid ending')
        if any(type(c) is not int or not 1 <= c <= 17 for c in chapters):
            raise ValueError('invalid ending chapter')
    return data


def make_code(code_va, catalog):
    mask_va, snapshot_va, table_va, ending_va = (code_va + o for o in (0x180, 0x200, 0x380, 0x600))
    g = Code(code_va)
    g.emit('9c 60 31d2 8b462c')  # preserve all; target ending -> required mask
    g.branch('e8', mask_va)
    g.emit('09c2 8b7e34 8b4dfc 8d3c8f 8b5e38')
    g.label('loop'); g.emit('3bfb')
    g.branch('0f83', 'check')
    g.emit('8b07'); g.branch('e8', mask_va)
    g.emit('09c2 83c704'); g.branch('e9', 'loop')
    g.label('check'); g.emit('85d2'); g.branch('0f84', 'allow')
    g.emit('a1'); g.imm(STATE)
    g.emit('85c0'); g.branch('0f84', 'deny')
    g.emit('3b45f8'); g.branch('0f85', 'deny')
    g.emit('a1'); g.imm(STATE + 4)
    g.emit('21d0 3bc2'); g.branch('0f85', 'deny')
    g.label('allow'); g.emit('61 9d 837b1400')
    g.branch('0f85', CLONE + 0x146)
    g.branch('e9', CLONE + 0x12f)
    g.label('deny'); g.emit('61 9d')
    g.branch('e9', CLONE + 0x11e)

    # EAX scene/ending ID -> EAX chapter mask. Unknown content cannot acquire
    # permission from a partial set of the 17 verified chapter bits.
    m = Code(mask_va)
    m.emit('85c0'); m.branch('0f84', 'zero')
    m.emit('3d84030000'); m.branch('0f84', 'zero')  # shared blank ending 900
    m.emit('83f801'); m.branch('0f82', 'unknown')
    m.emit('83f811'); m.branch('0f87', 'ending')
    m.emit('8bc8 49 b801000000 d3e0 c3')
    m.label('ending'); m.emit('2d85030000 83f810')
    m.branch('0f87', 'unknown')
    m.emit('8b0485'); m.imm(ending_va); m.emit('c3')
    m.label('unknown'); m.emit('b8ffffffff c3')
    m.label('zero'); m.emit('31c0 c3')

    s = Code(snapshot_va)
    s.emit('55 8bec ff750c ff7508')
    s.branch('e8', 0x02921a50)  # real additive table loader, unchanged
    s.emit('9c 60 c705'); s.imm(STATE); s.imm(0)
    s.emit('8b7508 31d2 bf'); s.imm(table_va)
    s.emit('6a11')  # 17 groups; this private counter is removed before popad
    s.label('group'); s.emit('8b07 8b4f04 83c708')
    s.label('quest'); s.emit('0fb71f 83c702 49 803c1e00')
    s.branch('0f85', 'matched')
    s.emit('85c9'); s.branch('0f85', 'quest')
    s.branch('e9', 'next')
    s.label('matched'); s.emit('09c2 8d3c4f')  # skip remaining alternatives
    s.label('next'); s.emit('ff0c24'); s.branch('0f85', 'group')
    s.emit('83c404 8915'); s.imm(STATE + 4)
    s.emit('8b0d'); s.imm(original.PLAYER_MANAGER)
    s.emit('85c9'); s.branch('0f84', 'done')
    s.branch('e8', 0x0296dd80)
    s.emit('a3'); s.imm(STATE)
    s.label('done'); s.emit('61 9d 8be5 5d c20800')

    table = bytearray()
    for chapter in catalog['chapters']:
        ids = chapter['any_completed']
        table.extend(struct.pack('<II', 1 << (chapter['scene'] - 1), len(ids)))
        table.extend(struct.pack('<' + 'H' * len(ids), *ids))
    endings = b''.join(struct.pack('<I', sum(1 << (c - 1) for c in catalog['endings'][str(i)]))
                       if str(i) in catalog['endings'] else b'\xff' * 4 for i in range(901, 918))
    code = bytearray(0x650)
    for offset, chunk, limit in ((0, g.finish(), 0x180), (0x180, m.finish(), 0x200),
                                  (0x200, s.finish(), 0x380), (0x380, table, 0x600),
                                  (0x600, endings, 0x650)):
        if offset + len(chunk) > limit:
            raise ValueError('chapter gate slot overflow')
        code[offset:offset + len(chunk)] = chunk
    return bytes(code), dict(clone=CLONE, gate=code_va, mask=mask_va, snapshot=snapshot_va,
                            table=table_va, endings=ending_va, state=STATE,
                            clear=CLONE + 0x500, select_success=CLONE + 0x600,
                            return_success=CLONE + 0x680)


def build(data, catalog_path=CATALOG):
    source_sha256 = sha256(data)
    if source_sha256 not in SUPPORTED_SOURCES:
        raise ValueError('expected exact published V158 or V165 input')
    catalog = load_catalog(catalog_path)
    pe = pefile.PE(data=data, fast_load=True)
    if pe.FILE_HEADER.Machine != 0x14c or pe.OPTIONAL_HEADER.ImageBase != 0x400000:
        raise ValueError('expected fixed x86 image')
    if pe.OPTIONAL_HEADER.DllCharacteristics & 0x40 or pe.OPTIONAL_HEADER.DATA_DIRECTORY[5].Size:
        raise ValueError('unexpected relocatable input')
    if pe.OPTIONAL_HEADER.DATA_DIRECTORY[4].Size:
        raise ValueError('unexpected signed input')
    count = len(pe.sections)
    header = pe.sections[0].get_file_offset() + count * 40
    if header + 40 > pe.OPTIONAL_HEADER.SizeOfHeaders or any(data[header:header + 40]):
        raise ValueError('no unused section header space')
    if max(s.PointerToRawData + s.SizeOfRawData for s in pe.sections) != len(data):
        raise ValueError('unexpected overlay')
    code_rva = align(max(s.VirtualAddress + max(s.Misc_VirtualSize, s.SizeOfRawData)
                         for s in pe.sections), pe.OPTIONAL_HEADER.SectionAlignment)
    code, entries = make_code(0x400000 + code_rva, catalog)
    raw_size = align(len(code), pe.OPTIONAL_HEADER.FileAlignment)
    after, changes = bytearray(data), []

    def edit(offset, before, value, purpose):
        if len(before) != len(value) or bytes(after[offset:offset + len(before)]) != before:
            raise ValueError('fingerprint mismatch: ' + purpose)
        after[offset:offset + len(value)] = value
        changes.append(dict(offset=offset, length=len(value), before=before.hex(),
                            after=value.hex(), purpose=purpose))

    at = lambda va: pe.get_offset_from_rva(va - 0x400000)
    edit(at(GATE_CALL), rel('e9', GATE_CALL, OLD_GATE) + b'\x90',
         rel('e9', GATE_CALL, entries['gate']) + b'\x90', 'extend automatic gate')
    edit(at(original.SNAPSHOT_CALL), rel('e8', original.SNAPSHOT_CALL, OLD_SNAPSHOT),
         rel('e8', original.SNAPSHOT_CALL, entries['snapshot']), 'capture chapter completion mask')
    new_header = struct.pack('<8sIIIIIIHHI', b'.sdg2', len(code), code_rva, raw_size,
                             len(data), 0, 0, 0, 0, 0x60000020)
    edit(header, bytes(40), new_header, 'new read-execute section')
    for offset, size, value in (
        (pe.FILE_HEADER.get_field_absolute_offset('NumberOfSections'), 2, count + 1),
        (pe.OPTIONAL_HEADER.get_field_absolute_offset('SizeOfImage'), 4,
         align(code_rva + len(code), pe.OPTIONAL_HEADER.SectionAlignment)),
        (pe.OPTIONAL_HEADER.get_field_absolute_offset('SizeOfCode'), 4,
         pe.OPTIONAL_HEADER.SizeOfCode + raw_size),
    ):
        edit(offset, data[offset:offset + size], value.to_bytes(size, 'little'), 'PE size/count')
    after.extend(code + bytes(raw_size - len(code)))
    checksum = pefile.PE(data=bytes(after), fast_load=True).generate_checksum()
    offset = pe.OPTIONAL_HEADER.get_field_absolute_offset('CheckSum')
    edit(offset, data[offset:offset + 4], struct.pack('<I', checksum), 'PE checksum')
    candidate = bytes(after)
    restore = bytearray(candidate[:len(data)])
    for change in changes:
        offset = change['offset']
        restore[offset:offset + change['length']] = bytes.fromhex(change['before'])
    if bytes(restore) != data or not pefile.PE(data=candidate, fast_load=True).verify_checksum():
        raise ValueError('preservation/checksum verification failed')
    return candidate, dict(source_sha256=source_sha256, candidate_sha256=sha256(candidate),
                           source_size=len(data), size=len(candidate), entries=entries,
                           changes=changes, appended_sha256=sha256(candidate[len(data):]),
                           catalog_sha256=sha256(Path(catalog_path).read_bytes()),
                           pvf_sha256=catalog['pvf_sha256'], all_other_original_bytes_preserved=True,
                           published=False, deployed=False, native_acceptance=False)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('source', type=Path)
    parser.add_argument('output', type=Path)
    parser.add_argument('--receipt', type=Path, required=True)
    args = parser.parse_args()
    paths = [p.resolve() for p in (args.source, args.output, args.receipt)]
    if len(set(paths)) != 3 or args.output.exists() or args.receipt.exists():
        parser.error('source, new output and new receipt must be distinct; outputs must not exist')
    candidate, receipt = build(args.source.read_bytes())
    with args.output.open('xb') as stream:
        stream.write(candidate)
    with args.receipt.open('x', encoding='utf-8') as stream:
        json.dump(receipt, stream, indent=2)
    print(json.dumps(dict(candidate_sha256=receipt['candidate_sha256'], size=len(candidate))))


if __name__ == '__main__':
    main()
