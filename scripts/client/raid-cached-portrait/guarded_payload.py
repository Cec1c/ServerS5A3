"""P34 current-raid/current-row checked fallback, pinned to v168 native layout.

Only the null-active-actor branch uses this helper. The cache fallback is limited
to the exact category1 identity, no model pointer, and existing icon frames 0..15.
"""
from build_candidate import Code

def member_gate(base):
    c=Code(base)
    c.raw('9c 60 8bf1 0fb7fa 85f6'); c.je('miss')
    c.raw('85ff'); c.je('miss')
    c.raw('81ffffff0000'); c.je('miss')
    c.raw('a178376a05 85c0'); c.je('miss')
    c.raw('397008'); c.jne('miss')
    c.raw('8b5004 83faff'); c.je('miss')
    c.raw('3916'); c.jne('miss')
    c.raw('8b86a0000000 85c0'); c.je('miss')
    c.raw('8d96a0000000 3910'); c.jne('miss')
    c.raw('8b9ea4000000 8b8ea8000000 85db'); c.je('miss')
    c.raw('3bcb'); c.branch('0f86','miss')
    c.raw('3b8eac000000'); c.branch('0f87','miss')
    c.raw('8bc1 2bc3 33d2 bb64000000 f7f3 85d2'); c.jne('miss')
    c.raw('3dff000000'); c.branch('0f87','miss')
    c.raw('8b9ea4000000')
    c.label('loop')
    c.raw('66393b'); c.je('hit')
    c.raw('83c364 3bd9'); c.branch('0f82','loop')
    c.label('miss'); c.raw('c744241c00000000 61 9d c3')
    c.label('hit'); c.raw('c744241c01000000 61 9d c3')
    return c.finish()

def row_gate(base, member_base):
    c=Code(base)
    c.raw('9c 60'); c.call(member_base); c.raw('85c0'); c.je('miss')
    c.raw('8b1d78376a05 8b83cc000000 85c0'); c.je('miss')
    c.raw('8d93cc000000 3910'); c.jne('miss')
    c.raw('8b8bd0000000 8b93d4000000 85c9'); c.je('miss')
    c.raw('3bd1'); c.branch('0f86','miss')
    c.raw('3b93d8000000'); c.branch('0f87','miss')
    # Exact live row object: the native getter returns entry +4, stride 0x24.
    c.raw('8b7dc4 83ff04'); c.branch('0f82','miss')
    c.raw('83ef04 3bf9'); c.branch('0f82','miss')
    c.raw('3bfa'); c.branch('0f83','miss')
    c.raw('8bc2 2bc1 33d2 bb24000000 f7f3 85d2'); c.jne('miss')
    c.raw('8bc7 2bc1 33d2 f7f3 85d2'); c.jne('miss')
    c.raw('8b4508 3907'); c.jne('miss')
    # The selected group offset is native row-index*20 at caller [ebp-0x40].
    c.raw('8d7704 8b4608 85c0'); c.je('miss')
    c.raw('8d5608 3910'); c.jne('miss')
    c.raw('8b4e0c 8b5e10 85c9'); c.je('miss')
    c.raw('3bd9'); c.branch('0f86','miss')
    c.raw('3b5e14'); c.branch('0f87','miss')
    c.raw('8bc3 2bc1 33d2 be14000000 f7f6 85d2'); c.jne('miss')
    c.raw('8b45c0 8bf8 33d2 f7f6 85d2'); c.jne('miss')
    c.raw('2bd9 3bfb'); c.branch('0f83','miss')
    c.raw('03cf 8b01 85c0'); c.je('miss')
    c.raw('3908'); c.jne('miss')
    c.raw('8b4104 8b5108 85c0'); c.je('miss')
    c.raw('3bd0'); c.branch('0f86','miss')
    c.raw('3b510c'); c.branch('0f87','miss')
    c.raw('2bd0 f6c203'); c.jne('miss')
    # Native row entries hold a full dword promoted from the wire uint16.
    c.raw('8b542414 3b10'); c.jne('miss')
    c.raw('c744241c01000000'); c.jump('done')
    c.label('miss'); c.raw('c744241c00000000')
    c.label('done'); c.raw('61 9d c3')
    return c.finish()

def entry_gate(base, row_base):
    c=Code(base)
    c.raw('9c 60 0fb7d7 85d2');c.je('miss')
    c.raw('81faffff0000');c.je('miss')
    c.raw('8b0d78376a05 85c9');c.je('miss')
    # Read current context without invoking the lazy allocating getter.
    c.raw('8b7108 85f6');c.je('miss')
    c.raw('8bce');c.call(row_base);c.raw('85c0');c.je('miss')
    c.raw('0fb65603 52 6a01 0fb65602 52 0fb7d7 52')
    c.call(0x1A5A550);c.raw('83c410 85c0');c.je('miss')
    c.raw('8138dcd19e04');c.jne('miss')
    c.raw('0fb75020 0fb7cf 3bd1');c.jne('miss')
    c.raw('0fb69004060000 3a5602');c.jne('miss')
    c.raw('0fb69005060000 3a5603');c.jne('miss')
    # Cache fallback needs only the existing native class icon path. Never
    # follow a cached world-model pointer through the model renderer.
    c.raw('83b8f404000000');c.jne('miss')
    c.raw('8378340f');c.branch('0f87','miss')
    c.raw('83783000');c.je('miss')
    c.raw('89442410 61 9d');c.jump(0x110B134)
    c.label('miss');c.raw('61 9d');c.jump(0x110B352)
    return c.finish()


def emit(base):
    entry=entry_gate(base,base+0x100)
    row=row_gate(base+0x100,base+0x300)
    member=member_gate(base+0x300)
    if (len(entry),len(row),len(member)) != (207,370,225):
        raise ValueError("reviewed helper sizes changed")
    return entry+b"\xcc"*(0x100-len(entry))+row+b"\xcc"*(0x200-len(row))+member
