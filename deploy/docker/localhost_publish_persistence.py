"""Install a pinned loopback guard before Docker starts, under a coordinator.

This module never restarts Docker or changes its daemon configuration. The
coordinator seals capture() before install(), and restores proxy/bridge state
before restore(). A pre-existing matching helper/hook remains owned by its
original installer. All mutations are limited to these two exact files.
"""
import errno
import hashlib
import json
import os
from pathlib import Path
import stat
import subprocess

HELPER = Path('/usr/local/libexec/servers5a3-localhost-publish-guard.py')
HOOK = Path('/etc/systemd/system/docker.service.d/30-servers5a3-localhost-publish-guard.conf')
HOOK_BYTES = ('[Service]\nExecStartPre=/usr/bin/python3 ' + str(HELPER) + '\n').encode()


def digest(data):
    return hashlib.sha256(data).hexdigest()


def safe_directory(path):
    path = Path(path)
    for parent in [*reversed(path.parents), path]:
        if parent.is_symlink():
            raise ValueError('redirected guard installation directory')
        if parent.exists():
            entry = parent.stat()
            if not stat.S_ISDIR(entry.st_mode) or entry.st_uid != 0 or entry.st_mode & 0o022:
                raise ValueError('untrusted guard installation directory')


def file_state(path):
    safe_directory(path.parent)
    if path.is_symlink():
        raise ValueError('redirected guard installation file')
    if not path.exists():
        return {'exists': False}
    entry = path.stat()
    if not stat.S_ISREG(entry.st_mode) or entry.st_uid != 0 or entry.st_mode & 0o022:
        raise ValueError('untrusted guard installation file')
    if entry.st_size > 256 * 1024:
        raise ValueError('unexpected guard installation file size')
    return dict(exists=True, sha256=digest(path.read_bytes()), uid=entry.st_uid,
                gid=entry.st_gid, mode=stat.S_IMODE(entry.st_mode))


def expected_files(helper):
    if not isinstance(helper, bytes) or not helper or len(helper) > 256 * 1024:
        raise ValueError('a bounded pinned helper is required')
    compile(helper, str(HELPER), 'exec')
    return ((HELPER, helper, 0o755), (HOOK, HOOK_BYTES, 0o644))


def require_matching(state, data, mode):
    if state['exists'] and (state['sha256'] != digest(data) or state['mode'] != mode or
                            state['uid'] != 0 or state['gid'] != 0):
        raise ValueError('existing guard helper/hook differs; preserve it for review')


def capture(helper):
    """Read-only preflight, to be sealed by the maintenance coordinator."""
    files = {}
    directories = {}
    for path, data, mode in expected_files(helper):
        state = file_state(path)
        require_matching(state, data, mode)
        files[str(path)] = state
        for parent in path.parents:
            if str(parent) in directories:
                break
            directories[str(parent)] = {'exists': parent.exists()}
    return dict(schema=1, helperSha256=digest(helper), hookSha256=digest(HOOK_BYTES),
                files=files, directories=directories)


def validate_baseline(baseline, helper):
    if (not isinstance(baseline, dict) or set(baseline) !=
            {'schema', 'helperSha256', 'hookSha256', 'files', 'directories'} or
            baseline['schema'] != 1 or baseline['helperSha256'] != digest(helper) or
            baseline['hookSha256'] != digest(HOOK_BYTES) or
            set(baseline['files']) != {str(HELPER), str(HOOK)}):
        raise ValueError('sealed guard installation baseline differs')
    expected_parents = {str(parent) for path, _, _ in expected_files(helper) for parent in path.parents}
    if set(baseline['directories']) != expected_parents:
        raise ValueError('guard installation directory baseline differs')
    for state in baseline['directories'].values():
        if not isinstance(state, dict) or set(state) != {'exists'} or type(state['exists']) is not bool:
            raise ValueError('invalid original guard directory state')
    for path, data, mode in expected_files(helper):
        state = baseline['files'][str(path)]
        if not isinstance(state, dict) or type(state.get('exists')) is not bool:
            raise ValueError('invalid original guard file state')
        expected_keys = {'exists', 'sha256', 'uid', 'gid', 'mode'} if state['exists'] else {'exists'}
        if set(state) != expected_keys:
            raise ValueError('invalid original guard file identity')
        require_matching(state, data, mode)


def reload_manager(runner):
    result = runner(['systemctl', 'daemon-reload'], capture_output=True, text=True, timeout=20)
    if result.returncode:
        raise RuntimeError('systemd reload failed: ' + result.stderr.strip()[:300])


def create_file(path, data, mode):
    safe_directory(path.parent)
    # Only missing exact ancestors are created. Existing attributes are retained.
    for parent in reversed(path.parents):
        if not parent.exists():
            parent.mkdir(mode=0o755)
    safe_directory(path.parent)
    temporary = path.with_name(path.name + '.install-part')
    descriptor = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, mode)
    try:
        with os.fdopen(descriptor, 'wb') as out:
            os.fchmod(out.fileno(), mode)
            os.fchown(out.fileno(), 0, 0)
            out.write(data);out.flush();os.fsync(out.fileno())
        os.link(temporary, path)  # Create-only, even if another process installs concurrently.
    finally:
        temporary.unlink()
    directory = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
    try:
        os.fsync(directory)
    finally:
        os.close(directory)


def install(baseline, helper, runner=subprocess.run):
    """Idempotent for the same sealed baseline; no Docker restart or rule change."""
    if os.geteuid() != 0:
        raise PermissionError('root is required for the Docker startup hook')
    validate_baseline(baseline, helper)
    for path, data, mode in expected_files(helper):
        current = file_state(path)
        require_matching(current, data, mode)
        if baseline['files'][str(path)]['exists'] and current != baseline['files'][str(path)]:
            raise ValueError('pre-existing guard installation changed')
    for path, data, mode in expected_files(helper):
        if not file_state(path)['exists']:
            create_file(path, data, mode)
    reload_manager(runner)
    current = capture(helper)
    if not all(x['exists'] for x in current['files'].values()):
        raise RuntimeError('guard startup hook is incomplete')
    return dict(installed=True, helperSha256=digest(helper), hookSha256=digest(HOOK_BYTES))


def restore(baseline, helper, runner=subprocess.run):
    """Only after proxy/bridge rollback; safe to repeat after a partial restore."""
    if os.geteuid() != 0:
        raise PermissionError('root is required for the Docker startup hook')
    validate_baseline(baseline, helper)
    for path, data, mode in expected_files(helper):
        current = file_state(path)
        require_matching(current, data, mode)
        if baseline['files'][str(path)]['exists'] and current != baseline['files'][str(path)]:
            raise ValueError('pre-existing guard installation changed')
    if not baseline['files'][str(HOOK)]['exists'] and HOOK.exists():
        HOOK.unlink()
    # Keep the helper if reload fails: the manager may still have the old hook.
    reload_manager(runner)
    if not baseline['files'][str(HELPER)]['exists'] and HELPER.exists():
        HELPER.unlink()
    for name in sorted(baseline['directories'], key=lambda value: len(Path(value).parts), reverse=True):
        path = Path(name)
        if not baseline['directories'][name]['exists'] and path.exists():
            safe_directory(path)
            try:
                path.rmdir()
            except OSError as error:
                if error.errno not in (errno.ENOTEMPTY, errno.EEXIST):
                    raise
    return dict(restored=True, files={str(path): file_state(path) for path, _, _ in expected_files(helper)})
