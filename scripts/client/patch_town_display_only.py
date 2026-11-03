"""Create a missing town visual at the native same-area NOTI23 lookup only.

Full actor/stat objects and existing visuals retain the original path. The
server's separately gated ordinary-town mode0-only projection uses this path.
"""
import struct
import patch_receivers_v128 as pe
from patch_at5t_input_priority import Code

INPUT_SHA = '24372bf2df57cdaecc696752299dbe8c61e222806a697e3d1c6f57fbcaffbdae'
INPUT_SIZE = 112549888
OUTPUT_SHA = 'd1930d5bb07ee8fae348e0c23551b0f12bcfaea67ed7bf53ab0c9099e195c309'
SITE = 0x203CBC4
GETTER = 0x29659B0
FACTORY = 0x2965BD0
ORIGINAL = b'\xe8' + struct.pack('<i', GETTER - SITE - 5)


def helper(base):
    c = Code(base)
    c.emit('558bec5657 8bf1 33ff 85f6')  # Same thiscall/ret4; retain UserInfo.
    c.branch('0f84', 'done')
    c.emit('ff7508')
    c.branch('e8', GETTER)
    c.emit('8bf8 85ff')
    c.branch('0f85', 'done')
    c.emit('83bef804000000')            # Full mode1 objects remain untouched.
    c.branch('0f85', 'done')
    c.emit('66837e2000')
    c.branch('0f84', 'done')
    c.emit('a1b46f7b05 0b05b06f7b05')   # An established local actor must exist.
    c.branch('0f84', 'done')
    c.emit('8bce')
    c.branch('e8', 0x2964340)           # Native auxiliary-namespace predicate.
    c.emit('84c0')
    c.branch('0f85', 'done')
    c.emit('6a00 8bce')
    c.branch('e8', FACTORY)             # Native visual only, no full class/skills.
    c.emit('8bf8')
    c.label('done')
    c.emit('8bc7 5f5e5d c20400')
    return c.finish()


def build(data):
    if len(data) != INPUT_SIZE or pe.sha(data) != INPUT_SHA:
        raise ValueError('exact installed cumulative SHA256 and size required')
    opt, sections = pe.headers(data)
    coff = opt - 24
    count = struct.unpack_from('<H', data, coff + 6)[0]
    table = opt + struct.unpack_from('<H', data, coff + 20)[0]
    section = table + (count - 1) * 40
    name, used, rva, raw_size, raw = struct.unpack_from('<8sIIII', data, section)
    if (name != b'.ctnull\0' or (used, rva, raw_size, raw) != (0x53C, 0x6B8B000, 0x1000, 0x6B55000) or raw + raw_size != len(data) or
            struct.unpack_from('<I', data, section + 36)[0] != 0x60000020):
        raise ValueError('owned RX padding layout changed')
    if sum(data[table+n*40:table+n*40+8] == name for n in range(count)) != 1:
        raise ValueError('owned RX section is not unique')
    if used <= 0 or used >= raw_size or any(data[raw + used:raw + raw_size]):
        raise ValueError('owned padding is not empty')
    at = pe.offset(sections, SITE, len(ORIGINAL))
    if data[at:at + 5] != ORIGINAL:
        raise ValueError('native same-area visual lookup changed')
    # Both branches are distinguished by their exact call sites. The off-area
    # lookup and all native ownership, current-model and portrait patches stay.
    for va, expected in ((0x203CC27, b'\xe8' + struct.pack('<i', GETTER - 0x203CC2C)),
                         (GETTER, bytes.fromhex('558bec568bf10fb7')),
                         (0x2964340, bytes.fromhex('558bec6aff6870ca')),
                         (FACTORY, bytes.fromhex('558bec6aff')),
                         (0x2965DBE, bytes.fromhex('e93da4550490'))):
        pos = pe.offset(sections, va, len(expected))
        if data[pos:pos + len(expected)] != expected:
            raise ValueError('native lifecycle/retained guard changed')
    offset = (used + 15) & ~15
    base = pe.IMAGE_BASE + rva + offset
    code = helper(base)
    if offset + len(code) > raw_size:
        raise ValueError('owned padding insufficient')
    out = bytearray(data)
    out[at:at + 5] = b'\xe8' + struct.pack('<i', base - SITE - 5)
    out[raw + offset:raw + offset + len(code)] = code
    struct.pack_into('<I', out, section + 8, offset + len(code))
    struct.pack_into('<I', out, opt + 64, pe.checksum(out, opt + 64))
    ranges = sorted(((at, at + 5), (raw + offset, raw + offset + len(code)),
                     (section + 8, section + 12), (opt + 64, opt + 68)))
    restored = bytearray(out)
    for lo, hi in ranges:
        restored[lo:hi] = data[lo:hi]
    if restored != data:
        raise ValueError('unexpected mutation')
    if pe.sha(out) != OUTPUT_SHA:
        raise ValueError('deterministic cumulative output identity changed')
    return bytes(out), dict(input_sha256=INPUT_SHA, output_sha256=pe.sha(out),
                           input_size=len(data), output_size=len(out), site=hex(SITE),
                           helper_va=hex(base), helper_size=len(code), changed_ranges=ranges,
                           off_area_changed=False, full_actor_loader_changed=False,
                           loading_draw_changed=False, protection_changed=False,
                           added_dll=False, gameplay_acceptance='pending')
