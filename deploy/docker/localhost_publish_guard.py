"""Preserve loopback-only publications when legacy Docker uses kernel NAT.

Linux only. Installs one owned raw PREROUTING rule without flushing any chain.
The coordinator owns persistence, the approved baseline and rollback ordering.
"""
from contextlib import contextmanager
import hashlib
import json
import os
from pathlib import Path
import shlex
import stat
import subprocess
import time

LABEL = 'servers5a3-localhost-publish-v1'
RULE = ['!', '-i', 'lo', '-d', '127.0.0.0/8', '-m', 'comment', '--comment', LABEL, '-j', 'DROP']
POLICY_SHA256 = hashlib.sha256(json.dumps(RULE, separators=(',', ':')).encode()).hexdigest()
LOCK = Path('/run/servers5a3-localhost-publish-guard.lock')


def invoke(arguments, runner):
    result = runner(['iptables', '-w', '5', '-t', 'raw', *arguments], capture_output=True, text=True, timeout=15)
    if result.returncode not in (0, 1):
        raise RuntimeError('iptables operation failed: ' + result.stderr.strip()[:300])
    return result


def check(runner=subprocess.run):
    listing = invoke(['-S', 'PREROUTING'], runner)
    if listing.returncode:
        raise RuntimeError('cannot read raw PREROUTING policy')
    owned = []
    chain_rules = []
    for line in listing.stdout.splitlines():
        tokens = shlex.split(line)
        if tokens[:2] == ['-A', 'PREROUTING']:
            chain_rules.append(tokens)
        if '--comment' in tokens:
            index = tokens.index('--comment')
            if index + 1 < len(tokens) and tokens[index + 1] == LABEL:
                owned.append(tokens)
    present = invoke(['-C', 'PREROUTING', *RULE], runner).returncode == 0
    if len(owned) > 1 or bool(owned) != present:
        raise RuntimeError('owned loopback rule conflicts with the approved policy')
    if present and (not chain_rules or chain_rules[0] != owned[0]):
        raise RuntimeError('owned loopback rule must precede other raw PREROUTING rules')
    return {'present': present, 'policySha256': POLICY_SHA256}


@contextmanager
def guard_lock():
    import fcntl
    if os.geteuid() != 0:
        raise PermissionError('root is required for the loopback publication policy')
    descriptor = os.open(LOCK, os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
    try:
        entry = os.fstat(descriptor)
        if not stat.S_ISREG(entry.st_mode) or entry.st_uid != 0 or entry.st_mode & 0o077:
            raise RuntimeError('unsafe loopback guard lock file')
        deadline = time.monotonic() + 5
        while True:
            try:
                fcntl.flock(descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
                break
            except BlockingIOError:
                if time.monotonic() >= deadline:
                    raise TimeoutError('loopback guard is busy') from None
                time.sleep(0.05)
        yield
    finally:
        os.close(descriptor)
    # Keep the inode: unlinking a lock file permits two independent lock owners.


def ensure(runner=subprocess.run, *, lock=None):
    with guard_lock() if lock is None else lock:
        before = check(runner)
        if not before['present']:
            inserted = invoke(['-I', 'PREROUTING', '1', *RULE], runner)
            if inserted.returncode:
                raise RuntimeError('cannot install the loopback publication policy')
        after = check(runner)
        if not after['present']:
            raise RuntimeError('loopback publication policy did not become active')
        return {**after, 'changed': not before['present']}


def remove_if_created(original_present, policy_sha256, runner=subprocess.run, *, lock=None):
    if type(original_present) is not bool or policy_sha256 != POLICY_SHA256:
        raise ValueError('sealed original policy identity is required')
    with guard_lock() if lock is None else lock:
        current = check(runner)
        if original_present:
            if not current['present']:
                raise RuntimeError('pre-existing loopback policy disappeared')
            return {**current, 'changed': False}
        if current['present']:
            removed = invoke(['-D', 'PREROUTING', *RULE], runner)
            if removed.returncode:
                raise RuntimeError('cannot remove the created loopback policy')
        after = check(runner)
        if after['present']:
            raise RuntimeError('created loopback policy remains active')
        return {**after, 'changed': current['present']}


def main():
    import argparse
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--check', action='store_true')
    args = parser.parse_args()
    result = check() if args.check else ensure()
    print(json.dumps(result, sort_keys=True))
    return 0 if result['present'] else 1


if __name__ == '__main__':
    raise SystemExit(main())
