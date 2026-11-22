"""One post-health evidence exchange inside the existing game SSH invocation."""
import hashlib
import json
import os
import re
import shlex
import subprocess
import sys
import tempfile
import time

from deployment_clock import wait_for_remote_evidence_clock

PREFIX = b'POST_HEALTH_EDGE_REFRESH '
# The remote invocation waits 120 seconds. Leave it 20 seconds to receive and
# verify our answer; all local reads, clock waits and delivery share this budget.
REFRESH_BUDGET_SECONDS = 100.0
DELIVERY_RESERVE_SECONDS = 20.0


class ExchangeBudget:
    def __init__(self):
        self.started = time.monotonic()
        self.deadline = self.started + REFRESH_BUDGET_SECONDS
        self.steps = []

    def call(self, name, limit, reserve, action):
        started = time.monotonic()
        timeout = min(limit, self.deadline - started - reserve)
        step = {'stage': name, 'timeoutSeconds': max(0.0, timeout)}
        self.steps.append(step)
        try:
            if timeout <= 0:
                raise TimeoutError('post-health exchange budget exhausted')
            result = action(timeout)
            step['status'] = 'succeeded'
            return result
        except Exception as error:
            step['status'] = 'failed'
            step['error'] = type(error).__name__
            if isinstance(error, subprocess.CalledProcessError):
                step['returnCode'] = error.returncode
            raise
        finally:
            step['elapsedSeconds'] = round(time.monotonic() - started, 3)

    def sleep(self, name, seconds):
        def wait(available):
            if seconds > available:
                raise TimeoutError('post-health clock wait exceeds remaining budget')
            time.sleep(seconds)
        self.call(name, 30, DELIVERY_RESERVE_SECONDS, wait)


def checked_challenge(value, run_id, manifest, pre_edge_sha, transition_sha):
    fields = {'format', 'nonce', 'runId', 'sourceCommit', 'serverSha256', 'image',
              'preEdgeSha256', 'transitionSha256', 'requestedAt'}
    if (not isinstance(value, dict) or set(value) != fields or value['format'] != 1 or
            not isinstance(value['nonce'], str) or not re.fullmatch('[0-9a-f]{64}', value['nonce']) or
            value['runId'] != run_id or value['sourceCommit'] != manifest['sourceCommit'] or
            value['serverSha256'] != manifest['serverSha256'] or
            value['preEdgeSha256'] != pre_edge_sha or value['transitionSha256'] != transition_sha or
            not isinstance(value['image'], str) or not re.fullmatch('sha256:[0-9a-f]{64}', value['image'])):
        raise ValueError('healthy game challenge does not match this prepared invocation')
    return value


def save_new(path, value):
    raw = json.dumps(value, sort_keys=True, separators=(',', ':')).encode()
    with path.open('xb') as stream:
        stream.write(raw)
    return raw


def put_response(updater, value, timeout=DELIVERY_RESERVE_SECONDS):
    raw = json.dumps(value, sort_keys=True, separators=(',', ':')).encode()
    if len(raw) > 2 * 1024 * 1024:
        raise ValueError('post-health response too large')
    # A fixed file below this immutable run, never a path from the challenge.
    path = updater.remote_stage + '/post-health-edge-response.json'
    code = ("from pathlib import Path; import os,sys; os.umask(0o077); p=Path(" + repr(path) +
            "); t=p.with_suffix('.new'); assert not p.exists() and not p.is_symlink(); "
            "f=t.open('xb'); f.write(sys.stdin.buffer.read()); f.flush(); os.fsync(f.fileno()); f.close(); os.replace(t,p)")
    updater.ssh(['sudo', '-n', 'python3', '-c', code], raw, timeout=timeout)
    save_new(updater.stage / 'post-health-edge-response.json', value)


