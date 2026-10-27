"""Append a parent-frame-only marker inheritance rule after the V179 title gate."""

import struct


OP19UI = 0x6E95000
LEGACY_VIRTUAL_SIZE = 0x2E8
LEGACY_EXTENSION_VA = 0x6E951D2
LEGACY_EXTENSION_SIZE = 0x116
LEGACY_EXTENSION_SHA256 = "a21e4f050e4441d5523ca7ee515ffc10b510eac4c4a86bf99217ba971ce6ba3d"

PRODUCER = 0x25F9A70
PRODUCER_BOUNDARY = 0x25FA98D
CONTINUE = 0x25FA994
PUSH_MARKER = 0x374EA10
WRITER_GETTER = 0x374EA10
WRITER_U8 = 0x374F390
WRITER_GLOBAL = 0x57ED8D4
APPEND_LEAF = 0x38C7F70

OUTER_CALL = 0x12ACB78
OUTER_RETURN = 0x12ACB7D
OUTER_FUNCTION = 0x12ABEA0
OUTER_PARENT_CALL = 0x12AE59A
OUTER_PARENT_RETURN = 0x12AE59F

RECURSIVE_CALL = 0x25FA48E
INNER_RETURN = 0x25FA493
TARGET_SLOT = 13
OUTER_COUNT = 1
OUTER_ARG4 = 0
OUTER_ARG5 = 0
INNER_ARG4 = 3
INNER_ARG5 = 1
MAX_FRAME_DISTANCE = 0x2000


class Code:
    def __init__(self, va):
        self.va = va
        self.data = bytearray()
        self.labels = {}
        self.fixups = []

    def raw(self, value):
        self.data.extend(bytes.fromhex(value.replace(" ", "")))

    def label(self, name):
        if name in self.labels:
            raise ValueError(f"duplicate label {name}")
        self.labels[name] = len(self.data)

    def rel32(self, opcode, target):
        self.raw(opcode)
        offset = len(self.data)
        self.data.extend(b"\0\0\0\0")
        self.fixups.append((offset, target))

    def jmp(self, target):
        self.rel32("e9", target)

    def call(self, target):
        self.rel32("e8", target)

    def je(self, target):
        self.rel32("0f84", target)

    def jne(self, target):
        self.rel32("0f85", target)

    def jbe(self, target):
        self.rel32("0f86", target)

    def ja(self, target):
        self.rel32("0f87", target)

    def cmp_ebp_imm(self, displacement, value):
        self.raw("817d" + f"{displacement:02x}")
        self.data.extend(struct.pack("<i", value))

    def cmp_eax_imm(self, displacement, value):
        self.raw("8178" + f"{displacement:02x}")
        self.data.extend(struct.pack("<i", value))

    def cmp_ecx_imm(self, displacement, value):
        self.raw("8179" + f"{displacement:02x}")
        self.data.extend(struct.pack("<i", value))

    def finish(self):
        for offset, target in self.fixups:
            destination = self.va + self.labels[target] if isinstance(target, str) else target
            struct.pack_into("<i", self.data, offset, destination - self.va - offset - 4)
        return bytes(self.data)


def boundary_redirect(extension_va):
    """Redirect the real producer boundary from the legacy extension to the new one."""
    return b"\xe9" + struct.pack("<i", extension_va - PRODUCER_BOUNDARY - 5)


def recursive_extension(va):
    """Build the recursively scoped extension.

    The new code handles exactly one inner producer return, 0x25FA493. Every
    other frame is restored and sent to the existing V179 extension, preserving
    its 17-case behavior without widening automatic callers.
    """
    c = Code(va)
    c.raw("9c505152")  # pushfd; push eax; push ecx; push edx

    c.cmp_ebp_imm(0x4, INNER_RETURN)
    c.jne("legacy")

    # Parent producer frame must be a nearby, higher stack frame.
    c.raw("8b4500")  # mov eax,[ebp]
    c.raw("85c0")
    c.je("legacy")
    c.raw("3bc5")  # cmp eax,ebp
    c.jbe("legacy")
    c.raw("8bd0")  # mov edx,eax
    c.raw("2bd5")  # sub edx,ebp
    c.raw("81fa" + struct.pack("<I", MAX_FRAME_DISTANCE).hex())
    c.ja("legacy")

    # The immediate parent is the authenticated outer producer CALL.
    c.cmp_eax_imm(0x4, OUTER_RETURN)
    c.jne("legacy")
    c.cmp_eax_imm(0x8, -1)
    c.jne("legacy")
    c.cmp_eax_imm(0xC, TARGET_SLOT)
    c.jne("legacy")
    c.cmp_eax_imm(0x10, OUTER_COUNT)
    c.jne("legacy")
    c.cmp_eax_imm(0x14, OUTER_ARG4)
    c.jne("legacy")
    c.cmp_eax_imm(0x18, OUTER_ARG5)
    c.jne("legacy")

    # The grandparent must be the exact caller of the authenticated UI method.
    c.raw("8b08")  # mov ecx,[eax]
    c.raw("85c9")
    c.je("legacy")
    c.raw("3bc8")  # cmp ecx,eax
    c.jbe("legacy")
    c.raw("8bd1")  # mov edx,ecx
    c.raw("2bd0")  # sub edx,eax
    c.raw("81fa" + struct.pack("<I", MAX_FRAME_DISTANCE).hex())
    c.ja("legacy")
    c.cmp_ecx_imm(0x4, OUTER_PARENT_RETURN)
    c.jne("legacy")

    # Pin the actual recursive producer CALL shape and its count relationship.
    c.cmp_ebp_imm(0x8, -1)
    c.je("legacy")
    c.cmp_ebp_imm(0xC, TARGET_SLOT)
    c.jne("legacy")
    c.cmp_eax_imm(0x10, OUTER_COUNT)
    c.jne("legacy")
    c.raw("8b5010")  # mov edx,[eax+0x10]
    c.raw("42")      # inc edx
    c.raw("395510")  # cmp [ebp+0x10],edx
    c.jne("legacy")
    c.cmp_ebp_imm(0x14, INNER_ARG4)
    c.jne("legacy")
    c.cmp_ebp_imm(0x18, INNER_ARG5)
    c.jne("legacy")

    c.raw("5a59589d6a02")  # pop edx; pop ecx; pop eax; popfd; push marker 2
    c.call(PUSH_MARKER)
    c.jmp(CONTINUE)

    c.label("legacy")
    c.raw("5a59589d")  # pop edx; pop ecx; pop eax; popfd
    c.jmp(OP19UI)
    return c.finish()
