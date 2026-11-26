"""Build the ported weapon-skin + skin-warehouse client DNF.exe on the exact hash-bound cumulative input."""
import argparse
import gzip
import os
import hashlib
import json
import shutil
import struct
import subprocess
from pathlib import Path
import pefile
from capstone import Cs, CS_ARCH_X86, CS_MODE_32

HERE = Path(__file__).resolve().parent
SRC_ROOT = HERE / "client-source"
PORT_ROOT: Path
BUILD_DIR: Path
BASE_EXE: Path
DELTA_GZ = HERE / "weapon-skin-95.exe-delta.gz"
OUT_DIR: Path
VCVARS: Path

EXPECTED_BASE_SHA256 = "2111a6ea61395a51ed96ef5b3701f78e1b61b638d56c23f9168493ccb2df935d"
IMAGE_BASE = 0x400000
DLL_BASE = 0x072A0000
CATALOG_VA = 0x071F1000
CATALOG_EXPECTED_SIZE = 713600

# Slot 1 .bpiat verified IAT addresses (0x06f21018..0x06f21074)
IAT_MAP = {
    "0x6ee5018": "0x6f21018",   # ReleaseSRWLockExclusive
    "0x6ee501c": "0x6f2101c",   # AcquireSRWLockExclusive
    "0x6ee5024": "0x6f21024",   # GetCurrentProcessId
    "0x6ee5028": "0x6f21028",   # GetCurrentThreadId
    "0x6ee5030": "0x6f21030",   # GetTickCount
    "0x6ee5038": "0x6f21038",   # VirtualQuery
    "0x6ee5040": "0x6f21044",   # GetWindow
    "0x6ee5044": "0x6f21048",   # IsWindowVisible
    "0x6ee5048": "0x6f2104c",   # RegisterWindowMessageW
    "0x6ee504c": "0x6f21050",   # PostMessageW
    "0x6ee5050": "0x6f21054",   # CallWindowProcW
    "0x6ee5054": "0x6f21058",   # GetWindowThreadProcessId
    "0x6ee5058": "0x6f2105c",   # SetTimer
    "0x6ee505c": "0x6f21060",   # KillTimer
    "0x6ee5060": "0x6f21064",   # GetClientRect
    "0x6ee5064": "0x6f21068",   # SetWindowLongW
    "0x6ee5068": "0x6f2106c",   # EnumWindows
    "06ee5070h": "06f21074h",   # _except_handler3
    "0x6fa4018": "0x6f21020",   # GetCurrentProcess
    "0x6fa4024": "0x6f2102c",   # FlushInstructionCache
    "0x6fa402c": "0x6f21034",   # VirtualProtect
    "0x7014008": "0x6f21038",   # VirtualQuery (队友武器图层.cpp)
}

ROLE_VTABLE_DTORS = [
    (0x045d8404, 0x00993da0),
    (0x04683e3c, 0x00d92eb0),
    (0x046850e4, 0x00d9a120),
    (0x046ee6f4, 0x00febee0),
    (0x047408dc, 0x01341380),
    (0x0474996c, 0x01365680),
    (0x0474a8fc, 0x01366d40),
    (0x047ae45c, 0x0164e510),
    (0x047b67f4, 0x0166e510),
    (0x047b7734, 0x0166e670),
    (0x047babc4, 0x0167aaf0),
    (0x047bbb04, 0x0167b3e0),
    (0x047dbf0c, 0x0174c460),
    (0x047dce74, 0x0174c8b0),
    (0x0481741c, 0x01837db0),
    (0x0485c17c, 0x019b3290),
    (0x048b3d5c, 0x01bdf0b0),
    (0x04b44824, 0x02e05550),
    (0x04b462d4, 0x02e2dc70),
    (0x04b4b884, 0x02e88610),
    (0x04b54c6c, 0x02f15cb0),
    (0x04b5f58c, 0x02fa19d0),
    (0x04b66f24, 0x03001c90),
    (0x04b6cf04, 0x030b38d0),
    (0x04b6e68c, 0x030c5b70),
]


def must_replace(text: str, old: str, new: str, label: str) -> str:
    if old not in text:
        raise RuntimeError(f"Expected snippet not found in {label}: {old!r}")
    return text.replace(old, new)


