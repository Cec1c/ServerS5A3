"""Verify and join the selected resource archives; no installation or network IO."""
import argparse
import hashlib
import json
from pathlib import Path


def digest(path):
    with path.open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('directory', type=Path)
    parser.add_argument('--output-dir', type=Path, required=True)
    args = parser.parse_args()
    manifest = json.loads(Path(__file__).with_name('downloads.json').read_text('utf8'))
    for archive in manifest['archives']:
        parts = sorted((p for p in manifest['parts'] if p['archive'] == archive['name']), key=lambda p: p['part'])
        if not parts or [p['part'] for p in parts] != list(range(1, parts[0]['parts'] + 1)):
            raise ValueError('incomplete part manifest: ' + archive['name'])
        for part in parts:
            path = args.directory / part['name']
            if path.stat().st_size != part['bytes'] or digest(path) != part['sha256']:
                raise ValueError('part checksum mismatch: ' + part['name'])
        target = args.output_dir / archive['name']
        if target.exists():
            raise FileExistsError('refusing existing output: ' + str(target))
        args.output_dir.mkdir(parents=True, exist_ok=True)
        with target.open('xb') as output:
            for part in parts:
                with (args.directory / part['name']).open('rb') as source:
                    for block in iter(lambda: source.read(1024 * 1024), b''):
                        output.write(block)
        if target.stat().st_size != archive['bytes'] or digest(target) != archive['sha256']:
            raise ValueError('archive checksum mismatch; preserve output for inspection: ' + str(target))
        print('verified:', target.name, archive['sha256'])


if __name__ == '__main__':
    main()
