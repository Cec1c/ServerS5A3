"""Explicit-PID, read-only room-registration capture; never calls target code."""
import argparse
import ctypes as C
import ctypes.wintypes as W
import hashlib
import json
import os
from pathlib import Path
import struct
import time
from datetime import datetime, timezone


SUPPORTED = {
    '244b5c1e3126967d8a2b718658c586c1ac675a08b255d7377e95b93b4b815112',
    "735ed3e96f178bd2b546be18ff38ba4a578c951092b1d97a352ab73db7333d8b",
    "52223a060313b352fe43e718bc813ccb6824ea07913579f097d699dd21a91a1e",
    "3fef7a071333b25e9086fcc79174b58998aa092978d414258a7c5709d61b5e29",
    "8d38d4afef31292a5544a93fcd53a418fcb1fd1a4cd9e21ef6d3a4333e81c227",
}
# Fixed-base images only; allowlisting does not claim deployment or gameplay acceptance.
SIGNATURES = {
    0x32B4970: bytes.fromhex("8b81c0000000c3"),  # controller -> current mode
    0x370DC10: bytes.fromhex("8b81c4000000c3"),  # room -> registry
    0x10F1D10: bytes.fromhex("8b81f8040000c3"),  # wrapper -> dungeon actor
    0x2879C90: bytes.fromhex("558bec8b4110568b710c"),  # registry membership vector
    0x3C1BEA0: bytes.fromhex("8d4130c3"),  # wrapper -> inline profile
    0xE90A50: bytes.fromhex("80790900750332c0c383795c010f94c0c3"),
}
# Distinguish the supported live variants even if DNF.exe on disk was
# replaced after this PID started. These are RX sites/padding in all three.
VARIANT_RANGES = ((0x2017D97, 6), (0x6F88100, 0x200), (0x9988F5, 5),
                  (0x6F88300, 0x100), (0x2100913, 7), (0x6F88400, 28),
                  (0x2523A13, 5), (0x6F88500, 33), (0x1FC0E3A, 2))


def disk_code(image, address, size):
    pe = struct.unpack_from("<I", image, 0x3C)[0]
    section_count = struct.unpack_from("<H", image, pe + 6)[0]
    optional_size = struct.unpack_from("<H", image, pe + 20)[0]
    base = struct.unpack_from("<I", image, pe + 24 + 28)[0]
    if base != 0x400000:
        raise ValueError("Unexpected preferred image base")
    for index in range(section_count):
        entry = pe + 24 + optional_size + 40 * index
        rva, raw_size, raw_offset = struct.unpack_from("<III", image, entry + 12)
        offset = address - base - rva
        if 0 <= offset and offset + size <= raw_size:
            return image[raw_offset + offset:raw_offset + offset + size]
    raise ValueError("Code range is not backed by the pinned PE image")


def now():
    return datetime.now(timezone.utc).isoformat()


class ProcessReader:
    def __init__(self, pid, expected_path, image):
        self.k = C.WinDLL("kernel32", use_last_error=True)
        self.k.OpenProcess.argtypes = [W.DWORD, W.BOOL, W.DWORD]
        self.k.OpenProcess.restype = W.HANDLE
        self.k.CloseHandle.argtypes = [W.HANDLE]
        self.k.ReadProcessMemory.argtypes = [W.HANDLE, C.c_void_p, C.c_void_p, C.c_size_t, C.POINTER(C.c_size_t)]
        self.k.QueryFullProcessImageNameW.argtypes = [W.HANDLE, W.DWORD, W.LPWSTR, C.POINTER(W.DWORD)]
        self.k.GetProcessTimes.argtypes = [W.HANDLE, C.POINTER(W.FILETIME), C.POINTER(W.FILETIME), C.POINTER(W.FILETIME), C.POINTER(W.FILETIME)]
        self.handle = self.k.OpenProcess(0x1010, False, pid)  # QUERY_LIMITED_INFORMATION | VM_READ
        if not self.handle:
            raise OSError(C.get_last_error(), "OpenProcess read-only failed")
        try:
            path = C.create_unicode_buffer(32768)
            size = W.DWORD(len(path))
            if not self.k.QueryFullProcessImageNameW(self.handle, 0, path, C.byref(size)):
                raise OSError(C.get_last_error(), "Cannot verify process image")
            if Path(path.value).resolve() != expected_path:
                raise ValueError("PID image does not match --exe")
            times = [W.FILETIME() for _ in range(4)]
            if not self.k.GetProcessTimes(self.handle, *(C.byref(t) for t in times)):
                raise OSError(C.get_last_error(), "Cannot read process identity")
            self.created_filetime = times[0].dwHighDateTime << 32 | times[0].dwLowDateTime
            if self.read(0x400000, 2) != b"MZ":
                raise ValueError("Expected fixed-base PE image is absent")
            for address, expected in SIGNATURES.items():
                if self.read(address, len(expected)) != expected:
                    raise ValueError("Live code signature mismatch at " + hex(address))
            for address, size in VARIANT_RANGES:
                if self.read(address, size) != disk_code(image, address, size):
                    raise ValueError("Live client variant differs from disk at " + hex(address))
        except BaseException:
            self.close()
            raise

    def close(self):
        if self.handle:
            self.k.CloseHandle(self.handle)
            self.handle = None

    def read(self, address, size):
        if not (0x10000 <= address < 0xFFFF0000 and 0 < size <= 4096 and address + size <= 0xFFFF0000):
            raise ValueError("Read bounds rejected")
        data = C.create_string_buffer(size)
        count = C.c_size_t()
        if not self.k.ReadProcessMemory(self.handle, C.c_void_p(address), data, size, C.byref(count)) or count.value != size:
            raise OSError(C.get_last_error(), "Unreadable address " + hex(address))
        return data.raw

    def u32(self, address):
        return struct.unpack("<I", self.read(address, 4))[0]