def prepare_port_sources() -> None:
    if PORT_ROOT.exists():
        raise FileExistsError("Use a new work directory; prepared source already exists")
    shutil.copytree(SRC_ROOT, PORT_ROOT)

    # 1. Apply IAT address replacements across all files
    for p in PORT_ROOT.rglob("*"):
        if not p.is_file():
            continue
        txt = p.read_text("utf-8")
        orig = txt
        for old, new in IAT_MAP.items():
            txt = txt.replace(old, new)
        if txt != orig:
            p.write_text(txt, "utf-8")

    # 2. Adapt 同窗仓库接线.cpp
    ui_cpp = PORT_ROOT / r"同窗候选源码\同窗仓库接线.cpp"
    txt = ui_cpp.read_text("utf-8")
    txt = must_replace(
        txt,
        "constexpr uintptr_t stateAddress = 0x7089cd0;",
        'extern "C" {\n'
        "    volatile uint32_t g_ws_data_sentinel = 0x31565357u;\n"
        "    alignas(16) unsigned char g_ws_ui_state[0x130] = {};\n"
        "    alignas(16) unsigned char g_ws_scene_state[0x18c] = {};\n"
        "    alignas(16) unsigned char g_ws_party_deferred[0x80] = {};\n"
        "}",
        "同窗仓库接线.cpp state",
    )
    txt = must_replace(
        txt,
        "State& Data() { return *reinterpret_cast<State*>(stateAddress); }",
        "State& Data() { return *reinterpret_cast<State*>(g_ws_ui_state); }",
        "同窗仓库接线.cpp Data()",
    )
    txt = must_replace(txt, "push 070007a0h", "push 06f1ff10h", "WeaponBeginHook")
    txt = must_replace(txt, "push 07000cb0h", "push 06f202d0h", "WeaponReceiveHook")
    txt = must_replace(txt, "push 070009c0h", "push 032bea50h", "ForgeNpcHook")
    txt = must_replace(
        txt,
        'extern "C" void __cdecl ForgeNpcCaption(void* out,void* owner,unsigned command) {\n'
        "    reinterpret_cast<void (__cdecl*)(void*,void*,unsigned)>(0x7000ff0)(out,owner,command);",
        'extern "C" void __cdecl ForgeNpcCaption(void* out,void* owner,unsigned command) {',
        "ForgeNpcCaption",
    )
    caption_thunks = """
extern "C" __declspec(naked) void ForgeNpcCaptionTrampoline() {
    __asm {
        pushfd
        pushad
        mov eax, esp
        sub esp, 220h
        and esp, -16
        mov [esp+200h], eax
        mov edx, dword ptr fs:[34h]
        mov [esp+204h], edx
        fxsave [esp]
        cld
        mov ecx, [eax+1ch]
        cmp ecx, [eax+30h]
        jne skip_caption
        mov ecx, [eax+28h]
        push ecx
        mov ecx, [eax+24h]
        push ecx
        mov ecx, [eax+1ch]
        push ecx
        call ForgeNpcCaption
        add esp, 12
    skip_caption:
        fxrstor [esp]
        mov eax, [esp+204h]
        mov dword ptr fs:[34h], eax
        mov esp, [esp+200h]
        popad
        popfd
        lea esp, [esp+8]
        ret 4
    }
}

extern "C" __declspec(naked) void ForgeNpcCaptionThunk1() {
    __asm {
        push dword ptr [ebp-1b4h]
        push dword ptr [ebp-1cch]
        push dword ptr [esp+0ch]
        mov eax, 03854f50h
        call eax
        jmp ForgeNpcCaptionTrampoline
    }
}

extern "C" __declspec(naked) void ForgeNpcCaptionThunk2() {
    __asm {
        push edi
        lea eax, [esi-0dch]
        push eax
        push dword ptr [esp+0ch]
        mov eax, 03854f50h
        call eax
        jmp ForgeNpcCaptionTrampoline
    }
}
"""
    txt = must_replace(
        txt,
        'extern "C" void __cdecl PayloadEntry() {}',
        caption_thunks + '\nextern "C" void __cdecl PayloadEntry() {}',
        "PayloadEntry thunks",
    )
    ui_cpp.write_text(txt, "utf-8")

    # 3. Adapt 林纳斯制作窗.h (Slot 1's 0x32becc0 calls 0x6ef3fd1 instead of 0x3365ba0)
    forge_h = PORT_ROOT / r"同窗候选源码\林纳斯制作窗.h"
    txt = forge_h.read_text("utf-8")
    txt = must_replace(
        txt,
        "return reinterpret_cast<void* (__cdecl*)(int,void*)>(0x3365ba0)(id,owner);",
        "return reinterpret_cast<void* (__cdecl*)(int,void*)>(0x6ef3fd1)(id,owner);",
        "ForgeFactory fallback",
    )
    txt = must_replace(
        txt,
        "constexpr unsigned char original[] = {0xe8,0xdb,0x6e,0x0a,0x00};",
        "constexpr unsigned char original[] = {0xe8,0x0c,0x53,0xc3,0x03};",
        "ForgeSwapFactory original bytes",
    )
    forge_h.write_text(txt, "utf-8")

    # 4. Adapt 武器仓库绘制.cpp
    scene_cpp = PORT_ROOT / r"场景绘制\武器仓库绘制.cpp"
    txt = scene_cpp.read_text("utf-8")
    txt = must_replace(
        txt,
        "State& Data() {\n    return *reinterpret_cast<State*>(0x7089e00);\n}",
        'extern "C" unsigned char g_ws_scene_state[0x18c];\n'
        "static State& Data() {\n    return *reinterpret_cast<State*>(g_ws_scene_state);\n}",
        "武器仓库绘制.cpp Data()",
    )
    txt = must_replace(
        txt,
        "bool Readable(const void* address, size_t size) {",
        "static bool Readable(const void* address, size_t size) {",
        "武器仓库绘制.cpp Readable",
    )
    txt = must_replace(
        txt,
        'extern "C" void __cdecl PayloadEntry() {}',
        'extern "C" void __cdecl ScenePayloadEntry() {}',
        "武器仓库绘制.cpp PayloadEntry",
    )
    scene_cpp.write_text(txt, "utf-8")

    # 5. Adapt 队友武器图层.cpp
    party_cpp = PORT_ROOT / r"场景绘制\队友武器图层.cpp"
    txt = party_cpp.read_text("utf-8")
    txt = must_replace(
        txt,
        '#include "场景入口.h"',
        'extern "C" unsigned char g_ws_scene_state[0x18c];\n'
        'extern "C" unsigned char g_ws_party_deferred[0x80];\n'
        'extern "C" void __cdecl ApplySceneAppearance(Object*, Object*, const void*);\n'
        'extern "C" void __cdecl SelectAppearance(Object*, Object*, unsigned);',
        "队友武器图层.cpp externs",
    )
    txt = must_replace(
        txt,
        "bool Readable(const void* pointer, unsigned size) {",
        "static bool Readable(const void* pointer, unsigned size) {",
        "队友武器图层.cpp Readable",
    )
    txt = must_replace(
        txt,
        "Deferred& Queue() {\n    return *reinterpret_cast<Deferred*>(0x7089f8c);\n}",
        "static Deferred& Queue() {\n    return *reinterpret_cast<Deferred*>(g_ws_party_deferred);\n}",
        "队友武器图层.cpp Queue()",
    )
    txt = must_replace(
        txt,
        "Native<void (__cdecl*)(Object*,Object*,const void*)>(kApplySceneAppearance)(role,scene,appearance);",
        "ApplySceneAppearance(role,scene,appearance);",
        "队友武器图层.cpp PriorApply",
    )
    txt = must_replace(
        txt,
        "const auto& parse = *Native<const ParseState*>(0x7089e00);",
        "const auto& parse = *reinterpret_cast<const ParseState*>(g_ws_scene_state);",
        "队友武器图层.cpp parse",
    )
    txt = must_replace(
        txt,
        "Native<void (__cdecl*)(Object*,Object*,unsigned)>(kSelectAppearance)(role,entry,",
        "SelectAppearance(role,entry,",
        "队友武器图层.cpp SelectAppearance",
    )
    txt = must_replace(txt, "push 07111720h", "push 06ea1280h", "PartyMode1Hook")
    txt = must_replace(
        txt,
        "    existing:\n        popfd\n        push 07111760h\n        ret",
        "    existing:\n        popfd\n        mov al, byte ptr [ebp-351h]\n        ret",
        "PartyRosterHook",
    )
    party_cpp.write_text(txt, "utf-8")

    # Sanity check: zero remaining upstream addresses
    forbidden = ("6ee5", "6fa4", "7014", "7089", "7000", "7111", "7131")
    for p in PORT_ROOT.rglob("*"):
        if not p.is_file() or p.name == "场景入口.h":
            continue
        t = p.read_text("utf-8").lower()
        for tok in forbidden:
            if tok in t:
                raise RuntimeError(f"Leftover upstream address token {tok!r} in {p}")


