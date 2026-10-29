"""Internal exact-input candidate: consume a fresh right-up for title recursion.

This builder produces an unprotected experiment. It does not publish, install,
or claim gameplay acceptance. The historical builder remains a fixture helper.
"""
import hashlib
import inspect
import struct

import recursive_extension as rec
from pe_support import Image, available_memory, pe_checksum

INPUT_SHA256 = '7777de23d051b28e7cabf75328a8a5b911f50768220fbff8f2e971e3b953fe5f'
INPUT_SIZE = 111923200
RIGHT_TARGET = 0x55ADADC
FRAME_SITE = 0x21E9198
ROOT_STEP_SITE = 0x3829F50
RIGHTUP_SITE = 0x382A494
EXPECTED_HOOKS = {
    FRAME_SITE: bytes.fromhex('8b0da86a6805'),
    ROOT_STEP_SITE: bytes.fromhex('568bf180beee00000000'),
    RIGHTUP_SITE: bytes.fromhex('8935dcda5a05'),
}


def require(ok, message):
    if not ok:
        raise ValueError(message)


def align(value, alignment):
    require(alignment > 0 and alignment & (alignment - 1) == 0, 'bad alignment')
    return (value + alignment - 1) & ~(alignment - 1)


def fresh_extension(va, event_va):
    # Reuse the exact legacy frame contract, adding the one-shot input check
    # before its unique marker-2 emission. Code resolves all labels afterward.
    class FreshCode(rec.Code):
        def raw(self, value):
            if value == '5a59589d6a02':
                super().raw('8b15' + struct.pack('<I', event_va).hex())
                super().raw('83faff')
                self.je('legacy')
                super().raw('3b15' + struct.pack('<I', RIGHT_TARGET).hex())
                self.jne('legacy')
                super().raw('c705' + struct.pack('<I', event_va).hex() + 'ffffffff')
            super().raw(value)

    # Function-global injection is isolated to a newly compiled function; the
    # imported historical module and its validator remain unchanged.
    namespace = dict(rec.__dict__, Code=FreshCode)
    exec(compile(inspect.getsource(rec.recursive_extension), __file__, 'exec'), namespace)
    return namespace['recursive_extension'](va)


def reset_hook(va, event_va, original, continuation):
    c = rec.Code(va)
    c.raw('c705' + struct.pack('<I', event_va).hex() + 'ffffffff')
    c.raw(original.hex())
    c.jmp(continuation)
    return c.finish()


def rightup_hook(va, event_va):
    c = rec.Code(va)
    c.raw(EXPECTED_HOOKS[RIGHTUP_SITE].hex())
    c.raw('8935' + struct.pack('<I', event_va).hex())
    c.jmp(RIGHTUP_SITE + len(EXPECTED_HOOKS[RIGHTUP_SITE]))
    return c.finish()


def redirect(site, destination, length):
    require(length >= 5, 'short redirect')
    return b'\xe9' + struct.pack('<i', destination - site - 5) + b'\x90' * (length - 5)