def passive_getter(reader, obj, function, kind):
    """Decode a small pure-getter whitelist, never emulate or invoke arbitrary code."""
    code = reader.read(function, 16)
    result = {"function": hex(function), "bytes": code.hex(), "known": False}
    if kind == "pointer" and code[:2] in (b"\x8b\x81", b"\x8d\x81") and code[6:7] == b"\xc3":
        offset = struct.unpack("<i", code[2:6])[0]
        if 0 <= offset <= 0x100000:
            result.update(known=True, offset=offset, value=reader.u32(obj + offset) if code[0] == 0x8B else obj + offset)
    elif kind == "pointer" and code[:2] in (b"\x8b\x41", b"\x8d\x41") and code[3:4] == b"\xc3":
        offset = struct.unpack("b", code[2:3])[0]
        if offset >= 0:
            result.update(known=True, offset=offset, value=reader.u32(obj + offset) if code[0] == 0x8B else obj + offset)
    elif kind == "boolean" and code[:3] == b"\xb0\x01\xc3":
        result.update(known=True, value=True)
    elif kind == "boolean" and code[:3] in (b"\x32\xc0\xc3", b"\x33\xc0\xc3"):
        result.update(known=True, value=False)
    # Calls, branches, thunks, writes and complex methods remain unknown.
    return result