def compile_and_link() -> tuple[bytes, dict[str, int]]:
    BUILD_DIR.mkdir(parents=True, exist_ok=True)
    vc = VCVARS
    vc_text = str(vc.resolve(strict=True))
    if any(c in vc_text for c in '\r\n"%&|<>^!'):
        raise ValueError("Unsupported shell metacharacter in vcvars path")
    dump = subprocess.check_output(['cmd.exe', '/d', '/u', '/c',
                                    f'call "{vc_text}" -vcvars_ver=14.44 >nul && set'])
    env = dict(
        line.split("=", 1)
        for line in dump.decode("utf-16le").splitlines()
        if "=" in line and not line.startswith("=")
    )
    cl = Path(env["VCToolsInstallDir"]) / "bin/Hostx86/x86/cl.exe"
    link = cl.with_name("link.exe")

    def run(args):
        proc = subprocess.run([str(x) for x in args], env=env, cwd=BUILD_DIR, capture_output=True)
        if proc.returncode != 0:
            print("STDOUT:", proc.stdout.decode("mbcs", "replace"))
            print("STDERR:", proc.stderr.decode("mbcs", "replace"))
            raise RuntimeError(f"Command failed ({proc.returncode}): {args[0]}")

    cflags = ["/nologo", "/c", "/O2", "/GS-", "/GR-", "/Zl", "/EHs-c-", "/std:c++17", "/utf-8"]
    run([cl, *cflags, "/I" + str(PORT_ROOT / "同窗候选源码"), PORT_ROOT / r"同窗候选源码\同窗仓库接线.cpp", "/Foui_warehouse.obj"])
    run([cl, *cflags, "/I" + str(PORT_ROOT / "场景绘制"), PORT_ROOT / r"场景绘制\武器仓库绘制.cpp", "/Foscene_draw.obj"])
    run([cl, *cflags, "/I" + str(PORT_ROOT / "场景绘制"), PORT_ROOT / r"场景绘制\队友武器图层.cpp", "/Foparty_layer.obj"])

    dll_path = BUILD_DIR / "WeaponSkin101.dll"
    map_path = BUILD_DIR / "WeaponSkin101.map"
    run([
        link, "/nologo", "/DLL", "/NOENTRY", "/NODEFAULTLIB", "/FIXED", "/DYNAMICBASE:NO",
        f"/BASE:0x{DLL_BASE:x}", "/MACHINE:X86", "/SAFESEH:NO",
        "/MERGE:.rdata=.text", "/SECTION:.text,ER",
        "/OPT:NOREF", "/OPT:NOICF",
        f"/MAP:{map_path.name}", f"/OUT:{dll_path.name}",
        "ui_warehouse.obj", "scene_draw.obj", "party_layer.obj",
    ])

    dll_bytes = dll_path.read_bytes()
    pe = pefile.PE(data=dll_bytes)
    assert not getattr(pe, "DIRECTORY_ENTRY_IMPORT", None), "DLL must have zero imports"
    assert pe.OPTIONAL_HEADER.DATA_DIRECTORY[5].Size == 0, "DLL must have zero relocations"
    assert pe.OPTIONAL_HEADER.ImageBase == DLL_BASE
    assert len(pe.sections) == 2, f"Expected 2 sections (.text, .data), got {[s.Name for s in pe.sections]}"

    symbols: dict[str, int] = {}
    for line in map_path.read_text(encoding="mbcs").splitlines():
        fields = line.split()
        if len(fields) >= 3 and ":" in fields[0] and fields[1][0] in "_@?":
            try:
                address = int(fields[2], 16)
            except ValueError:
                continue
            name = fields[1]
            if name[0] in "_@":
                name = name[1:]
            name = name.split("@")[0]
            symbols[name] = address
    (BUILD_DIR / "symbols.json").write_text(json.dumps(symbols, indent=2), encoding="utf-8")
    print(
        "Compiled & linked WeaponSkin101.dll:",
        [(s.Name.rstrip(b"\0").decode(), hex(DLL_BASE + s.VirtualAddress), hex(s.Misc_VirtualSize), hex(s.SizeOfRawData)) for s in pe.sections],
        hashlib.sha256(dll_bytes).hexdigest(),
    )
    return dll_bytes, symbols


