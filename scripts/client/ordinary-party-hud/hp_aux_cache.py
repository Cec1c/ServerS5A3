"""Reviewed aux-cache fixture used by the portable HUD lookup verifier.

This is the RANGES/identity/bucket/signed/install subset only. It has no
suite runner and no dependency outside the standard library.
"""

RANGES = {
    0x1A5A5E0: 0x97,
    0x224D950: 0x18,
    0x224D8D0: 0x17,
    0x2429E20: 0x16C,
    0x224E5F0: 0x1B,
    0x3AC9DD0: 0x5D,
    0xC3FA60: 0xF6,
    0x3E76D18: 0xE,
}


def identity(key, channel, server):
    return ((server & 255) << 24) | ((channel & 255) << 16) | (key & 65535)


def bucket(key):
    quotient, remainder = divmod((key ^ 0xDEADBEEF) & 0x7FFFFFFF, 0x1F31D)
    value = remainder * 0x41A7 - quotient * 0xB14
    if value < 0:
        value += 0x7FFFFFFF
    return value & 7


def signed(value):
    return value if value < 0x80000000 else value - 0x100000000


def install(u, put, w, categories, windows):
    """Five native hash containers; vectors contain checked list iterators."""
    for address, code in windows.items():
        put(address, code)
    put(0x3E738B1, b"\xcc")
    for category in range(5):
        table = 0x5676898 + 0x38 * category
        arena = 0x400000 + 0x2000 * category
        proxy, sentinel, vector = arena, arena + 0x20, arena + 0x100
        put(table, bytes(0x38))
        put(arena, bytes(0x2000))
        w(table + 8, proxy)
        w(proxy, table + 8)
        w(table + 12, sentinel)
        w(table + 0x1C, vector)
        w(table + 0x20, vector + 8 * 24)
        w(table + 0x24, vector + 8 * 24)
        w(table + 0x2C, 7)
        w(table + 0x30, 8)
        groups = [[] for _ in range(8)]
        records = categories.get(category, {})
        for key, value in records.items():
            groups[bucket(key)].append((key, value))
        ordered = [item for group in groups for item in
                   sorted(group, key=lambda item: signed(item[0]))]
        nodes = {key: arena + 0x400 + index * 16
                 for index, (key, _) in enumerate(ordered)}
        chain = [sentinel] + [nodes[key] for key, _ in ordered] + [sentinel]
        w(sentinel, chain[1])
        w(sentinel + 4, chain[-2])
        w(table + 16, len(ordered))
        for index, (key, value) in enumerate(ordered, 1):
            node = nodes[key]
            w(node, chain[index + 1])
            w(node + 4, chain[index - 1])
            w(node + 8, key)
            w(node + 12, value)
        for index, group in enumerate(groups):
            group.sort(key=lambda item: signed(item[0]))
            first = nodes[group[0][0]] if group else sentinel
            last = nodes[group[-1][0]] if group else sentinel
            for offset, node in ((0, first), (12, last)):
                w(vector + index * 24 + offset, proxy)
                w(vector + index * 24 + offset + 8, node)
