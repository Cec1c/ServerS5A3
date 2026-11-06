"""按精确版本顺序补齐婚姻接收准入，只写独立候选，不安装或启动游戏。"""
import argparse
import hashlib
import json
from pathlib import Path

import patch_wedding_receive_admission as initial
import patch_wedding_full_admission as full
import patch_ring_notice_admission as ring

STAGES = (initial, full, ring)
INPUTS = tuple(stage.INPUT_SHA256 for stage in STAGES)
OUTPUT_SHA256 = ring.OUTPUT_SHA256
INPUT_SIZE = initial.INPUT_SIZE
CLASS0 = tuple(sorted(set(initial.CLASS0 + full.CLASS0 + (1001,))))
CLASS1 = tuple(sorted(set(initial.CLASS1 + full.CLASS1)))
ALREADY_ALLOWED = frozenset(((0, 997), (0, 1000)))
TARGETS = frozenset((0, op) for op in CLASS0) | frozenset((1, op) for op in CLASS1)
ADDITIONS = TARGETS - ALREADY_ALLOWED


def sha(data):
    return hashlib.sha256(data).hexdigest()


def require(ok, message):
    if not ok:
        raise ValueError(message)


def build(data):
    digest = sha(data)
    require(len(data) == INPUT_SIZE and digest in INPUTS,
            "仅接受说明中3个精确输入；拒绝未知、变动或已完成的版本")
    start = INPUTS.index(digest)
    current, reports = data, []
    for stage in STAGES[start:]:
        current, report = stage.build(current)
        require(sha(current) == stage.OUTPUT_SHA256, "阶段输出不符")
        reports.append(report)
    require(sha(current) == OUTPUT_SHA256, "最终输出不符")
    return current, dict(kind="wedding_receive_complete", input_sha256=digest,
        output_sha256=OUTPUT_SHA256, input_size=len(data), output_size=len(current),
        stages=reports, final_class0=list(CLASS0), final_class1=list(CLASS1),
        explicit_pairs=len(TARGETS), newly_admitted_from_A5=len(ADDITIONS),
        already_allowed=[[0, 997], [0, 1000]], already_allowed_not_patched=[[1, 1054]],
        unused_not_admitted=[[0, 1010]], added_dll=False, interface_modified=False,
        installed=False, game_launched=False, gameplay_acceptance=False)


def write_candidate(source, destination, receipt=None):
    source, destination = Path(source).resolve(), Path(destination).resolve()
    receipt = Path(receipt).resolve() if receipt is not None else None
    require(source != destination and not destination.exists(), "禁止原位覆盖或覆盖既有候选")
    require(receipt is None or (receipt not in (source, destination) and not receipt.exists()),
            "禁止覆盖输入、候选或既有回执")
    original = source.read_bytes()
    candidate, report = build(original)
    require(sha(source.read_bytes()) == report["input_sha256"], "输入在准备期间改变")
    destination.parent.mkdir(parents=True, exist_ok=True)
    with destination.open("xb") as stream:
        stream.write(candidate)
    require(sha(destination.read_bytes()) == OUTPUT_SHA256 and
            sha(source.read_bytes()) == report["input_sha256"], "输入或输出读回不符")
    if receipt is not None:
        receipt.parent.mkdir(parents=True, exist_ok=True)
        with receipt.open("x", encoding="utf-8") as stream:
            json.dump(report, stream, ensure_ascii=False, indent=2)
            stream.write("\n")
    return report


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--receipt", type=Path)
    args = parser.parse_args()
    print(json.dumps(write_candidate(args.input, args.output, args.receipt), ensure_ascii=False, indent=2))