def extract_catalog() -> bytes:
    recipe = gzip.open(DELTA_GZ, "rb").read()
    HEADER = struct.Struct("<8sII32s32sI")
    RECORD = struct.Struct("<II")
    magic, old_size, new_size, old_hash, new_hash, count = HEADER.unpack_from(recipe)
    assert magic == b"DNF90SK1"
    pos = HEADER.size
    catalog = None
    for idx in range(count):
        offset, size = RECORD.unpack_from(recipe, pos)
        pos += RECORD.size
        payload = recipe[pos : pos + size]
        pos += size
        if idx == 61:
            catalog = payload
            break
    assert catalog is not None and len(catalog) == CATALOG_EXPECTED_SIZE, f"Unexpected catalog size {len(catalog) if catalog else None}"
    for i in range(13329):
        source, wid, p0, p1, name_ptr, icon_ptr, frame, jobs = struct.unpack_from("<IIIIIIII", catalog, i * 32)
        assert CATALOG_VA <= name_ptr < CATALOG_VA + len(catalog), f"Bad name_ptr at {i}: {hex(name_ptr)}"
        assert CATALOG_VA <= icon_ptr < CATALOG_VA + len(catalog), f"Bad icon_ptr at {i}: {hex(icon_ptr)}"
    print(f"Verified WeaponCatalog: {len(catalog)} bytes, 13329 entries, sha256={hashlib.sha256(catalog).hexdigest()}")
    return catalog


