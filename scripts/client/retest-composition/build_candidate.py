"""Compose independently verified title, party, HP and receive-gate patches."""
from pathlib import Path
import argparse
import array
import hashlib
import json
import os
import struct
import subprocess
import sys

BASE_SHA256 = 'c97fec768b7178ae89446a65de5aa980a1532724e3a1bde49e5273fe5aff0db6'
RESULT_SHA256 = '2a6cc14f3353e3d9d142b8580e432fe76ff3c1bbdc5be6818092347f9003bc74'
SIZE = 111919104
ROOT = Path(__file__).resolve().parents[3]
PARTS = {
    'title': 'scripts/client/title-request/repair_published.py',
    'peer': 'scripts/client/dungeon-peer-retire/repair_published.py',
    'hp': 'scripts/client/ordinary-party-hud/build_hp_realtime.py',
    'receivers': 'scripts/client/retest-receiver-gate/build_class_gate.py',
}


def require(ok, message):
    if not ok:
        raise ValueError(message)


def sha(data):
    return hashlib.sha256(data).hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input', required=True, type=Path)
    parser.add_argument('--output', required=True, type=Path)
    parser.add_argument('--work-dir', required=True, type=Path)
    parser.add_argument('--receipt', required=True, type=Path)
    args = parser.parse_args()
    source = args.input.resolve()
    output = args.output.resolve()
    receipt_path = args.receipt.resolve()
    work = args.work_dir.resolve()
    require(source != output and source != receipt_path and output != receipt_path, 'paths must be distinct')
    require(all(path != work and work not in path.parents for path in (source, output, receipt_path)),
            'input, output and receipt must be outside the new work directory')
    require(not output.exists() and not receipt_path.exists() and not work.exists(), 'output already exists')
    baseline = source.read_bytes()
    require(len(baseline) == SIZE and sha(baseline) == BASE_SHA256, 'input is not exact published V175')
    work.mkdir(parents=True)
    merged = bytearray(baseline)
    checksum_at = struct.unpack_from('<I', baseline, 0x3c)[0] + 24 + 64
    owners = {}
    parts = []
    env = dict(os.environ, PYTHONUTF8='1', PYTHONDONTWRITEBYTECODE='1')
    for name, relative in PARTS.items():
        script = ROOT / relative
        variant = work / (name + '.exe')
        evidence = work / (name + '.json')
        input_args = [str(source)] if name == 'receivers' else ['--input', str(source)]
        result = subprocess.run([sys.executable, '-B', str(script), *input_args,
                                 '--output', str(variant), '--receipt', str(evidence)],
                                env=env, capture_output=True, timeout=180)
        (work / (name + '.log')).write_bytes(result.stdout + result.stderr)
        require(result.returncode == 0, name + ' builder failed; retained log has details')
        value = variant.read_bytes()
        proof = json.loads(evidence.read_text(encoding='utf8'))
        expected = proof['output']['sha256'] if name == 'receivers' else proof['outputSha256']
        require(len(value) == SIZE and sha(value) == expected, name + ' result differs from receipt')
        if name == 'receivers':
            require(proof['hashVerified'] and not proof['bitmapTouched'], 'receiver scope changed')
        changes = []
        for offset, (before, after) in enumerate(zip(baseline, value)):
            if before == after or checksum_at <= offset < checksum_at + 4:
                continue
            require(offset not in owners, 'independent patches overlap at ' + hex(offset))
            owners[offset] = name
            merged[offset] = after
            changes.append(offset)
        parts.append(dict(name=name, source=relative, sourceSha256=sha(script.read_bytes()),
                          outputSha256=expected, changedOffsets=changes))
    struct.pack_into('<I', merged, checksum_at, 0)
    words = array.array('H', merged)
    if sys.byteorder != 'little':
        words.byteswap()
    checksum = sum(words)
    while checksum >> 16:
        checksum = (checksum & 0xffff) + (checksum >> 16)
    struct.pack_into('<I', merged, checksum_at, (checksum + len(merged)) & 0xffffffff)
    require(sha(merged) == RESULT_SHA256, 'composition differs from the verified native candidate')
    require(sha(source.read_bytes()) == BASE_SHA256, 'input changed during build')
    with output.open('xb') as stream:
        stream.write(merged)
    require(sha(output.read_bytes()) == RESULT_SHA256, 'written candidate changed')
    receipt = dict(passed=True, inputSha256=BASE_SHA256, outputSha256=RESULT_SHA256,
                   length=SIZE, parts=parts, gameAcceptance=False)
    with receipt_path.open('x', encoding='utf8') as stream:
        json.dump(receipt, stream, ensure_ascii=False, indent=2)
    print(json.dumps(dict(passed=True, outputSha256=RESULT_SHA256, length=SIZE)))


if __name__ == '__main__':
    main()
