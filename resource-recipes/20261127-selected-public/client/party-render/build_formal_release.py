#!/usr/bin/env python3
"""Fresh fixed-output FE6 build from retained accepted predecessor binaries."""
import argparse
import hashlib
import json
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile


HERE = Path(__file__).resolve().parent
LINEAGE = HERE / "lineage"
EXPECTED = {
    "c2": "C2DDF1006EF710BA7D379924C59A3F1D1C2FC98E619F5900A09A0FFA9E7BE2FF",
    "retained_47e": "47E557D5F6249F3FE526D7BAACB95EC05306EC43AF6CBCF86AFA0708F452CDCD",
    "weapon_045ff": "045FF05FC947062F417A4068F086509852C8FC3CCE85048D9D04A4D7AFFDBEB7",
    "d798": "D798B52293F8994926B2574DB3DEE075F07136A2C5096636D42F2844F8B82F1B",
    "party_d777": "D7774894A291D3596F2F3B5C3833BAFDECC9BA5C7EB95EA5E2EE98EC2A6135DA",
    "final_fe6a": "FE6A606F53FF4A496C56E78395D3288C897FBDC5A245441718414796C896BCE0",
}
EXPECTED_SIZE = {
    "c2": 114032640,
    "retained_47e": 113037312,
    "weapon_045ff": 114032640,
    "d798": 114032640,
}


def digest(path):
    with Path(path).open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest().upper()


def checked_file(path, key):
    path = Path(path).resolve(strict=True)
    if path.is_symlink() or not path.is_file():
        raise SystemExit(f"{key} is not a regular file")
    if path.stat().st_size != EXPECTED_SIZE[key] or digest(path) != EXPECTED[key]:
        raise SystemExit(f"{key} accepted predecessor identity differs")
    return path


def run(argv):
    result = subprocess.run([sys.executable, *map(str, argv)], cwd=HERE,
                            stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    if result.returncode:
        raise SystemExit((result.stdout + result.stderr).decode("utf-8", "replace"))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--c2", type=Path, required=True,
                        help="accepted production c2 client")
    parser.add_argument("--retained-47e", type=Path, required=True,
                        help="accepted retained pre-weapon-skin client")
    parser.add_argument("--weapon-045ff", type=Path, required=True,
                        help="accepted Weapon Skin client")
    parser.add_argument("--d798", type=Path, required=True,
                        help="accepted owner0 client; this binary is required")
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--receipt", type=Path, required=True)
    args = parser.parse_args()

    inputs = {
        "c2": checked_file(args.c2, "c2"),
        "retained_47e": checked_file(args.retained_47e, "retained_47e"),
        "weapon_045ff": checked_file(args.weapon_045ff, "weapon_045ff"),
        "d798": checked_file(args.d798, "d798"),
    }
    output = args.output.resolve()
    receipt_path = args.receipt.resolve()
    if output.exists() or output.is_symlink():
        raise SystemExit("output already exists; preserve it and use a new path")
    if receipt_path.exists() or receipt_path.is_symlink():
        raise SystemExit("receipt already exists; preserve it and use a new path")
    output.parent.mkdir(parents=True, exist_ok=True)
    receipt_path.parent.mkdir(parents=True, exist_ok=True)

    with tempfile.TemporaryDirectory(prefix="five-session-client-") as temp_name:
        temp = Path(temp_name)
        lineage_report = temp / "lineage-audit.json"
        run([LINEAGE / "lineage_audit.py",
             "--c2", inputs["c2"], "--retained-47e", inputs["retained_47e"],
             "--weapon-045ff", inputs["weapon_045ff"], "--d798", inputs["d798"],
             "--out", lineage_report])
        reconstructed_d798 = temp / "D798.exe"
        run([LINEAGE / "apply_c2_to_d798_ranges.py",
             "--c2", inputs["c2"], "--accepted-d798", inputs["d798"],
             "--manifest", temp / "reconstructed-ranges.json",
             "--output", reconstructed_d798])
        if digest(reconstructed_d798) != EXPECTED["d798"]:
            raise SystemExit("accepted range replay does not reproduce D798")

        party = temp / "DNF-party-v4.exe"
        party_receipt = temp / "party-v4-receipt.json"
        run([HERE / "build_party_v4.py", "--input", inputs["d798"],
             "--output", party, "--receipt", party_receipt])
        if digest(party) != EXPECTED["party_d777"]:
            raise SystemExit("party v4 output differs from D7774894")
        run([HERE / "verify_party_v4.py", "--candidate", party,
             "--baseline", inputs["d798"], "--build-receipt", party_receipt])

        final = temp / "DNF-final.exe"
        final_receipt = temp / "blackscreen-receipt.json"
        verify_report = temp / "blackscreen-verify.json"
        run([HERE / "build_blackscreen.py", "--input", party,
             "--output", final, "--receipt", final_receipt])
        if digest(final) != EXPECTED["final_fe6a"]:
            raise SystemExit("black-screen output differs from FE6A606F")
        run([HERE / "verify_blackscreen.py", "--candidate", final,
             "--baseline", party, "--build-receipt", final_receipt,
             "--report", verify_report])
        receipt = {
            "schema": 1,
            "status": "fresh-output-sealed",
            "inputs": {name: {"sha256": digest(path), "bytes": path.stat().st_size}
                       for name, path in inputs.items()},
            "rangeReplay": {"sha256": digest(reconstructed_d798),
                            "lineageAuditSha256": digest(lineage_report)},
            "partyV4": {"sha256": digest(party), "receiptSha256": digest(party_receipt)},
            "final": {"sha256": digest(final), "bytes": final.stat().st_size,
                      "buildReceiptSha256": digest(final_receipt),
                      "verifyReportSha256": digest(verify_report)},
            "buildScripts": {
                name: digest(HERE / name) for name in
                ("build_party_v4.py", "build_blackscreen.py",
                 "verify_party_v4.py", "verify_blackscreen.py")
            },
            "freshProcessExecuted": True,
            "gameExecuted": False,
            "deployed": False,
        }
        if receipt["final"]["sha256"] != EXPECTED["final_fe6a"]:
            raise SystemExit("sealed final identity differs")
        with output.open("xb") as stream:
            with final.open("rb") as source:
                shutil.copyfileobj(source, stream, 4 << 20)
            stream.flush()
        if digest(output) != EXPECTED["final_fe6a"]:
            raise SystemExit("output disk readback differs")
        receipt["output"] = {"sha256": digest(output), "bytes": output.stat().st_size}
        with receipt_path.open("x", encoding="utf-8") as stream:
            json.dump(receipt, stream, ensure_ascii=False, indent=2)
            stream.write("\n")
    print(json.dumps(receipt, ensure_ascii=False))


if __name__ == "__main__":
    main()
