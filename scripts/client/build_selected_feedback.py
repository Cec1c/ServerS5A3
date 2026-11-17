"""Build the selected arena/marriage client from the public V215 ZIP and PVF."""
import argparse
import hashlib
import importlib.util
import json
from pathlib import Path
import tempfile
import zipfile

ROOT = Path(__file__).resolve().parents[2]


def load(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def build(base_zip, pvf, output):
    tag = load('selected_tag_builder', ROOT / 'scripts/client/tag-buff-groups/build_candidate.py')
    wedding = load('selected_wedding_builder', ROOT / 'resource-recipes/20261117-wedding-female-client/build.py')
    if output.exists():
        raise ValueError('output must not exist')
    with pvf.open('rb') as stream:
        digest = hashlib.sha256()
        for block in iter(lambda: stream.read(1024 * 1024), b''):
            digest.update(block)
    if digest.hexdigest() != wedding.PVF:
        raise ValueError('require the exact public 66482976 PVF')
    required = {'DNF.exe', 'manifest.json', *wedding.DEPENDENCIES}
    with zipfile.ZipFile(base_zip) as archive:
        names = archive.namelist()
        if any(names.count(name) != 1 for name in required):
            raise ValueError('public baseline members are missing or duplicated')
        manifest = json.loads(archive.read('manifest.json'))
        original = archive.read('DNF.exe')
        if hashlib.sha256(original).hexdigest() != tag.CURRENT_BASE:
            raise ValueError('require the exact public V215 baseline')
        if manifest.get('clientFiles', {}).get('DNF.exe') != dict(sha256=tag.CURRENT_BASE, bytes=len(original)):
            raise ValueError('public baseline manifest EXE differs')
        candidate, receipt = tag.build(original)
        output.parent.mkdir(parents=True, exist_ok=True)
        with tempfile.TemporaryDirectory(prefix='selected-client-', dir=output.parent) as directory:
            base = Path(directory)
            (base / 'DNF.exe').write_bytes(candidate)
            for name, (expected, length) in wedding.DEPENDENCIES.items():
                data = archive.read(name)
                pin = dict(sha256=expected, bytes=length)
                if (hashlib.sha256(data).hexdigest(), len(data)) != (expected, length) or manifest.get('clientFiles', {}).get(name) != pin:
                    raise ValueError('public baseline dependency differs: ' + name)
                path = base / name;path.parent.mkdir(parents=True, exist_ok=True);path.write_bytes(data)
            source = dict(manifest, gamePvfSha256=wedding.PVF, baseExeSha256=tag.CURRENT_BASE,
                          tagBuffRecipe='scripts/client/tag-buff-groups/build_candidate.py')
            source.pop('source', None)
            source.pop('selectedItem', None)
            source['basePublicSource'] = manifest.get('source')
            source['selectedItems'] = ['NF23', 'NF24', 'NF25']
            source['clientFiles'] = dict(manifest['clientFiles'])
            source['clientFiles']['DNF.exe'] = dict(sha256=receipt['outputSHA256'], bytes=len(candidate))
            (base / 'source.json').write_text(json.dumps(source, ensure_ascii=False, indent=2)+'\n', 'utf8')
            result = wedding.build(base, output)
        (output / 'tag-build.json').write_text(json.dumps(receipt, indent=2)+'\n', 'utf8')
        return result


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--base-zip', required=True, type=Path)
    parser.add_argument('--pvf', required=True, type=Path)
    parser.add_argument('--output', required=True, type=Path)
    args = parser.parse_args()
    print(json.dumps(build(args.base_zip, args.pvf, args.output), indent=2))