def refresh(updater, challenge, pre_edge_sha, ssh_options):
    manifest = json.loads((updater.stage / 'game-source-manifest.json').read_text('utf8'))
    resource_pair = bool(getattr(updater, 'resource_pair', False))
    transition_sha = (hashlib.sha256(updater.resource_transition.read_bytes()).hexdigest()
                      if resource_pair else None)
    checked_challenge(challenge, updater.run_id, manifest, pre_edge_sha, transition_sha)
    if getattr(updater, 'publication_permit', None) is not None:
        updater.publication_permit.check('activate')
    save_new(updater.stage / 'post-health-edge-request.json', challenge)
    alias = updater.profile['edge_ssh_alias']
    budget = ExchangeBudget()
    diagnostics = {'format': 1, 'budgetSeconds': REFRESH_BUDGET_SECONDS,
                   'steps': budget.steps, 'status': 'failed'}

    def edge_command(argv, timeout, code=None):
        return subprocess.run(['ssh', *ssh_options, alias, shlex.join(argv)], input=code,
                              stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=True, timeout=timeout)

    # Ensure the independent edge clock has reached the game health challenge.
    # This is the existing bounded clock alignment, not a changed timestamp.
    try:
        first_clock = wait_for_remote_evidence_clock([challenge['requestedAt']],
            lambda: budget.call('edge-clock', 20, DELIVERY_RESERVE_SECONDS,
                lambda timeout: float(edge_command(['python3', '-c', 'import time; print(time.time())'], timeout).stdout)),
            lambda seconds: budget.sleep('edge-clock-wait', seconds))
        edge_args = ['sudo', '-n', 'python3', '-', 'edge-snapshot', '--root',
                     updater.profile.get('edge_remote_root', updater.profile['remote_root'])]
        edge = budget.call('edge-snapshot', 60, DELIVERY_RESERVE_SECONDS,
            lambda timeout: json.loads(edge_command(edge_args, timeout, updater.worker_code).stdout))
        second_clock = wait_for_remote_evidence_clock([edge['checkedAt']],
            lambda: budget.call('game-clock', 20, DELIVERY_RESERVE_SECONDS,
                lambda timeout: float(updater.ssh(['python3', '-c', 'import time; print(time.time())'], timeout=timeout).stdout)),
            lambda seconds: budget.sleep('game-clock-wait', seconds))
        if getattr(updater, 'publication_permit', None) is not None:
            updater.publication_permit.check('activate')
        save_new(updater.stage / 'post-health-edge-clock.json', {'beforeCapture': first_clock, 'beforeDelivery': second_clock})
        budget.call('response-delivery', DELIVERY_RESERVE_SECONDS, 0,
            lambda timeout: put_response(updater, {'format': 1, 'challenge': challenge, 'edge': edge}, timeout))
        diagnostics['status'] = 'succeeded'
    except Exception as error:
        diagnostics['error'] = type(error).__name__
        raise
    finally:
        diagnostics['elapsedSeconds'] = round(time.monotonic() - budget.started, 3)
        # No command arguments, response payloads or stderr: those may contain
        # deployment material. The failed stage and timeout are enough.
        save_new(updater.stage / 'post-health-edge-diagnostics.json', diagnostics)


def run(updater, argv, capture, pre_edge_sha, ssh_options, serve=refresh):
    """Drain this one SSH process; refresh failure lets its original rollback run."""
    command = ['ssh', *ssh_options, updater.profile['ssh_alias'], shlex.join(argv)]
    output = bytearray()
    refresh_error = None
    served = False
    with tempfile.TemporaryFile() as errors:
        process = subprocess.Popen(command, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=errors,
                                   creationflags=0x08000000 if os.name == 'nt' else 0)
        try:
            process.stdin.write(updater.worker_code)
            process.stdin.close()
            for line in iter(process.stdout.readline, b''):
                if capture:
                    output.extend(line)
                else:
                    sys.stdout.buffer.write(line)
                    sys.stdout.buffer.flush()
                if not line.startswith(PREFIX):
                    continue
                challenge = None
                try:
                    if served or len(line) > 16384:
                        raise ValueError('duplicate or oversized health challenge')
                    served = True
                    challenge = json.loads(line[len(PREFIX):])
                    serve(updater, challenge, pre_edge_sha, ssh_options)
                except Exception as error:
                    refresh_error = error
                    # Do not kill or retry the deployment process. An explicit
                    # rejection (or its bounded timeout) triggers original rollback.
                    try:
                        put_response(updater, {'format': 1, 'challenge': challenge,
                                               'error': type(error).__name__})
                    except Exception:
                        pass
            status = process.wait()
            errors.seek(0)
            stderr = errors.read(65536)
            if stderr and not capture:
                sys.stderr.buffer.write(stderr)
                sys.stderr.buffer.flush()
        finally:
            process.stdout.close()
            # An interrupted caller must not terminate or replay the remote action.
        if status:
            raise subprocess.CalledProcessError(status, command, bytes(output), stderr)
        if refresh_error is not None or not served:
            raise RuntimeError('deployment finished without a successful post-health exchange; inspect original run') from refresh_error
        return subprocess.CompletedProcess(command, status, bytes(output), stderr)
