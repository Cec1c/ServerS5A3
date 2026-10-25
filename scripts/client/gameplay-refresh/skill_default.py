"""Fixed-image op19 default-command repair; no invented command encoding.

The native skill-info receiver always calls the custom-command setter, whose
empty-vector check returns without registering anything. A profession change
has already cleared/rebuilt the actor's command manager. At the empty-field
branch, serialize the skill's own PVF default container using the same native
serializer as the command UI's reset-to-default action. Explicit field4 values
continue through the original copy loop, including Dark Knight root payloads.
"""
import struct

BASE_SHA256 = "e5751354901fc142324d11cfc717e4a958396877f1c0fc545f51645f8e77af55"
SITE = 0x2020B0D
ORIGINAL = bytes.fromhex("0f8e41010000")
COPY = 0x2020B13
APPLY = 0x2020C54
SERIALIZE = 0x295B360


def helper(address):
    code = bytearray()
    def rel(op, target):
        code.extend(op)
        code.extend(struct.pack("<i", target - (address + len(code) + 4)))
    # Flags come from cmp [ebp-28h],edi (edi=0), immediately before SITE.
    rel(b"\x0f\x8f", COPY)
    code.extend(bytes.fromhex("8d8554ffffff50"))  # output vector at ebp-ac
    code.extend(bytes.fromhex("8d862810000050"))  # actual skill default container
    rel(b"\xe8", SERIALIZE)                     # native thiscall, ret 8
    rel(b"\xe9", APPLY)
    return bytes(code)


def redirect(address):
    return b"\xe9" + struct.pack("<i", address-SITE-5) + b"\x90"
