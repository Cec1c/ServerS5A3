"""Verify every required cumulative client/PVF component before installation."""
import argparse
import hashlib
import json
from pathlib import Path

def verify(directory, manifest):
    document = json.loads(manifest.read_text(encoding='utf8'))
    for name, pin in document['files'].items():
        path = directory / name
        if path.stat().st_size != pin['bytes']:
            raise ValueError('size mismatch: '+name)
        with path.open('rb') as stream:
            if hashlib.file_digest(stream, 'sha256').hexdigest() != pin['sha256']:
                raise ValueError('SHA-256 mismatch: '+name)
    return len(document['files'])

if __name__ == '__main__':
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('directory', type=Path)
    p.add_argument('--manifest', type=Path, default=Path(__file__).with_name('manifest.json'))
    a=p.parse_args()
    print('Verified components:', verify(a.directory, a.manifest))
