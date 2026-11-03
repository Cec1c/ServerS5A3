"""Hash-verified resource cache staging; no network or live-service changes."""
import hashlib
import shutil
import time
from pathlib import Path


def digest(path):
    h = hashlib.sha256()
    with Path(path).open('rb') as f:
        for b in iter(lambda: f.read(1024 * 1024), b''):
            h.update(b)
    return h.hexdigest()


def cached_resource(cache_dir, stage, expected, size):
    import re
    if not re.fullmatch('[a-f0-9]{64}', expected) or type(size) is not int or size <= 0:
        raise ValueError('Invalid resource identity')
    source = Path(cache_dir) / expected
    if not source.exists():
        return {'hit': False}
    started = time.monotonic()
    if source.is_symlink() or not source.is_file() or source.stat().st_size != size or digest(source) != expected:
        raise ValueError('Resource cache corrupt; preserve evidence')
    target = Path(stage) / 'Script.pvf'
    with source.open('rb') as inp, target.open('xb') as out:
        shutil.copyfileobj(inp, out, 1024 * 1024)
    if digest(target) != expected:
        raise ValueError('Resource changed during staging')
    return {'hit': True, 'sha256': expected, 'bytes': size, 'seconds': round(time.monotonic() - started, 6)}
