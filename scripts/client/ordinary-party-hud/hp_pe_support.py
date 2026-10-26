"""Minimal PE and existing HUD-helper proof support, extracted from the reviewed HUD builder."""
import array,struct,sys
VA=0x6ec1000

def require(condition, message):
    if not condition:
        raise ValueError(message)

def u16(data, at):
    return struct.unpack_from("<H", data, at)[0]

def u32(data, at):
    return struct.unpack_from("<I", data, at)[0]

class PE:
    """Minimal PE32 reader used for validation and VA-to-file mapping."""

    def __init__(self, data):
        self.data = data
        require(data[:2] == b"MZ", "input is not an MZ image")
        self.nt = u32(data, 0x3C)
        require(data[self.nt:self.nt + 4] == b"PE\0\0", "missing PE signature")
        require(u16(data, self.nt + 4) == 0x14C, "input is not PE32")
        self.count = u16(data, self.nt + 6)
        self.opt = self.nt + 24
        require(u16(data, self.opt) == 0x10B,
                "input is not a PE32 optional header")
        self.base = u32(data, self.opt + 28)
        self.sa, self.fa = struct.unpack_from("<II", data, self.opt + 32)
        self.st = self.opt + u16(data, self.nt + 20)
        self.sections = []
        for index in range(self.count):
            at = self.st + index * 40
            virtual_size, rva, raw_size, raw = struct.unpack_from(
                "<IIII", data, at + 8)
            self.sections.append(dict(
                index=index,
                name=data[at:at + 8].rstrip(b"\0").decode("ascii"),
                virtual_size=virtual_size,
                rva=rva,
                raw_size=raw_size,
                raw=raw,
                characteristics=u32(data, at + 36),
            ))

    def offset(self, va, size=1):
        rva = va - self.base
        for section in self.sections:
            if (section["rva"] <= rva and
                    rva + size <= section["rva"] + section["raw_size"]):
                return section["raw"] + rva - section["rva"]
        raise ValueError("VA has no raw backing: 0x{:x} / 0x{:x}".format(va, size))

    def read(self, va, size):
        at = self.offset(va, size)
        return self.data[at:at + size]

def pe_checksum(data, checksum_offset):
    work = bytearray(data)
    struct.pack_into("<I", work, checksum_offset, 0)
    if len(work) & 1:
        work.append(0)
    words = array.array("H", work)
    if sys.byteorder != "little":
        words.byteswap()
    total = sum(words)
    while total >> 16:
        total = (total & 0xFFFF) + (total >> 16)
    return (total + len(data)) & 0xFFFFFFFF

class Code:
    def __init__(self, base: int):
        self.base = base
        self.buf = bytearray()
        self.labels: dict[str, int] = {}
        self.fixups: list[tuple[str, int, str]] = []

    def raw(self, text: str) -> None:
        self.buf.extend(bytes.fromhex(text.replace(" ", "")))

    def label(self, name: str) -> None:
        self.labels[name] = len(self.buf)

    def call(self, va: int) -> None:
        self.buf.append(0xE8)
        self.buf.extend(struct.pack("<i", va - (self.base + len(self.buf) + 4)))

    def jump(self, target: int | str) -> None:
        self.buf.append(0xE9)
        offset = len(self.buf)
        self.buf.extend(b"\0\0\0\0")
        if isinstance(target, str):
            self.fixups.append(("jmp", offset, target))
        else:
            struct.pack_into("<i", self.buf, offset,
                             target - (self.base + offset + 4))

    def je(self, target: str) -> None:
        self.buf.extend(b"\x0f\x84")
        offset = len(self.buf)
        self.buf.extend(b"\0\0\0\0")
        self.fixups.append(("je", offset, target))

    def jne(self, target: str) -> None:
        self.buf.extend(b"\x0f\x85")
        offset = len(self.buf)
        self.buf.extend(b"\0\0\0\0")
        self.fixups.append(("jne", offset, target))

    def finish(self) -> bytes:
        for _, offset, label in self.fixups:
            struct.pack_into("<i", self.buf, offset,
                             self.labels[label] - offset - 4)
        return bytes(self.buf)

def helper():
    c = Code(VA)

    def branch(op, label):
        c.raw(op)
        at = len(c.buf)
        c.raw("00000000")
        c.fixups.append(("branch", at, label))

    # thiscall(manager, slot), returning an actor without publishing it to the
    # scene/party model tables. Preserve the original nonvolatile registers.
    c.raw("55 8bec 83ec0c 53 56 57 8b5d08 53")
    c.call(0x2974890)
    c.raw("85c0")
    c.jne("done")
    c.raw("83fb07")
    branch("0f87", "miss")
    c.call(0x18C2800)
    c.raw("84c0")
    c.jne("miss")
    c.raw("8b0d58376a05 85c9")
    c.je("miss")
    # Existing cached local actor only. Do not lazily create/rebind anything.
    c.raw("8b4170 85c0")
    c.je("miss")
    c.raw("8945fc 8bb0c8040000 85f6")
    c.je("miss")
    # The currently associated party must still contain the local identity.
    c.raw("0fb74020 50 8bce")
    c.call(0x2973110)
    c.raw("83f8ff")
    c.je("miss")
    c.raw("8b7c9e2c 85ff")
    c.je("miss")
    c.raw("81ffffff0000")
    branch("0f83", "miss")
    # Resolve the zero scene namespace through the existing native getters;
    # verify the party namespace agrees before looking up mode8 data.
    c.raw("8b4dfc")
    c.call(0x2964680)
    c.raw("0fb6c0 85c0")
    c.je("miss")
    c.raw("8945f8 8b5674 85d2")
    c.je("owner_ok")
    c.raw("3bd0")
    c.jne("miss")
    c.label("owner_ok")
    c.raw("8b4dfc")
    c.call(0x2964560)
    c.raw("0fb6c0 8945f4 394670")
    c.jne("miss")
    c.raw("50 ff75f8 57")
    c.call(0x1A5A5E0)
    c.raw("83c40c 85c0")
    c.je("miss")
    c.raw("0fb75020 3bd7")
    c.jne("miss")
    c.raw("0fb69004060000 3b55f8")
    c.jne("miss")
    c.raw("0fb69005060000 3b55f4")
    c.jne("miss")
    # Both renderers dereference stats. Metadata-only entries are insufficient.
    c.raw("83b8f804000000")
    c.je("miss")
    c.jump("done")
    c.label("miss")
    c.raw("33c0")
    c.label("done")
    c.raw("5f 5e 5b 8be5 5d c20400")
    return c.finish()
