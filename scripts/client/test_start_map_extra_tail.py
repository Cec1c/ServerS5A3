"""Read-only replay of the registered op29 extra loop in two pinned clients.

Requires pefile and unicorn. Object allocation/vector/destruction are fixtures;
the loop, packet readers and memcpy execute original image instructions.
Optional --cases accepts locally supplied [{name, rows, extraAndTailHex}].
No raw packets or client image are included in this repository.
"""
from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path
import struct

import pefile
from unicorn import Uc, UC_ARCH_X86, UC_MODE_32, UC_HOOK_CODE
from unicorn.x86_const import (
    UC_X86_REG_EAX, UC_X86_REG_EBP, UC_X86_REG_EBX, UC_X86_REG_ECX,
    UC_X86_REG_EDI, UC_X86_REG_EFLAGS, UC_X86_REG_EIP, UC_X86_REG_ESI,
    UC_X86_REG_ESP,
)

CLIENTS = {
    "534b5ef166d0090d1ab88e01f5f443721abf97eabfacfb007e04e30fd9d51b24",
    "f8d68484a8f4b7b25afa7692c6ba011aab67c6d1928952934057d570615ce646",
}
START, STOP = 0x1FF96BE, 0x1FF9830
LOOP_SHA = "a9f407b14d0cbb3a85457d55225d764e0951b8934ebb4cc384b0ee9ae0663092"
PACKET, BP, CONTEXT = 0x20020000, 0x20010000, 0x20030000
CURSOR, FOG = 0x57F9AD0, 0x56A37EE
READERS = {0x37E82E0: 1, 0x37E8350: 2, 0x37E83C0: 4}


def require(condition, message):
    if not condition:
        raise AssertionError(message)


