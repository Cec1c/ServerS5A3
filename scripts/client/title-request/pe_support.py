"""Fixed v170 PE32 parsing and memory-floor helpers for title requests."""
import ctypes
import hashlib
import struct
from pathlib import Path


IMAGE_BASE = 0x400000
INPUT_SHA256 = "e5751354901fc142324d11cfc717e4a958396877f1c0fc545f51645f8e77af55"
INPUT_SIZE = 111910912
FLOOR_BYTES = 3 * 1024**3


class MemoryStatus(ctypes.Structure):
    _fields_ = [
        ("length", ctypes.c_ulong),
        ("load", ctypes.c_ulong),
    ] + [
        (name, ctypes.c_ulonglong)
        for name in (
            "total",
            "available",
            "page_total",
            "page_available",
            "virtual_total",
            "virtual_available",
            "extended",
        )
    ]


def sha256(data):
    return hashlib.sha256(data).hexdigest()


def available_memory():
    status = MemoryStatus()
    status.length = ctypes.sizeof(status)
    if not ctypes.windll.kernel32.GlobalMemoryStatusEx(ctypes.byref(status)):
        raise RuntimeError("GlobalMemoryStatusEx failed")
    if status.available < FLOOR_BYTES:
        raise RuntimeError(
            f"available physical memory below 3 GiB: {status.available} bytes"
        )
    return status.available


class Image:
    @classmethod
    def from_bytes(cls, data):
        image = cls.__new__(cls)
        image.path = Path("<memory>")
        image.data = data
        image.sha256 = sha256(data)
        image._parse()
        return image

    def __init__(self, path, expected_sha=None, expected_size=None):
        self.path = path
        self.data = path.read_bytes()
        self.sha256 = sha256(self.data)
        if expected_sha is not None and self.sha256 != expected_sha:
            raise ValueError(f"unexpected image SHA256 {self.sha256}")
        if expected_size is not None and len(self.data) != expected_size:
            raise ValueError(f"unexpected image length {len(self.data)}")

        self._parse()

    def _parse(self):
        data = self.data

        pe = struct.unpack_from("<I", data, 0x3C)[0]
        if data[:2] != b"MZ" or data[pe : pe + 4] != b"PE\0\0":
            raise ValueError("not a PE image")
        self.optional = pe + 24
        if (
            struct.unpack_from("<H", data, pe + 4)[0] != 0x14C
            or struct.unpack_from("<H", data, self.optional)[0] != 0x10B
            or struct.unpack_from("<I", data, self.optional + 28)[0] != IMAGE_BASE
        ):
            raise ValueError("not the expected fixed x86 PE32 image")
        self.size_of_image = struct.unpack_from(
            "<I", data, self.optional + 56
        )[0]
        self.section_table = self.optional + struct.unpack_from("<H", data, pe + 20)[0]
        self.section_count = struct.unpack_from("<H", data, pe + 6)[0]
        self.sections = []
        for index in range(self.section_count):
            header = self.section_table + 40 * index
            name = data[header : header + 8].rstrip(b"\0").decode(
                "ascii", "replace"
            )
            virtual_size, rva, raw_size, raw = struct.unpack_from(
                "<IIII", data, header + 8
            )
            characteristics = struct.unpack_from("<I", data, header + 36)[0]
            self.sections.append(
                dict(
                    index=index,
                    header=header,
                    name=name,
                    va=IMAGE_BASE + rva,
                    virtual_size=virtual_size,
                    raw_size=raw_size,
                    raw=raw,
                    characteristics=characteristics,
                )
            )

    def section(self, name):
        found = [section for section in self.sections if section["name"] == name]
        if len(found) != 1:
            raise ValueError(f"expected exactly one {name} section")
        return found[0]

    def file_offset(self, va, size=1):
        for section in self.sections:
            low = section["va"]
            high = low + section["raw_size"]
            if low <= va and va + size <= high:
                return section["raw"] + va - low
        raise ValueError(f"address {va:#x} outside file-backed sections")

    def bytes_at(self, va, size):
        offset = self.file_offset(va, size)
        return self.data[offset : offset + size]


def pe_checksum(data, field):
    words = memoryview(data).cast("H")
    value = sum(words) - words[field // 2] - words[field // 2 + 1]
    while value >> 16:
        value = (value & 0xFFFF) + (value >> 16)
    return (value + len(data)) & 0xFFFFFFFF
