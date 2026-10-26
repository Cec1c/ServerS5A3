"""Portable title-only extension for the fixed v170 OP19 UI producer."""
import struct


OP19UI = 0x6E95000
OLD_VIRTUAL_SIZE = 0x1D2
FALLBACK = 0x6E95050
CONTINUE = 0x25FA994
PUSH_MARKER = 0x374EA10
TITLE_DEST_SLOT = 13

# The saved return is AFTER the five-byte CALL, not the CALL instruction VA.
TITLE_RETURNS = (0x12A3479, 0x12AB199, 0x12ACB30, 0x12ACB7D, 0x12AF91D)

# Parent returns are read through the producer's saved EBP. These identify the
# title control-event callers; all other producer paths retain marker 0.
PARENT_RETURNS = {
    0x12A3479: (0x12AEF1B, 0x12AF00E, 0x12AB1CD),
    0x12AB199: (0x12AE884,),
    0x12ACB30: (0x12AE59F,),
    0x12ACB7D: (0x12AE59F,),
}

# Current-image claim entry: the CALL at 0x24A2889 lives in the window
# control-event handler 0x24A1F80 and is reached only through vtable
# 0x49BD6E0 + 0x28, so it has no fixed parent return. It is gated by the saved
# return, destination slot 13, count 1 and its own 4th argument.
CLAIM_CALL_SITE = 0x24A2889
CLAIM_RETURN = 0x24A288E
CLAIM_4TH_ARG = 0

# The five legacy entries keep their existing path. Only the claim entry gets
# the extra 4th-argument check, because four of the five legacy callers pass 2
# there and would otherwise be downgraded to marker 0.
TITLE_RETURNS_WITH_CLAIM = TITLE_RETURNS + (CLAIM_RETURN,)


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
    c.cmp_ret(0x12A3479)
    c.je("check_3474")
    c.cmp_ret(0x12AB199)
    c.je("check_ab")
    c.cmp_ret(0x12ACB30)
    c.je("check_ac")
    c.cmp_ret(0x12ACB7D)
    c.je("check_ac")
    c.cmp_ret(0x12AF91D)
    c.je("args")
    c.cmp_ret(CLAIM_RETURN)
    c.je("claim")
    c.jmp("fallback")

    c.label("claim")
    c.cmp_arg(0x14, CLAIM_4TH_ARG)
    c.jne("fallback")
    c.jmp("args")

    c.label("check_3474")
    c.raw("8b450085c0")
    c.je("fallback")
    c.raw("a803")
    c.jne("fallback")
    for parent in PARENT_RETURNS[0x12A3479]:
        c.cmp_saved_return(parent)
        c.je("args")
    c.jmp("fallback")

    c.label("check_ab")
    c.raw("8b450085c0")
    c.je("fallback")
    c.raw("a803")
    c.jne("fallback")
    c.cmp_saved_return(PARENT_RETURNS[0x12AB199][0])
    c.jne("fallback")
    c.jmp("args")

    c.label("check_ac")
    c.raw("8b450085c0")
    c.je("fallback")
    c.raw("a803")
    c.jne("fallback")
    c.cmp_saved_return(PARENT_RETURNS[0x12ACB30][0])
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
