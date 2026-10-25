"""Corrected local x86 helpers for exact v166 dungeon op9 call sites.

Record layout established from the original parser:
  record+0x70 = op9 server route
  record+0x74 = op9 ownerChannel
  record+0x04 = kind3 target sceneObjectKey

The current dungeon namespace is route=1, owner=0. These helpers therefore
require [record+0x70]==1 and [record+0x74]==0. They are emitted experiments;
they do not build or install an EXE.
"""
from __future__ import annotations

import struct

BASE = 0x33000000
REMOVE_BASE = BASE
KIND0_BASE = BASE + 0x200


class Code:
    def __init__(self, base: int):
        self.base = base
        self.buf = bytearray()
        self.labels: dict[str, int] = {}
        self.fixups: list[tuple[int, int, str]] = []

    def raw(self, text: str) -> None:
        self.buf.extend(bytes.fromhex(text.replace(" ", "")))

    def label(self, name: str) -> None:
        if name in self.labels:
            raise ValueError(f"duplicate label: {name}")
        self.labels[name] = len(self.buf)

    def branch(self, opcode: str, target: str) -> None:
        self.raw(opcode)
        offset = len(self.buf)
        self.buf.extend(b"\0\0\0\0")
        self.fixups.append((offset, 4, target))

    def call(self, target: int | str) -> None:
        if isinstance(target, str):
            self.branch("e8", target)
        else:
            self.buf.append(0xE8)
            self.buf.extend(struct.pack("<i", target - (self.base + len(self.buf) + 4)))

    def jump(self, target: int | str) -> None:
        if isinstance(target, str):
            self.branch("e9", target)
        else:
            self.buf.append(0xE9)
            self.buf.extend(struct.pack("<i", target - (self.base + len(self.buf) + 4)))

    def je(self, target: str) -> None:
        self.branch("0f84", target)

    def jne(self, target: str) -> None:
        self.branch("0f85", target)

    def ja(self, target: str) -> None:
        self.branch("0f87", target)

    def finish(self) -> bytes:
        for offset, size, label in self.fixups:
            destination = self.labels[label]
            struct.pack_into("<i", self.buf, offset, destination - offset - size)
        return bytes(self.buf)


def emit(base=BASE, preserve_local=False, preserve_dungeon_town=False):
    if preserve_dungeon_town and not preserve_local:
        raise ValueError("town retirement requires local model preservation")
    remove_base, kind0_base = base, base + 0x200
    # thiscall(record,slot,FFFF), replacing only the kind3 handler call.
    c = Code(remove_base)
    c.raw("55 8bec 53 56 8bf1")
    c.raw("837e7001")                 # route must be 1
    c.jne("original")
    c.raw("837e7400")                 # owner must be 0
    if preserve_dungeon_town:
        c.je("context_ok")
        # A surviving ordinary-dungeon actor can still be associated with
        # the pre-entry town record. Only its current route/channel may use
        # the non-destructive cleanup; unrelated contexts retain native code.
        c.call(0x1FA2790)
        c.raw("8bc8")
        c.call(0x2526B50)
        c.raw("83f803")
        c.jne("original")
        c.raw("8b0d58376a05 85c9")
        c.je("original")
        c.call(0x296DD80)
        c.raw("85c0")
        c.je("original")
        c.raw("8bc8")
        c.call(0x2964680)
        c.raw("0fb6c0 85c0")
        c.je("original")
        c.raw("394674")
        c.jne("original")
        c.label("context_ok")
    else:
        c.jne("original")
    c.raw("817d0cffff0000")           # outgoing key must be FFFF
    c.jne("original")
    c.raw("8b5d08 83fb07")
    c.ja("original")
    c.raw("8b449e2c 3dffff0000")
    c.je("original")
    c.raw("0fb74e04 3bc1")            # P+4 is the kind3 target key
    if preserve_local:
        c.jne("preserve_member")
        # Retiring the local party record must not destroy the local actor's
        # drawable. The local player may still be fighting in this instance.
        # Compare the actual local actor, not a packet slot or leader bit.
        c.raw("8b0d58376a05 85c9")
        c.je("original")
        c.call(0x296DD80)
        c.raw("85c0")
        c.je("original")
        c.raw("0fb74020 0fb74e04 3bc1")
        c.jne("original")
        c.label("preserve_member")
    else:
        c.je("original")

    # A different member appears in the retiring record's roster. Clear only
    # its record slot/association, never its shared active actor.
    c.raw("53 8bce")
    c.call(0x29730E0)
    c.raw("85c0")
    c.je("clear_slot")
    c.raw("8b549e2c 0fb74820 3bca")
    c.jne("clear_slot")
    c.raw("8bc8")
    c.call(0x29614D0)
    c.raw("397014")                   # actor+0x4C8 == this record?
    c.jne("clear_slot")
    c.raw("c7401400000000")
    c.label("clear_slot")
    c.raw("c7449e2cffff0000")
    c.jump("done")

    c.label("original")
    c.raw("ff750c ff7508 8bce")
    c.call(0x29733B0)
    c.label("done")
    c.raw("5e 5b 8be5 5d c20800")
    remove = c.finish()

    # thiscall(record,slot,key), wrapping the original kind0 setter and then
    # filling only a null actor->record association.
    c = Code(kind0_base)
    c.raw("55 8bec 53 56 8bf1 ff750c ff7508 8bce")
    c.call(0x29733B0)
    c.raw("837e7001")                 # route must be 1
    c.jne("done")
    c.raw("837e7400")                 # owner must be 0
    c.jne("done")
    c.raw("8b5d08 83fb07")
    c.ja("done")
    c.raw("817d0cffff0000")
    c.je("done")
    c.raw("8b449e2c 3b450c")
    c.jne("done")
    c.raw("53 8bce")
    c.call(0x29730E0)
    c.raw("85c0")
    c.je("done")
    c.raw("0fb74820 3b4d0c")
    c.jne("done")
    c.raw("8bc8")
    c.call(0x29614D0)
    c.raw("83781400")
    c.jne("done")
    c.raw("897014")
    c.label("done")
    c.raw("5e 5b 8be5 5d c20800")
    kind0 = c.finish()
    return remove, kind0


