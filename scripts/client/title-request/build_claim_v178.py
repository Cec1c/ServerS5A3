"""Apply only the title UI entry fix to the exact published guild V178 image.

The shared builder retains its default V177 contract. This entry selects the
fixed V178 digest and preserves every byte outside title extension/size/checksum.
It does not publish, deploy or start the game.
"""
import argparse
import json
from pathlib import Path

from build_claim_v177 import V178_SHA256, build as shared_build, require
from pe_support import sha256


def build(data):
    candidate, receipt = shared_build(data, baseline_sha256=V178_SHA256)
    receipt['builtBy'] = 'build_claim_v178.py'
    receipt['inputRelease'] = 'client-v178-guild-placement-20261026'
    return candidate, receipt


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--receipt', type=Path, required=True)
    args = parser.parse_args()
    source, output, report = (p.resolve() for p in (args.input,args.output,args.receipt))
    require(len({source,output,report}) == 3, 'input and output paths must be distinct')
    require(not output.exists() and not report.exists(), 'refusing to overwrite outputs')
    candidate, receipt = build(source.read_bytes())
    output.parent.mkdir(parents=True, exist_ok=True)
    report.parent.mkdir(parents=True, exist_ok=True)
    with output.open('xb') as stream: stream.write(candidate)
    with report.open('x', encoding='utf8') as stream: json.dump(receipt,stream,indent=2)
    require(sha256(source.read_bytes()) == V178_SHA256, 'input changed')
    require(sha256(output.read_bytes()) == receipt['outputSha256'], 'output readback differs')
    print(json.dumps(dict(outputSha256=receipt['outputSha256'],changedBytes=receipt['diff']['changedBytes'])))


if __name__ == '__main__':
    main()
