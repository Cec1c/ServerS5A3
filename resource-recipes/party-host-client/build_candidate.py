"""Build the paired host-completion and party-leader display client."""
from pathlib import Path
import argparse
import hashlib
import importlib.util
import json

ROOT = Path(__file__).resolve().parents[2]
INPUT = 'd333e4ce5f210a254932d7ba6e06e0649fc37f45ec248f033534e175e72e2d0b'
ACK = '0e4a99cb278bb4ec37fc90c027903417eb2f790915941000538fcf2209b81872'
OUTPUT = '7861cd3c546e73651ddfd28047686a29e580f5bebd38b1a7fd681d3b36fc24e0'

def module(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    value = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(value)
    return value

def build(source, destination):
    data = Path(source).read_bytes()
    if hashlib.sha256(data).hexdigest() != INPUT:
        raise ValueError('exact cumulative client input required')
    client = ROOT/'scripts/client'
    completion = module('host_completion', client/'party-host-completion/build_candidate.py')
    authority = module('host_authority', client/'party-host-authority/build_candidate.py')
    intermediate, ack_receipt = completion.build(data)
    if hashlib.sha256(intermediate).hexdigest() != ACK:
        raise ValueError('completion intermediate differs')
    candidate, display_receipt = authority.build(intermediate)
    if hashlib.sha256(candidate).hexdigest() != OUTPUT or len(candidate) != len(data):
        raise ValueError('final cumulative output differs')
    with Path(destination).open('xb') as stream:
        stream.write(candidate)
    if hashlib.sha256(Path(destination).read_bytes()).hexdigest() != OUTPUT:
        raise ValueError('candidate disk readback differs')
    return dict(inputSha256=INPUT, outputSha256=OUTPUT, bytes=len(candidate),
                completion=ack_receipt, display=display_receipt)

if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    print(json.dumps(build(args.input, args.output), indent=2))
