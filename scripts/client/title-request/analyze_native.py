"""Read-only proof for the title OP19 producer call sites in fixed v170."""
import argparse
import json
import struct
from pathlib import Path

from capstone import CS_ARCH_X86, CS_MODE_32, Cs

from pe_support import INPUT_SHA256, INPUT_SIZE, Image, available_memory


PRODUCER = 0x25F9A70
OP19UI = 0x6E95000
CURRENT_WHITELIST = (0x2499E10, 0x249A344, 0x249A3DF, 0x249A455)
CURRENT_STACK_CHECKED = (0x249C880, 0x2605F6D)
TITLE_CALLS = {
    0x12A3474: {
        "containing_function": 0x12A3450,
        "parent_returns": (0x12AEF1B, 0x12AF00E, 0x12AB1CD),
        "origin": "12A3450 title equipment helper called by UI control-event handlers",
    },
    0x12AB194: {
        "containing_function": 0x12AAE00,
        "parent_returns": (0x12AE884,),
        "origin": "12AAE00 title action branch from the current UI event handler",
    },
    0x12ACB2B: {
        "containing_function": 0x12ABEA0,
        "parent_returns": (0x12AE59F,),
        "origin": "12ABEA0 title action branch, action kind 2, from the current UI handler",
    },
    0x12ACB78: {
        "containing_function": 0x12ABEA0,
        "parent_returns": (0x12AE59F,),
        "origin": "12ABEA0 title action branch, action kind 1, from the current UI handler",
    },
    0x12AF918: {
        "containing_function": 0x12AF840,
        "parent_returns": (),
        "origin": "12AF840 selected-title action; exact UI callback return (no local frame)",
    },
}


def callers(image, target):
    result = []
    for section in image.sections:
        if not section["characteristics"] & 0x20000000:
            continue
        blob = image.data[
            section["raw"] : section["raw"] + section["raw_size"]
        ]
        cursor = 0
        while True:
            cursor = blob.find(b"\xe8", cursor)
            if cursor < 0 or cursor + 5 > len(blob):
                break
            rel = struct.unpack_from("<i", blob, cursor + 1)[0]
            if section["va"] + cursor + 5 + rel == target:
                result.append(section["va"] + cursor)
            cursor += 1
    return result


def function_start(image, address, span=0x4000):
    section = next(
        section
        for section in image.sections
        if section["va"] <= address < section["va"] + section["raw_size"]
    )
    raw = image.data[section["raw"] : section["raw"] + section["raw_size"]]
    at = address - section["va"]
    blob = raw[max(0, at - span) : at + 1]
    pos = blob.rfind(b"\x55\x8b\xec")
    return None if pos < 0 else section["va"] + max(0, at - span) + pos


def disasm(image, address, size):
    md = Cs(CS_ARCH_X86, CS_MODE_32)
    return [
        f"{instruction.address:08x} {instruction.bytes.hex():<24} "
        f"{instruction.mnemonic} {instruction.op_str}"
        for instruction in md.disasm(image.bytes_at(address, size), address)
    ]


def rtti_name(image, table_start):
    try:
        locator = struct.unpack("<I", image.bytes_at(table_start - 4, 4))[0]
        fields = struct.unpack("<5I", image.bytes_at(locator, 20))
        blob = image.bytes_at(fields[3], 320)
        marker = blob.find(b"?AV")
        return blob[marker:].split(b"\0")[0].decode("ascii", "replace") if marker >= 0 else None
    except Exception:
        return None


def vtable_classes(image, function):
    result = []
    needle = struct.pack("<I", function)
    for section in image.sections:
        blob = image.data[
            section["raw"] : section["raw"] + section["raw_size"]
        ]
        cursor = 0
        while True:
            cursor = blob.find(needle, cursor)
            if cursor < 0:
                break
            pointer_va = section["va"] + cursor
            found = None
            for back in range(0, 0x2000, 4):
                table_start = pointer_va - back
                if table_start < section["va"] + 4:
                    break
                name = rtti_name(image, table_start)
                if name:
                    found = {
                        "pointer_va": f"{pointer_va:#x}",
                        "vtable_start": f"{table_start:#x}",
                        "rtti": name,
                    }
                    break
            if found:
                result.append(found)
            cursor += 4
    return result


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--image", type=Path, required=True)
    parser.add_argument("--evidence-dir", type=Path, required=True)
    args = parser.parse_args()
    image_path = args.image
    available = available_memory()
    image = Image(image_path, INPUT_SHA256, INPUT_SIZE)
    evidence = args.evidence_dir
    evidence.mkdir(parents=True, exist_ok=True)
    windows = {
        "op19ui-6e95000": (OP19UI, 0x1D2),
        "producer-25f9a70": (PRODUCER, 0x760),
        "title-helper-12a3450": (0x12A3450, 0x30),
        "title-event-12aee80": (0x12AEE80, 0x300),
        "title-event-12ae660": (0x12AE660, 0x240),
        "title-event-12afa60": (0x12AFA60, 0x18),
    }
    for name, (address, size) in windows.items():
        (evidence / (name + ".asm")).write_text(
            "\n".join(disasm(image, address, size)) + "\n", encoding="utf-8"
        )

    producer_callers = callers(image, PRODUCER)
    call_records = []
    for address in producer_callers:
        start = function_start(image, address)
        call_records.append(
            {
                "call_va": f"{address:#x}",
                "containing_prologue": None if start is None else f"{start:#x}",
                "window": "\n".join(disasm(image, address - 48, 80)),
            }
        )

    report = {
        "schema": "add07-title-request-static-v1",
        "source": str(image_path),
        "sha256": image.sha256,
        "size": len(image.data),
        "availableBytes": available,
        "protectionFloorGiB": 3,
        "producer": f"{PRODUCER:#x}",
        "op19ui": {
            "va": f"{OP19UI:#x}",
            "instructionCount": len(disasm(image, OP19UI, 0x1D2)),
            "currentSimpleWhitelist": [f"{x:#x}" for x in CURRENT_WHITELIST],
            "currentStackChecked": [f"{x:#x}" for x in CURRENT_STACK_CHECKED],
            "manifestMarkerSite": "0x6e951c6 push 2",
            "fallbackSite": "0x6e95052 push 0",
        },
        "titleCalls": [
            {
                "call_va": f"{address:#x}",
                "containing_prologue": (
                    f"{details['containing_function']:#x}"
                ),
                "parentReturns": [f"{x:#x}" for x in details["parent_returns"]],
                "origin": details["origin"],
                "directCallerInCurrentWhitelist": address in CURRENT_WHITELIST,
            }
            for address, details in TITLE_CALLS.items()
        ],
        "producerDirectCallers": call_records,
        "vtableClasses": {
            f"{fn:#x}": vtable_classes(image, fn)
            for fn in (0x12AEE80, 0x12AE660, 0x12AFA60, 0x12AF670)
        },
        "classification": {
            "user_title_paths": [f"{x:#x}" for x in TITLE_CALLS],
            "login_reassert_policy": (
                "No non-title producer caller is reclassified. Appended checks match "
                "only the five direct title returns, destination slot 13, count 1, and "
                "the statically proven parent-return whitelist where an EBP frame exists."
            ),
            "limitation": (
                "The client does not expose a runtime manual-origin bit before the "
                "producer. Parent returns distinguish the known title control-event "
                "paths from unrelated producer callers; they do not prove which UI "
                "click produced a historical packet."
            ),
        },
    }
    (evidence / "static.json").write_text(
        json.dumps(report, indent=2) + "\n", encoding="utf-8"
    )
    print(json.dumps(report, indent=2))


if __name__ == "__main__":
    main()