def build(data):
    available_memory()
    require(len(data) == INPUT_SIZE and hashlib.sha256(data).hexdigest() == INPUT_SHA256,
            'exact guild cumulative raw input required')
    im = Image.from_bytes(data)
    code = im.section('.op19ui')
    require(code['va'] == rec.OP19UI and code['virtual_size'] == rec.LEGACY_VIRTUAL_SIZE
            and code['raw_size'] == 0x1000 and code['characteristics'] == 0x60000020,
            'unexpected title code section')
    require(hashlib.sha256(im.bytes_at(rec.LEGACY_EXTENSION_VA, rec.LEGACY_EXTENSION_SIZE)).hexdigest()
            == rec.LEGACY_EXTENSION_SHA256, 'legacy title extension changed')
    require(im.bytes_at(rec.PRODUCER_BOUNDARY, 5) == redirect(rec.PRODUCER_BOUNDARY, rec.OP19UI, 5),
            'producer boundary changed')
    require(im.bytes_at(code['va'] + code['virtual_size'], code['raw_size'] - code['virtual_size'])
            == bytes(code['raw_size'] - code['virtual_size']), 'occupied title code tail')
    for site, original in EXPECTED_HOOKS.items():
        require(im.bytes_at(site, len(original)) == original, f'input hook changed: {site:x}')

    pe = struct.unpack_from('<I', data, 0x3c)[0]
    section_align, file_align = struct.unpack_from('<II', data, im.optional + 32)
    header = im.section_table + 40 * im.section_count
    first_raw = min(s['raw'] for s in im.sections if s['raw_size'])
    require(header + 40 <= first_raw and data[header:header+40] == bytes(40), 'no empty section header')
    require(not any(s['name'] == '.titlert' for s in im.sections), 'runtime section already exists')
    require(max(s['raw'] + s['raw_size'] for s in im.sections) == len(data), 'unreviewed file overlay')
    require(struct.unpack_from('<II', data, im.optional + 128) == (0, 0), 'signed input unsupported')
    event_rva = align(max(s['va'] - 0x400000 + max(s['virtual_size'], s['raw_size']) for s in im.sections), section_align)
    require(event_rva == im.size_of_image, 'unexpected image tail geometry')
    event_va = 0x400000 + event_rva
    event_raw = align(len(data), file_align)
    data_size = align(4, file_align)
    new_image_size = align(event_rva + data_size, section_align)

    cursor = code['va'] + code['virtual_size']
    chunks = []
    extension = fresh_extension(cursor, event_va)
    entry = cursor
    chunks.append((cursor, extension)); cursor += len(extension)
    hooks = []
    for site in (FRAME_SITE, ROOT_STEP_SITE, RIGHTUP_SITE):
        original = EXPECTED_HOOKS[site]
        stub = (rightup_hook(cursor, event_va) if site == RIGHTUP_SITE else
                reset_hook(cursor, event_va, original, site + len(original)))
        hooks.append(dict(site=site, original=original.hex(), stub=cursor, length=len(stub)))
        chunks.append((cursor, stub)); cursor += len(stub)
    require(cursor <= code['va'] + code['raw_size'], 'title helper exceeds code tail')

    out = bytearray(data)
    changes = []
    def patch(offset, value):
        out[offset:offset+len(value)] = value
        changes.append((offset, offset+len(value)))
    patch(pe + 6, struct.pack('<H', im.section_count + 1))
    initialized = struct.unpack_from('<I', data, im.optional + 8)[0]
    patch(im.optional + 8, struct.pack('<I', initialized + data_size))
    patch(im.optional + 56, struct.pack('<I', new_image_size))
    patch(header, struct.pack('<8sIIIIIIHHI', b'.titlert', 4, event_rva, data_size, event_raw, 0, 0, 0, 0, 0xC0000040))
    patch(code['header'] + 8, struct.pack('<I', cursor - code['va']))
    patch(im.file_offset(rec.PRODUCER_BOUNDARY, 5), redirect(rec.PRODUCER_BOUNDARY, entry, 5))
    for hook in hooks:
        original = bytes.fromhex(hook['original'])
        patch(im.file_offset(hook['site'], len(original)), redirect(hook['site'], hook['stub'], len(original)))
    for va, payload in chunks:
        patch(im.file_offset(va, len(payload)), payload)
    out.extend(bytes(event_raw - len(out)))
    out.extend(b'\xff\xff\xff\xff' + bytes(data_size - 4))
    patch(im.optional + 64, struct.pack('<I', pe_checksum(out, im.optional + 64)))
    cursor = 0
    for lo, hi in sorted(changes):
        require(lo >= cursor and data[cursor:lo] == out[cursor:lo], 'overlap or undeclared change')
        cursor = hi
    require(data[cursor:] == out[cursor:len(data)], 'undeclared trailing input change')
    result = bytes(out)
    parsed = Image.from_bytes(result)
    runtime = parsed.section('.titlert')
    require(runtime['characteristics'] == 0xC0000040 and parsed.bytes_at(event_va, 4) == b'\xff'*4,
            'bad runtime state or permissions')
    return result, dict(schema='title-fresh-event-build.v1', inputSha256=INPUT_SHA256,
        outputSha256=hashlib.sha256(result).hexdigest(), inputSize=len(data), outputSize=len(result),
        eventStateVa=event_va, eventStateInitial=0xffffffff, runtimeSection=runtime,
        recursiveExtensionVa=entry, hooks=hooks, chunks=[dict(va=va,size=len(blob),sha256=hashlib.sha256(blob).hexdigest()) for va,blob in chunks],
        inputChangedRanges=sorted(changes), appendRange=[len(data),len(result)],
        eventConsumedOnlyOnNewPromotion=True, legacyGateUnchanged=True,
        nativeVerified=False, antiREApplied=False, published=False, gameAccepted=False)