def align(n: int, boundary: int) -> int:
    return (n + boundary - 1) // boundary * boundary


def build_exe(dll_bytes: bytes, symbols: dict[str, int], catalog: bytes) -> None:
    data = BASE_EXE.read_bytes()
    base_sha = hashlib.sha256(data).hexdigest()
    assert base_sha == EXPECTED_BASE_SHA256, f"Base EXE mismatch: {base_sha}"
    old = pefile.PE(data=data)
    lib = pefile.PE(data=dll_bytes)

    out = bytearray(data)
    edits = []

    def patch(address: int, before: bytes, after: bytes, name: str):
        off = old.get_offset_from_rva(address - IMAGE_BASE)
        assert len(before) == len(after), (name, hex(address))
        actual = bytes(out[off : off + len(before)])
        assert actual == before, f"Mismatch at {name} {hex(address)}: got {actual.hex()} want {before.hex()}"
        out[off : off + len(after)] = after
        edits.append(dict(address=hex(address), before=before.hex(), after=after.hex(), symbol=name))

    def call_site(address: int, prior_target: int, sym: str):
        patch(
            address,
            b"\xe8" + struct.pack("<i", prior_target - address - 5),
            b"\xe8" + struct.pack("<i", symbols[sym] - address - 5),
            sym,
        )

    def jmp_site(address: int, prior_bytes: bytes, sym: str):
        after = b"\xe9" + struct.pack("<i", symbols[sym] - address - 5) + b"\x90" * (len(prior_bytes) - 5)
        patch(address, prior_bytes, after, sym)

    def call_replace(address: int, prior_bytes: bytes, sym: str):
        after = b"\xe8" + struct.pack("<i", symbols[sym] - address - 5) + b"\x90" * (len(prior_bytes) - 5)
        patch(address, prior_bytes, after, sym)

    def ptr_site(address: int, prior_ptr: int, sym: str):
        patch(address, struct.pack("<I", prior_ptr), struct.pack("<I", symbols[sym]), sym)

    # === 1. UI / Warehouse / Forge hooks ===
    call_site(0x0113c28f, 0x0113bd90, "WarehouseRefreshLegacyFontRows")
    call_site(0x0113c29a, 0x0113bec0, "WarehouseLegacyFontHover")
    call_site(0x0113cfe7, 0x0113bd90, "WarehouseRefreshLegacyFontRows")
    call_site(0x0113e22d, 0x0113bd90, "WarehouseRefreshLegacyFontRows")
    patch(0x0113e2f4, bytes.fromhex("689600000068c8000000"), bytes.fromhex("68220000006810000000"), "WarehouseWindowPos")
    call_site(0x0113e30e, 0x039a6620, "WarehouseCaptionHook")
    call_replace(0x01d4c635, bytes.fromhex("807df3000f849a000000"), "ForgeBackpackShadeHook")
    call_replace(0x0249d761, bytes.fromhex("8b55e88d42fb"), "ForgeBackpackPickHook")
    call_replace(0x0249f589, bytes.fromhex("6a016a016a00475743538bceffd2"), "ForgeBackpackDrawHook")
    call_site(0x024a29fe, 0x032bea50, "WarehouseMenuOpen")
    call_site(0x024a2a14, 0x032b94a0, "WarehouseMenuClose")
    call_site(0x024a2a24, 0x032b55e0, "WarehouseMenuIsOpen")
    call_site(0x024e99be, 0x032bea50, "WarehouseMenuOpen")
    jmp_site(0x02523c8a, b"\xe9" + struct.pack("<i", 0x06f202d0 - 0x02523c8a - 5), "WeaponReceiveHook")
    call_replace(0x026038a2, bytes.fromhex("85db0f84da010000"), "WeaponRegisterRightClickHook")
    call_site(0x0354e8b1, 0x032bea50, "ForgeNpcHook")
    jmp_site(0x0374f280, b"\xe9" + struct.pack("<i", 0x06f1ff10 - 0x0374f280 - 5), "WeaponBeginHook")
    jmp_site(0x0382ab90, bytes.fromhex("558bec6aff"), "WeaponLoad")
    ptr_site(0x0471b528, 0x0113bfe0, "FontEvent")
    ptr_site(0x0471b534, 0x0109a730, "WarehouseDrawCharacter")
    ptr_site(0x049f03f4, 0x033618d0, "ForgeDrawPreview")

    # 5 native NPC caption call sites (replacing upstream .fcamp4 hook at 0x070010cb)
    for site in (0x03540ad5, 0x03540b15):
        call_site(site, 0x03854f50, "ForgeNpcCaptionThunk1")
    for site in (0x0353e9b5, 0x0353ea28, 0x0353ea73):
        call_site(site, 0x03854f50, "ForgeNpcCaptionThunk2")

    # === 2. Scene weapon drawing hooks ===
    jmp_site(0x022bdf29, bytes.fromhex("8d45d86a01"), "ObserveAvatarRowHook")
    call_site(0x022c5344, 0x022bde30, "ReadSceneAppearance")
    jmp_site(0x02724570, bytes.fromhex("558bec568bf1"), "SelectRenderSlotHook")
    call_site(0x027d07b4, 0x02726790, "AttachRealLayers")
    for vt_addr, orig_dtor in ROLE_VTABLE_DTORS:
        ptr_site(vt_addr, orig_dtor, "DestroySwordman")

    # === 3. Teammate weapon layer hooks ===
    call_replace(0x0201759f, bytes.fromhex("8a85affcffff"), "PartyRosterHook")
    call_site(0x022c407a, 0x06ea1280, "PartyMode1Hook")
    call_site(0x022c5fbe, 0x022c0d50, "PartyAppearanceHook")

    # === 4. Coalesce 4 adjacent RX section headers (.dshcard .. .cryssel) to free 3 header slots ===
    headers = []
    merged = []
    i = 0
    while i < len(old.sections):
        sec = old.sections[i]
        if sec.Name.rstrip(b"\0") == b".dshcard":
            group = old.sections[i : i + 4]
            assert [s.Name.rstrip(b"\0") for s in group] == [b".dshcard", b".op19ui", b".p33own", b".cryssel"]
            for first, second in zip(group, group[1:]):
                assert first.Characteristics == second.Characteristics == 0x60000020
                assert first.Misc_VirtualSize <= first.SizeOfRawData and second.Misc_VirtualSize <= second.SizeOfRawData
                assert first.VirtualAddress + first.SizeOfRawData == second.VirtualAddress
                assert first.PointerToRawData + first.SizeOfRawData == second.PointerToRawData
            hdr = bytearray(sec.__pack__())
            total_raw = sum(s.SizeOfRawData for s in group)
            struct.pack_into("<I", hdr, 8, total_raw)
            struct.pack_into("<I", hdr, 16, total_raw)
            headers.append(hdr)
            merged = [s.Name.rstrip(b"\0").decode() for s in group]
            i += 4
        else:
            headers.append(bytearray(sec.__pack__()))
            i += 1
    assert len(headers) == len(old.sections) - 3

    # Bridge .sgdata VSize cleanly up to .wscat (0x071f1000)
    assert old.sections[-1].Name.rstrip(b"\0") == b".sgdata"
    wscat_rva = CATALOG_VA - IMAGE_BASE
    last_rva = old.sections[-1].VirtualAddress
    last_end = last_rva + max(old.sections[-1].Misc_VirtualSize, old.sections[-1].SizeOfRawData)
    assert wscat_rva >= last_end
    struct.pack_into("<I", headers[-1], 8, wscat_rva - last_rva)

    file_align = old.OPTIONAL_HEADER.FileAlignment
    sec_align = old.OPTIONAL_HEADER.SectionAlignment
    added = []

    # Append .wscat at VA 0x071f1000 (VSize bridges to .wstext at 0x072a1000)
    wstext_rva = DLL_BASE + lib.sections[0].VirtualAddress - IMAGE_BASE
    cat_raw = catalog.ljust(align(len(catalog), file_align), b"\0")
    cat_vsize = wstext_rva - wscat_rva
    assert cat_vsize >= len(cat_raw)
    cat_off = align(len(out), file_align)
    out.extend(bytes(cat_off - len(out)))
    out.extend(cat_raw)
    headers.append(struct.pack("<8sIIIIIIHHI", b".wscat", cat_vsize, wscat_rva, len(cat_raw), cat_off, 0, 0, 0, 0, 0x40000040))
    added.append(dict(name=".wscat", va=hex(CATALOG_VA), vsize=hex(cat_vsize), raw=hex(len(cat_raw))))

    # Append .wstext and .wsdata from WeaponSkin101.dll
    end_rva = wscat_rva + cat_vsize
    for sec, name in zip(lib.sections, [b".wstext", b".wsdata"]):
        rva = DLL_BASE + sec.VirtualAddress - IMAGE_BASE
        assert rva >= end_rva, (name, hex(rva), hex(end_rva))
        off = align(len(out), file_align)
        out.extend(bytes(off - len(out)))
        raw = sec.get_data()
        raw = raw.ljust(align(len(raw), file_align), b"\0")
        out.extend(raw)
        headers.append(struct.pack("<8sIIIIIIHHI", name, sec.Misc_VirtualSize, rva, len(raw), off, 0, 0, 0, 0, sec.Characteristics))
        added.append(dict(name=name.decode(), va=hex(IMAGE_BASE + rva), vsize=hex(sec.Misc_VirtualSize), raw=hex(len(raw))))
        end_rva = rva + max(sec.Misc_VirtualSize, len(raw))

    assert len(headers) == len(old.sections)
    table = old.sections[0].get_file_offset()
    assert table + 40 * len(headers) <= old.OPTIONAL_HEADER.SizeOfHeaders
    for idx, hdr in enumerate(headers):
        out[table + 40 * idx : table + 40 * (idx + 1)] = hdr

    opt = old.OPTIONAL_HEADER.get_file_offset()
    struct.pack_into("<I", out, opt + 56, align(end_rva, sec_align))
    struct.pack_into("<I", out, opt + 4, old.OPTIONAL_HEADER.SizeOfCode + int(added[1]["raw"], 16))
    struct.pack_into("<I", out, opt + 8, old.OPTIONAL_HEADER.SizeOfInitializedData + int(added[0]["raw"], 16) + int(added[2]["raw"], 16))
    struct.pack_into("<I", out, opt + 64, 0)
    cand = pefile.PE(data=bytes(out))
    checksum = cand.generate_checksum()
    struct.pack_into("<I", out, opt + 64, checksum)

    # === 5. Exhaustive verification ===
    cand = pefile.PE(data=bytes(out))
    assert cand.verify_checksum(), "PE checksum verification failed"
    assert cand.OPTIONAL_HEADER.AddressOfEntryPoint == old.OPTIONAL_HEADER.AddressOfEntryPoint
    for idx in range(16):
        assert cand.OPTIONAL_HEADER.DATA_DIRECTORY[idx].__pack__() == old.OPTIONAL_HEADER.DATA_DIRECTORY[idx].__pack__()

    # Verify EVERY SINGLE BYTE of all original sections (allowing ONLY our explicit edits)
    for sec in old.sections:
        expected = bytearray(sec.get_data())
        for edit in edits:
            delta = int(edit["address"], 16) - IMAGE_BASE - sec.VirtualAddress
            if 0 <= delta < len(expected):
                rep = bytes.fromhex(edit["after"])
                expected[delta : delta + len(rep)] = rep
        actual = cand.get_data(sec.VirtualAddress, len(expected))
        assert actual == expected, f"Unexpected byte drift in section {sec.Name}"

    # Verify new sections
    assert cand.get_data(wscat_rva, len(catalog)) == catalog
    for sec in lib.sections:
        rva = DLL_BASE + sec.VirtualAddress - IMAGE_BASE
        assert cand.get_data(rva, sec.SizeOfRawData) == sec.get_data()

    # Verify Session #13's .xscene helper (0x06ec0380) and SwordGhost (.sgtext/.sgdata) are 100% untouched
    xscene_old = old.get_data(0x06ec0000 - IMAGE_BASE, 0x1000)
    xscene_new = cand.get_data(0x06ec0000 - IMAGE_BASE, 0x1000)
    assert xscene_old == xscene_new, ".xscene modified!"

    # Verify key hook site disassemblies
    md = Cs(CS_ARCH_X86, CS_MODE_32)
    print("\n=== Spot-Check Disassembly of Key Patched Sites ===")
    for label, va in [
        ("PartyRoster", 0x0201759f),
        ("PartyMode1", 0x022c407a),
        ("PartyAppearance", 0x022c5fbe),
        ("WeaponReceive", 0x02523c8a),
        ("ForgeNpc", 0x0354e8b1),
        ("ForgeNpcCaption1", 0x03540ad5),
        ("ForgeNpcCaption2", 0x0353e9b5),
        ("WeaponBegin", 0x0374f280),
        ("WeaponLoad", 0x0382ab90),
    ]:
        insns = list(md.disasm(cand.get_data(va - IMAGE_BASE, 10), va))[:2]
        print(f"  {label:18s} @ 0x{va:08x}: " + " ; ".join(f"{i.mnemonic} {i.op_str}" for i in insns))

    OUT_DIR.mkdir(parents=True, exist_ok=True)
    out_exe = OUT_DIR / "DNF.exe"
    out_exe.write_bytes(out)
    out_sha = hashlib.sha256(out).hexdigest()
    receipt = dict(
        baseSHA256=base_sha,
        outputSHA256=out_sha,
        outputSize=len(out),
        dllSHA256=hashlib.sha256(dll_bytes).hexdigest(),
        catalogSHA256=hashlib.sha256(catalog).hexdigest(),
        editCount=len(edits),
        mergedHeaders=merged,
        newSections=added,
        edits=edits,
        originalMappedBytesVerified=True,
        xsceneVerifiedIntact=True,
        peChecksumVerified=True,
    )
    (OUT_DIR / "build-receipt.json").write_text(json.dumps(receipt, ensure_ascii=False, indent=2), encoding="utf-8")
    print(f"\nSUCCESS! Built {out_exe} ({len(out)} bytes)")
    print(f"  baseSHA256   = {base_sha}")
    print(f"  outputSHA256 = {out_sha}")
    print(f"  edits        = {len(edits)}")


