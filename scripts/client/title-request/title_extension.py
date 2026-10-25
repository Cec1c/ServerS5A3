"""Portable title-only extension for the fixed v170 OP19 UI producer."""
import struct


OP19UI = 0x6E95000
OLD_VIRTUAL_SIZE = 0x1D2
FALLBACK = 0x6E95050
CONTINUE = 0x25FA994
PUSH_MARKER = 0x374EA10
TITLE_DEST_SLOT = 13

TITLE_RETURNS = (
    0x12A3474,
    0x12AB194,
    0x12ACB2B,
    0x12ACB78,
    0x12AF918,
)

# Parent returns are read through the producer's saved EBP. These identify the
# title control-event callers; all other producer paths retain marker 0.
PARENT_RETURNS = {
    0x12A3474: (0x12AEF1B, 0x12AF00E, 0x12AB1CD),
    0x12AB194: (0x12AE884,),
    0x12ACB2B: (0x12AE59F,),
    0x12ACB78: (0x12AE59F,),
}


class Code:
    def __init__(self, va):
        self.va = va
        self.data = bytearray()
        self.labels = {}
        self.fixups = []

    def raw(self, text):
        self.data.extend(bytes.fromhex(text.replace(" ", "")))

    def label(self, name):
        if name in self.labels:
            raise ValueError(f"duplicate label {name}")
        self.labels[name] = len(self.data)

    def rel32(self, opcode, label):
        self.raw(opcode)
        at = len(self.data)
        self.data.extend(b"\0\0\0\0")
        self.fixups.append((at, label))

    def jmp(self, label):
        self.rel32("e9", label)

    def call(self, target):
        self.raw("e8")
        at = len(self.data)
        self.data.extend(b"\0\0\0\0")
        self.fixups.append((at, target))

    def je(self, label):
        self.rel32("0f84", label)

    def jne(self, label):
        self.rel32("0f85", label)

    def cmp_ret(self, value):
        self.raw("817d04")
        self.data.extend(struct.pack("<I", value))

    def cmp_arg(self, displacement, value):
        self.raw("837d" + f"{displacement:02x}" + f"{value:02x}")

    def cmp_saved_return(self, value):
        self.raw("817804")
        self.data.extend(struct.pack("<I", value))

    def finish(self):
        for at, target in self.fixups:
            destination = (
                self.va + self.labels[target] if isinstance(target, str) else target
            )
            struct.pack_into("<i", self.data, at, destination - self.va - at - 4)
        return bytes(self.data)


def fallback_redirect(extension_va):
    """Return the five-byte branch installed at the old 0x6E95050 fallback."""
    return b"\xe9" + struct.pack("<i", extension_va - FALLBACK - 5)


def title_extension(va):
    """Build the title-only extension for direct composition by another builder."""
    c = Code(va)
    c.cmp_ret(0x12A3474)
    c.je("check_3474")
    c.cmp_ret(0x12AB194)
    c.je("check_ab")
    c.cmp_ret(0x12ACB2B)
    c.je("check_ac")
    c.cmp_ret(0x12ACB78)
    c.je("check_ac")
    c.cmp_ret(0x12AF918)
    c.je("args")
    c.jmp("fallback")

    c.label("check_3474")
    c.raw("8b450085c0")
    c.je("fallback")
    c.raw("a803")
    c.jne("fallback")
    for parent in PARENT_RETURNS[0x12A3474]:
        c.cmp_saved_return(parent)
        c.je("args")
    c.jmp("fallback")

    c.label("check_ab")
    c.raw("8b450085c0")
    c.je("fallback")
    c.raw("a803")
    c.jne("fallback")
    c.cmp_saved_return(PARENT_RETURNS[0x12AB194][0])
    c.jne("fallback")
    c.jmp("args")

    c.label("check_ac")
    c.raw("8b450085c0")
    c.je("fallback")
    c.raw("a803")
    c.jne("fallback")
    c.cmp_saved_return(PARENT_RETURNS[0x12ACB2B][0])
    c.jne("fallback")

    c.label("args")
    c.cmp_arg(0x10, 1)
    c.jne("fallback")
    c.cmp_arg(0x0C, TITLE_DEST_SLOT)
    c.jne("fallback")
    c.raw("589d6a02")
    c.call(PUSH_MARKER)
    c.jmp("continue")

    c.label("fallback")
    c.raw("589d6a00")
    c.call(PUSH_MARKER)

    c.label("continue")
    c.jmp(CONTINUE)
    return c.finish()
