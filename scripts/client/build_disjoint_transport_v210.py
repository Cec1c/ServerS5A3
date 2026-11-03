"""Build the cumulative V210 candidate from the exact V208 input."""
import argparse
import hashlib
import json
from pathlib import Path

from build_luke_upgrade_v209 import build as v209
from patch_game_tcp_buffers import build as tcp
from patch_disjoint_epic_display import build as display

OUTPUT_SHA256 = '24372bf2df57cdaecc696752299dbe8c61e222806a697e3d1c6f57fbcaffbdae'


def build(data):
    middle, inherited = v209(data)
    middle, transport = tcp(middle)
    output, ui = display(middle)
    if hashlib.sha256(output).hexdigest() != OUTPUT_SHA256:
        raise ValueError('cumulative V210 output changed')
    return output, dict(outputSha256=OUTPUT_SHA256, size=len(output), v209=inherited,
                       transport=transport, display=ui, playerAcceptance='pending')


if __name__ == '__main__':
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--input', type=Path, required=True)
    p.add_argument('--output', type=Path, required=True)
    a = p.parse_args()
    if a.input.resolve() == a.output.resolve() or a.output.exists():
        raise ValueError('output must be a separate new file')
    raw = a.input.read_bytes()
    output, receipt = build(raw)
    with a.output.open('xb') as stream:
        stream.write(output)
    if a.input.read_bytes() != raw or a.output.read_bytes() != output:
        raise RuntimeError('input/output readback mismatch')
    print(json.dumps(receipt, indent=2))