class Replay:
    def __init__(self, path):
        image = Path(path).read_bytes()
        self.digest = hashlib.sha256(image).hexdigest()
        require(self.digest in CLIENTS, "unsupported complete client SHA256")
        pe = pefile.PE(data=image, fast_load=True)
        require(pe.OPTIONAL_HEADER.ImageBase == 0x400000, "image base")
        read = lambda va, size: pe.get_data(va - 0x400000, size)
        require(read(0x2001F81, 16).hex() == "6a0068808dff016a1d8bcee80f235200", "op29 registration changed")
        require(read(0x20025AE, 12).hex() == "6a0068d0bbff016883010000", "op387 registration changed")
        require(hashlib.sha256(read(START, STOP-START)).hexdigest() == LOOP_SHA, "op29 loop changed")
        self.vm = Uc(UC_ARCH_X86, UC_MODE_32)
        self.vm.mem_map(0x400000, (pe.OPTIONAL_HEADER.SizeOfImage + 4095) & ~4095)
        self.vm.mem_write(0x400000, pe.get_memory_mapped_image())
        self.vm.mem_map(0x20000000, 0x40000)
        self.vm.hook_add(UC_HOOK_CODE, self.hook)

    def rd(self, address):
        return struct.unpack("<I", self.vm.mem_read(address, 4))[0]

    def wr(self, address, *values):
        self.vm.mem_write(address, struct.pack("<" + "I"*len(values), *values))

    def ret(self, value=0, clean=0):
        sp = self.vm.reg_read(UC_X86_REG_ESP)
        target = self.rd(sp)
        self.vm.reg_write(UC_X86_REG_EAX, value)
        self.vm.reg_write(UC_X86_REG_ESP, sp + 4 + clean)
        self.vm.reg_write(UC_X86_REG_EIP, target)

    def hook(self, vm, address, size, unused):
        if address in READERS:
            self.reads.append(READERS[address])
        if address == 0x2621750:
            sp = vm.reg_read(UC_X86_REG_ESP)
            row = [self.rd(sp + 4 + 4*i) for i in range(9)]
            row[-1] &= 0xFF
            self.rows.append(row)
            self.ret(vm.reg_read(UC_X86_REG_ECX), 36)
        elif address == 0x1FF8CC0:
            self.ret(0, 4)
        elif address == 0x37DF540:
            self.ret()

    def run(self, name, body, expected, alignment, should_match):
        self.rows, self.reads = [], []
        self.vm.mem_write(BP-0x1000, bytes(0x1000))
        self.wr(BP-0x44C, CONTEXT)
        address = PACKET + alignment
        self.vm.mem_write(address, body + bytes(64))
        self.wr(CURSOR, address, len(body), 0, 0)
        for reg, value in ((UC_X86_REG_EBP, BP), (UC_X86_REG_ESP, BP-0x800),
                           (UC_X86_REG_EBX, 0), (UC_X86_REG_ESI, 0),
                           (UC_X86_REG_EDI, 0), (UC_X86_REG_EFLAGS, 0x202)):
            self.vm.reg_write(reg, value)
        self.vm.emu_start(START, STOP, count=200000)
        require(self.vm.reg_read(UC_X86_REG_EIP) == STOP, "native loop did not finish")
        require(self.vm.reg_read(UC_X86_REG_ESP) == BP-0x800, "unbalanced stack")
        consumed = self.rd(CURSOR) - address
        remaining = self.rd(CURSOR+4)
        fog = self.vm.mem_read(FOG, 1)[0]
        expected_cursor = 2 + 21*len(expected)
        match = (self.rows == expected and consumed == expected_cursor
                 and consumed <= len(body) and remaining == len(body)-consumed
                 and fog == body[expected_cursor-1])
        require(match == should_match, f"unexpected contract result: {name} {self.rows}")
        require(self.reads == [1] + [1,4,4,4,2,1,2,2,1]*len(expected) + [1], "reader sequence")
        return dict(name=name, alignment=alignment, rowCount=len(expected),
                    contractMatches=match, consumed=consumed, remaining=remaining,
                    fog=fog, decodedRows=self.rows, stackBalanced=True,
                    inputSHA256=hashlib.sha256(body).hexdigest())


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--exe", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--cases", type=Path)
    args = parser.parse_args()
    runner = Replay(args.exe)
    results = []
    for count in (0, 1, 4, 6):
        for flag in (0, 0x49):
            rows = [[i, 0x10200+i, 10158700, 1, 0x42, 3, 0x45, 0x47, flag] for i in range(count)]
            for width in (20, 21):
                body = bytes([count]) + b"".join(
                    struct.pack("<BIIIHBHHB", *row)[:width] for row in rows)
                # Nonempty ridable/tail bytes make overread observable, including one row.
                body += b"\x01\x01\x01" + struct.pack("<5I", 11,22,33,44,55) + b"\xff"
                for alignment in range(4):
                    results.append(runner.run(f"synthetic-{count}-{flag}-{width}", body, rows, alignment, width==21 or count==0))
    if args.cases:
        for case in json.loads(args.cases.read_text("utf-8")):
            body = bytes.fromhex(case["extraAndTailHex"])
            rows = case["rows"]
            require(body[0] == len(rows), "external row count")
            require(body[1:1+21*len(rows)] == b"".join(struct.pack("<BIIIHBHHB", *row) for row in rows), "external row bytes")
            for width in (21, 20):
                candidate = body if width==21 else (body[:1] + b"".join(body[1+i*21:21+i*21] for i in range(len(rows))) + body[1+21*len(rows):])
                results.append(runner.run(case["name"]+f"-{width}", candidate, rows, 0, width==21 or not rows))
    receipt = dict(passed=True, exeSHA256=runner.digest, loopSHA256=LOOP_SHA,
                   registration=dict(op29="0x1ff8d80", op387="0x1ffbbd0"), cases=results,
                   scope="Original op29 loop/readers/memcpy; constructor/vector/destructor fixtures. No item rendering, network, pickup transaction or in-game acceptance.")
    with args.output.open("x", encoding="utf-8") as stream:
        json.dump(receipt, stream, indent=2)
    print(json.dumps(dict(passed=True, cases=len(results), exeSHA256=runner.digest)))


if __name__ == "__main__":
    main()