def main():
    p = argparse.ArgumentParser(description="Port the selected weapon warehouse onto exact 2111a6ea input")
    p.add_argument('--input', required=True, type=Path)
    p.add_argument('--work', required=True, type=Path, help="new scratch directory")
    p.add_argument('--output-dir', required=True, type=Path, help="new output directory")
    p.add_argument('--vcvars', required=True, type=Path, help="VS vcvars32.bat; MSVC 14.44 x86")
    a = p.parse_args()
    global BASE_EXE, PORT_ROOT, BUILD_DIR, OUT_DIR, VCVARS
    BASE_EXE, OUT_DIR, VCVARS = a.input, a.output_dir, a.vcvars
    if a.work.exists() or OUT_DIR.exists():
        raise FileExistsError("Work and output paths must both be new")
    if hashlib.sha256(BASE_EXE.read_bytes()).hexdigest() != EXPECTED_BASE_SHA256:
        raise ValueError("Base EXE SHA-256 differs")
    PORT_ROOT, BUILD_DIR = a.work/'port-source', a.work/'build'
    a.work.mkdir(parents=True)
    prepare_port_sources()
    dll_bytes, symbols = compile_and_link()
    catalog = extract_catalog()
    build_exe(dll_bytes, symbols, catalog)


if __name__ == "__main__":
    main()
