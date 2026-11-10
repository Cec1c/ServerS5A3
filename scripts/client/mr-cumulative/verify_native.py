"""Verify the local cumulative candidate without executing a game process."""
from pathlib import Path
import argparse
import importlib.util
import json
import struct
import subprocess
import sys

HERE = Path(__file__).resolve().parent
sys.path[:0] = [str(HERE), str(HERE.parent), str(HERE.parent / "local-patches/verify")]
import build_candidate as patch
import verify_imeup_all_keys
import verify_wedding_receive_admission as native


def verify(base, buff_base, candidate_dir, evidence_dir):
    before = base.read_bytes()
    admission, expected, receipt = patch.build(before)
    after = (candidate_dir / "DNF.exe").read_bytes()
    if after != expected or (candidate_dir / "DNF-admission.exe").read_bytes() != admission:
        raise ValueError("exact reviewed candidate files required")
    targets = {(0, n) for n in patch.CLASS0} | {(1, n) for n in patch.CLASS1}
    original, candidate = native.Native(before), native.Native(after)
    deltas, matrices = [], []
    for classification in (0, 1, 2):
        old = original.batch(classification, range(65536), patch.FALLBACK)
        new = candidate.batch(classification, range(65536), patch.ENTRY)
        for opcode, (a, b) in enumerate(zip(struct.iter_unpack("<I", old), struct.iter_unpack("<I", new))):
            if b[0] != int(a[0] or (classification, opcode) in targets):
                raise AssertionError((classification, opcode, a, b))
            if a != b:
                deltas.append((classification, opcode))
        matrices.append(dict(classification=classification, before=patch.sha(old), after=patch.sha(new)))
        print("native_matrix_complete", classification, flush=True)
    native.OLD = patch.FALLBACK
    samples = sorted(set(patch.CLASS0 + patch.CLASS1 + (0, 1, 1010, 1054, 1474, 1497, 65535)))
    abi = 0
    for classification in (0, 1, 2, 3, 127, 254, 255):
        for opcode in samples:
            old, _, _ = original.call_site(classification, opcode)
            new, events, _ = candidate.call_site(classification, opcode)
            if (classification, opcode) in targets:
                if new[native.UC_X86_REG_EAX] != 1 or events:
                    raise AssertionError("target admission did not return true directly")
            elif old != new or len(events) != 1:
                raise AssertionError("non-target fallback or ABI changed")
            abi += 1
    ime = verify_imeup_all_keys.verify(candidate_dir / "DNF-admission.exe", candidate_dir / "DNF.exe")
    if not ime["passed"] or (0, 1010) in deltas:
        raise AssertionError("unexpected admission or keyup verification failure")
    # Retain the exact paired Buff row/sender blocks and hooks, then execute
    # its independent existing verifier against this final cumulative image.
    spec = importlib.util.spec_from_file_location("buff_patch", HERE.parent / "buff-group-registration/build_candidate.py")
    buff = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(buff)
    blocks = buff.payload(0x6F888B0)
    image = patch.Image(after)
    for address, body in blocks:
        if image.read(address, len(body)) != body:
            raise AssertionError("paired Buff payload changed")
    buff_output = evidence_dir / "buff-native"
    buff_output.mkdir(parents=True, exist_ok=False)
    paired = subprocess.run([sys.executable, str(HERE.parent / "buff-group-registration/verify_native.py"),
                             "--base", str(buff_base), "--candidate-dir", str(base.parent),
                             "--candidate-image", str(candidate_dir / "DNF.exe"),
                             "--output-dir", str(buff_output)], capture_output=True, text=True)
    if paired.returncode:
        raise AssertionError("paired Buff native validation failed: " + paired.stderr[-2000:])
    buff_result = json.loads((buff_output / "native-group-verification.json").read_text("utf-8"))
    if not buff_result["passed"] or buff_result["exeSHA256"] != patch.OUTPUT_SHA:
        raise AssertionError("paired Buff native evidence identity differs")
    return dict(passed=True, inputSHA256=patch.INPUT_SHA, outputSHA256=patch.OUTPUT_SHA,
                nativeGateCalls=3 * 65536 * 2, matrices=matrices,
                actualAddedPairs=deltas, actualAddedPairCount=len(deltas), abiSamples=abi,
                keyup=ime, pairedBuffBlocksPreserved=True, pairedBuffNativeCases=len(buff_result["cases"]),
                layout=receipt, gameExecuted=False,
                limitations=["Native admission/ABI and keyup paths use synthetic headers and stack.",
                             "Game windows, network and gameplay require user acceptance."])


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base", type=Path, required=True)
    parser.add_argument("--buff-base", type=Path, required=True)
    parser.add_argument("--candidate-dir", type=Path, required=True)
    parser.add_argument("--report", type=Path, required=True)
    args = parser.parse_args()
    if args.report.exists():
        parser.error("preserve existing report")
    result = verify(args.base, args.buff_base, args.candidate_dir, args.report.parent)
    args.report.parent.mkdir(parents=True, exist_ok=True)
    with args.report.open("x", encoding="utf-8") as stream:
        json.dump(result, stream, ensure_ascii=False, indent=2)
        stream.write("\n")
    print(json.dumps(dict(passed=result["passed"], nativeGateCalls=result["nativeGateCalls"],
                          abiSamples=result["abiSamples"], actualAddedPairCount=result["actualAddedPairCount"])))


if __name__ == "__main__":
    main()