def capture(reader):
    r = reader
    result = {"atomic": False, "modelVisibleVerified": False}
    # One NOTI7 no-new-window predicate, not proof of a received notification.
    try:
        gate = r.u32(0x565CEA8)
        flag = r.read(gate + 9, 1)[0] if gate else 0
        state = r.u32(gate + 0x5C) if gate else 0
        stable = gate == r.u32(0x565CEA8)
        if gate:
            stable = stable and flag == r.read(gate + 9, 1)[0] and state == r.u32(gate + 0x5C)
        result["invitationRestriction"] = dict(
            manager=hex(gate), rawFlag9=flag, rawState5C=state, sampledStable=stable,
            wouldSkipNewMode0Window=bool(flag and state == 1) if stable else None,
            notificationBranchObserved=False,
        )
    except (OSError, ValueError) as error:
        result["invitationRestriction"] = {"readError": str(error), "notificationBranchObserved": False}
    controller = r.u32(0x56A2250)  # 1fa2790 -> 251e810; not the unrelated 56a3758 global
    mode = r.u32(controller + 0xC0) if controller else 0
    manager = r.u32(0x56A3768)
    party = r.u32(0x57B9080) if controller else 0
    result.update(controller=hex(controller), mode=hex(mode), activeManager=hex(manager), placementParty=hex(party))
    if party:
        slot_lookup = r.u32(r.u32(party) + 0x3C)
        result["placementSlotLookup"] = {"function": hex(slot_lookup), "bytes": r.read(slot_lookup, 32).hex(), "executed": False}
    wrappers = struct.unpack("<8I", r.read(manager + 0x48, 32)) if manager else (0,) * 8
    rows = []
    for slot, wrapper in enumerate(wrappers):
        row = {"slot": slot, "wrapper": hex(wrapper)}
        if wrapper:
            try:
                row.update(characterKey=struct.unpack("<H", r.read(wrapper + 0x20, 2))[0], actor=hex(r.u32(wrapper + 0x4F8)))
                # NOTI9 new-wrapper assignments 201703E/201704B/2017062.
                # Level at profile+14 is encoded; do not label raw bytes a level.
                identity = r.read(wrapper + 0x34, 12)
                job, grow, awakening = struct.unpack("<III", identity)
                row["profileIdentity"] = dict(job=job, grow=grow, awakening=awakening)
            except (OSError, ValueError) as error:
                row["readError"] = str(error)
        rows.append(row)
    result["activeManagerSlots"] = rows
    result["placementSlotsEquivalentToActiveManager"] = "unverified"
    if mode:
        vtable = r.u32(mode)
        result["modeVtable"] = hex(vtable)
        result["placementModeGate"] = passive_getter(r, mode, r.u32(vtable + 0x104), "boolean")
        room_getter = passive_getter(r, mode, r.u32(vtable + 0x60), "pointer")
        result["roomGetter"] = room_getter
        if room_getter["known"] and room_getter["value"]:
            room = room_getter["value"]
            registry = r.u32(room + 0xC4)
            result.update(room=hex(room), registry=hex(registry))
            if registry:
                header = r.read(registry + 0xC, 8)
                begin, end = struct.unpack("<II", header)
                size = end - begin
                if size < 0 or size % 4 or size > 4096:
                    result["registryStatus"] = "invalid_or_over_1024_entries"
                else:
                    contents = r.read(begin, size) if size else b""
                    values = struct.unpack("<" + "I" * (size // 4), contents)
                    stable = header == r.read(registry + 0xC, 8) and registry == r.u32(room + 0xC4)
                    stable = stable and (not size or contents == r.read(begin, size))
                    result.update(registryStatus="sampled", registryHeaderStable=stable, registeredActorPointers=[hex(v) for v in values])
                    for row in rows:
                        actor = int(row.get("actor", "0"), 16)
                        row["registrationObserved"] = bool(actor and actor in values) if stable else None
    # These checks detect some concurrent transitions, not ABA or mutation of
    # vector contents between reads. No process suspension is used.
    result["scenePointersStable"] = controller == r.u32(0x56A2250) and (not controller or mode == r.u32(controller + 0xC0))
    result["managerSlotsStable"] = manager == r.u32(0x56A3768) and (not manager or wrappers == struct.unpack("<8I", r.read(manager + 0x48, 32)))
    for row in rows:
        if "profileIdentity" in row:
            identity = row["profileIdentity"]
            try:
                latest = struct.unpack("<III", r.read(int(row["wrapper"], 16) + 0x34, 12))
                stable = result["managerSlotsStable"] and latest == (identity["job"], identity["grow"], identity["awakening"])
                identity["sampledStable"] = stable
                identity["outsideNativeJobTable"] = identity["job"] > 15 if stable else None
            except (OSError, ValueError) as error:
                identity.update(sampledStable=False, outsideNativeJobTable=None, readError=str(error))
        if "actor" in row:
            row["actorPointerStable"] = int(row["actor"], 16) == r.u32(int(row["wrapper"], 16) + 0x4F8)
            if not row["actorPointerStable"]:
                row["registrationObserved"] = None
    if not result["scenePointersStable"] or not result["managerSlotsStable"]:
        for row in rows:
            row["registrationObserved"] = None
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--pid", type=int, required=True)
    parser.add_argument("--exe", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--seconds", type=float, default=0, help="0: one sample; otherwise at most 30 seconds")
    args = parser.parse_args()
    if os.name != "nt" or not 0 <= args.seconds <= 30 or not 0 < args.pid <= 0xFFFFFFFF:
        parser.error("Windows, a positive PID and 0..30 seconds are required")
    exe = args.exe.resolve(strict=True)
    if args.output.exists():
        parser.error("Output already exists; refusing to overwrite evidence")
    image = exe.read_bytes()
    digest = hashlib.sha256(image).hexdigest()
    if digest not in SUPPORTED:
        parser.error("Unsupported EXE digest; no process opened")
    reader = ProcessReader(args.pid, exe, image)
    try:
        evidence = {"startedAt": now(), "pid": args.pid, "diskExeSHA256": digest, "liveVariantRangesMatched": True, "fullLiveImageHashed": False, "createdFiletime": reader.created_filetime, "accessMask": "0x1010", "readOnly": True, "modelVisibleVerified": False, "samples": []}
        deadline = time.monotonic() + args.seconds
        while True:
            try:
                sample = capture(reader)
            except (OSError, ValueError) as error:
                sample = {"readError": str(error), "modelVisibleVerified": False}
            evidence["samples"].append({"at": now(), "state": sample})
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                break
            time.sleep(min(0.1, remaining))
        evidence["finishedAt"] = now()
        evidence["diskImageUnchanged"] = hashlib.sha256(exe.read_bytes()).hexdigest() == digest
        with args.output.open("x", encoding="utf8") as output:
            json.dump(evidence, output, ensure_ascii=False, indent=2)
        print(json.dumps({"path": str(args.output.resolve()), "samples": len(evidence["samples"]), "readOnly": True}))
    finally:
        reader.close()


if __name__ == "__main__":
    main()
