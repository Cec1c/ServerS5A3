"""Shared-budget, diagnostics, rollback and integration-recipe tests."""
import copy
from datetime import datetime, timedelta, timezone
import hashlib
import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))

from deployment_clock import wait_for_remote_evidence_clock
import post_health_edge as publisher


def load_module(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


REMOTE_PATH = Path(__file__).resolve().parents[1] / 'remote/game_update.py'
WINDOWS_PATH = HERE / 'game_update.py'
remote_worker = load_module('post_health_remote_worker', REMOTE_PATH)
windows_update = load_module('post_health_windows_update', WINDOWS_PATH)


def encode(value):
    return json.dumps(value, sort_keys=True, separators=(',', ':')).encode()


def fixture():
    now = datetime(2026, 11, 3, 8, 0, 4, tzinfo=timezone.utc)
    before = {
        'format': 1,
        'checkedAt': (now - timedelta(seconds=321)).isoformat(),
        'management': {'mode': 'external-gm', 'pvfSha256': 'a' * 64, 'pid': 123},
        'published': {'profile': {'PvfSha256': 'a' * 64}, 'launcher': {'version': '1.0.8'}},
        'protectedPublished': {'policy': {'enabled': True}},
        'configHashes': {'/profile': 'b' * 64},
    }
    old_raw = encode(before)
    challenge = {
        'format': 1,
        'nonce': 'c' * 64,
        'runId': 'test-run',
        'sourceCommit': 'd' * 40,
        'serverSha256': 'e' * 64,
        'image': 'sha256:' + 'f' * 64,
        'preEdgeSha256': hashlib.sha256(old_raw).hexdigest(),
        'transitionSha256': None,
        'requestedAt': (now - timedelta(seconds=2)).isoformat(),
    }
    after = copy.deepcopy(before)
    after['checkedAt'] = (now - timedelta(seconds=1)).isoformat()
    return SimpleNamespace(now=now, before=before, challenge=challenge, after=after)


class ExchangeBudgetTests(unittest.TestCase):
    def run_refresh(self, directory, snapshot_seconds=45, fail_snapshot=False,
                    edge_clock_seconds=1):
        case = fixture()
        stage = Path(directory)
        (stage / 'game-source-manifest.json').write_bytes(encode(
            {'sourceCommit': 'd' * 40, 'serverSha256': 'e' * 64}))
        ticks = [0.0]
        self.calls = []

        def advance(timeout, seconds):
            if seconds > timeout:
                ticks[0] += timeout
                raise subprocess.TimeoutExpired('redacted-command', timeout)
            ticks[0] += seconds

        def edge(argv, **kwargs):
            snapshot = kwargs.get('input') is not None
            self.calls.append(('snapshot' if snapshot else 'edge-clock', kwargs['timeout']))
            if snapshot and fail_snapshot:
                ticks[0] += kwargs['timeout']
                raise subprocess.TimeoutExpired('redacted-command', kwargs['timeout'])
            advance(kwargs['timeout'], snapshot_seconds if snapshot else edge_clock_seconds)
            data = encode(case.after) if snapshot else str(case.now.timestamp()).encode()
            return subprocess.CompletedProcess(argv, 0, data, b'')

        def ssh(argv, code=None, timeout=None):
            is_clock = argv[:2] == ['python3', '-c']
            self.calls.append(('game-clock' if is_clock else 'response', timeout))
            advance(timeout, 1)
            data = str(case.now.timestamp()).encode() if is_clock else b''
            return subprocess.CompletedProcess(argv, 0, data, b'')

        updater = SimpleNamespace(
            stage=stage,
            run_id='test-run',
            profile={'edge_ssh_alias': 'edge', 'remote_root': '/root'},
            remote_stage='/srv/game/runtime/game-updates/test-run',
            worker_code=b'reviewed-readonly-code',
            ssh=ssh,
        )
        with patch.object(publisher.time, 'monotonic', side_effect=lambda: ticks[0]), \
                patch.object(publisher.subprocess, 'run', side_effect=edge):
            publisher.refresh(updater, case.challenge, case.challenge['preEdgeSha256'], [])
        return case

    def test_slow_snapshot_under_shared_budget_is_delivered_once(self):
        with tempfile.TemporaryDirectory() as directory:
            self.run_refresh(directory)
            result = json.loads((Path(directory) / 'post-health-edge-diagnostics.json').read_text())
            self.assertEqual(result['status'], 'succeeded')
            self.assertEqual(result['elapsedSeconds'], 48)
            self.assertEqual([item[0] for item in self.calls],
                             ['edge-clock', 'snapshot', 'game-clock', 'response'])
            self.assertEqual(self.calls[1][1], 60)
            self.assertEqual(len([item for item in self.calls if item[0] == 'response']), 1)

    def test_snapshot_timeout_records_stage_without_sensitive_output(self):
        with tempfile.TemporaryDirectory() as directory:
            with self.assertRaises(subprocess.TimeoutExpired):
                self.run_refresh(directory, fail_snapshot=True)
            stage = Path(directory)
            result = json.loads((stage / 'post-health-edge-diagnostics.json').read_text())
            self.assertEqual(result['steps'][-1]['stage'], 'edge-snapshot')
            self.assertEqual(result['steps'][-1]['error'], 'TimeoutExpired')
            self.assertFalse((stage / 'post-health-edge-response.json').exists())
            self.assertNotIn('redacted-command', json.dumps(result))
            self.assertEqual([item[0] for item in self.calls], ['edge-clock', 'snapshot'])

    def test_reads_share_budget_and_reserve_delivery_time(self):
        with tempfile.TemporaryDirectory() as directory:
            self.run_refresh(directory, snapshot_seconds=59, edge_clock_seconds=20)
            self.assertEqual(self.calls[2], ('game-clock', 1.0))
            result = json.loads((Path(directory) / 'post-health-edge-diagnostics.json').read_text())
            self.assertEqual(result['elapsedSeconds'], 81)
            self.assertLess(result['elapsedSeconds'], publisher.REFRESH_BUDGET_SECONDS)

    def test_exhausted_budget_never_starts_another_operation(self):
        ticks = [0.0]
        called = []
        with patch.object(publisher.time, 'monotonic', side_effect=lambda: ticks[0]):
            budget = publisher.ExchangeBudget()
            ticks[0] = 100
            with self.assertRaises(TimeoutError):
                budget.call('response-delivery', 20, 0, lambda timeout: called.append(timeout))
        self.assertEqual(called, [])
        self.assertEqual(budget.steps[-1]['status'], 'failed')

    def test_clock_sleep_cannot_spend_delivery_reserve(self):
        ticks = [0.0]
        with patch.object(publisher.time, 'monotonic', side_effect=lambda: ticks[0]), \
                patch.object(publisher.time, 'sleep') as sleep:
            budget = publisher.ExchangeBudget()
            ticks[0] = 75
            with self.assertRaises(TimeoutError):
                budget.sleep('edge-clock-wait', 6)
            sleep.assert_not_called()
        self.assertEqual(budget.steps[-1]['stage'], 'edge-clock-wait')

    def test_timed_out_stage_rejects_replay_and_requires_fresh_run(self):
        case = fixture()
        with tempfile.TemporaryDirectory() as directory:
            stage = Path(directory)
            (stage / 'game-source-manifest.json').write_bytes(encode(
                {'sourceCommit': 'd' * 40, 'serverSha256': 'e' * 64}))
            (stage / 'post-health-edge-request.json').write_bytes(encode(case.challenge))
            updater = SimpleNamespace(
                stage=stage,
                run_id='test-run',
                profile={'edge_ssh_alias': 'edge', 'remote_root': '/root'},
                remote_stage='/srv/game/runtime/game-updates/test-run',
                worker_code=b'reviewed-readonly-code',
                ssh=lambda *args, **kwargs: self.fail('replayed SSH command'),
            )
            with patch.object(publisher.subprocess, 'run', side_effect=AssertionError('replayed edge command')):
                with self.assertRaises(FileExistsError):
                    publisher.refresh(updater, case.challenge, case.challenge['preEdgeSha256'], [])
            self.assertFalse((stage / 'post-health-edge-response.json').exists())


class ProcessFallbackTests(unittest.TestCase):
    def run_child(self, fail):
        with tempfile.TemporaryDirectory() as directory:
            stage = Path(directory)
            response = stage / 'received.json'
            child = (
                "import sys,json,time\nfrom pathlib import Path\n"
                "sys.stdin.buffer.read()\n"
                "print('POST_HEALTH_EDGE_REFRESH '+json.dumps({'nonce':'test'}),flush=True)\n"
                f"p=Path({str(response)!r})\n"
                "end=time.monotonic()+5\n"
                "while not p.exists() and time.monotonic()<end:time.sleep(.01)\n"
                "v=json.loads(p.read_text()) if p.exists() else {'error':'timeout'}\n"
                "print('ROLLED_BACK' if 'error' in v else 'DEPLOYED',flush=True)\n"
                "sys.exit(1 if 'error' in v else 0)\n"
            )
            real_popen = subprocess.Popen
            processes = []

            def launch(_, **options):
                process = real_popen([sys.executable, '-c', child], **options)
                processes.append(process)
                return process

            def serve(*_):
                if fail:
                    raise OSError('snapshot unavailable')
                response.write_text('{}')

            def reject(_, value, timeout=20):
                response.write_text(json.dumps(value))

            updater = SimpleNamespace(
                profile={'ssh_alias': 'unused'},
                worker_code=b'reviewed worker',
                stage=stage,
            )
            with patch.object(publisher.subprocess, 'Popen', side_effect=launch), \
                    patch.object(publisher, 'put_response', side_effect=reject):
                if fail:
                    with self.assertRaises(subprocess.CalledProcessError) as caught:
                        publisher.run(updater, ['not-executed'], True, 'a' * 64, [], serve=serve)
                    self.assertIn(b'ROLLED_BACK', caught.exception.output)
                else:
                    result = publisher.run(updater, ['not-executed'], True, 'a' * 64, [], serve=serve)
                    self.assertIn(b'DEPLOYED', result.stdout)
            self.assertEqual(len(processes), 1)
            self.assertIsNotNone(processes[0].returncode)

    def test_single_process_receives_proof_and_completes(self):
        self.run_child(False)

    def test_failure_waits_for_original_rollback_without_retry(self):
        self.run_child(True)

    def test_windows_runner_and_remote_awaiter_complete_one_exchange(self):
        case = fixture()
        with tempfile.TemporaryDirectory() as directory:
            stage = Path(directory)
            before_path = stage / 'before.json'
            before_path.write_bytes(encode(case.before))
            response = stage / 'post-health-edge-response.json'
            child = (
                "import datetime,importlib.util,json,sys\nfrom pathlib import Path\n"
                "sys.stdin.buffer.read()\n"
                f"spec=importlib.util.spec_from_file_location('remote',{str(REMOTE_PATH)!r})\n"
                "remote=importlib.util.module_from_spec(spec);spec.loader.exec_module(remote)\n"
                f"before=json.loads(Path({str(before_path)!r}).read_text())\n"
                "now=datetime.datetime.now(datetime.timezone.utc)\n"
                "challenge={'format':1,'nonce':'a'*64,'runId':'test-run',"
                "'sourceCommit':'d'*40,'serverSha256':'e'*64,'image':'sha256:'+'f'*64,"
                "'preEdgeSha256':'1'*64,'transitionSha256':None,"
                "'requestedAt':(now-datetime.timedelta(seconds=2)).isoformat()}\n"
                f"edge=remote.await_post_health_edge(Path({str(stage)!r}),challenge,before)\n"
                "print('REMOTE_OK '+json.dumps(edge,sort_keys=True),flush=True)\n"
            )
            real_popen = subprocess.Popen
            processes = []

            def launch(_, **options):
                process = real_popen([sys.executable, '-c', child], **options)
                processes.append(process)
                return process

            def serve(_, challenge, __, ___):
                after = copy.deepcopy(case.before)
                after['checkedAt'] = datetime.now(timezone.utc).isoformat()
                response.write_bytes(encode({'format': 1, 'challenge': challenge, 'edge': after}))

            updater = SimpleNamespace(
                profile={'ssh_alias': 'unused'},
                worker_code=b'remote exchange fixture',
                stage=stage,
            )
            with patch.object(publisher.subprocess, 'Popen', side_effect=launch):
                result = publisher.run(updater, ['unused'], True, 'a' * 64, [], serve=serve)
            self.assertIn(b'REMOTE_OK', result.stdout)
            self.assertEqual(len(processes), 1)
            self.assertEqual(processes[0].returncode, 0)



class RemotePostHealthTests(unittest.TestCase):
    def setUp(self):
        self.now = datetime(2026, 11, 3, 8, 0, 4, tzinfo=timezone.utc)
        self.before = {
            'format': 1,
            'checkedAt': (self.now - timedelta(seconds=321)).isoformat(),
            'management': {'mode': 'external-gm', 'pvfSha256': 'a' * 64, 'pid': 123},
            'published': {'profile': {'PvfSha256': 'a' * 64}, 'launcher': {'version': '1.0.8'}},
            'configHashes': {'/srv/game/profile.json': 'b' * 64},
        }
        self.challenge = {
            'format': 1,
            'nonce': 'c' * 64,
            'runId': 'test-run',
            'sourceCommit': 'd' * 40,
            'serverSha256': 'e' * 64,
            'image': 'sha256:' + 'f' * 64,
            'preEdgeSha256': hashlib.sha256(encode(self.before)).hexdigest(),
            'transitionSha256': None,
            'requestedAt': (self.now - timedelta(seconds=2)).isoformat(),
        }
        self.after = copy.deepcopy(self.before)
        self.after['checkedAt'] = (self.now - timedelta(seconds=1)).isoformat()
        self.proof = {'format': 1, 'challenge': self.challenge, 'edge': self.after}

    def test_fresh_snapshot_is_bound_to_challenge_and_unchanged_state(self):
        self.assertEqual(
            remote_worker.checked_post_health_edge(
                encode(self.proof), self.challenge, self.before, self.now),
            self.after,
        )
        changed = copy.deepcopy(self.after)
        changed['published']['launcher']['version'] = 'changed'
        with self.assertRaisesRegex(ValueError, 'changed during startup'):
            remote_worker.checked_post_health_edge(
                encode({'format': 1, 'challenge': self.challenge, 'edge': changed}),
                self.challenge, self.before, self.now)

    def test_challenge_capture_time_and_size_are_bounded(self):
        wrong = dict(self.challenge, nonce='0' * 64)
        with self.assertRaisesRegex(ValueError, 'challenge'):
            remote_worker.checked_post_health_edge(encode(self.proof), wrong, self.before, self.now)
        old = copy.deepcopy(self.after)
        old['checkedAt'] = (self.now - timedelta(seconds=3)).isoformat()
        with self.assertRaisesRegex(ValueError, 'after this health'):
            remote_worker.checked_post_health_edge(
                encode({'format': 1, 'challenge': self.challenge, 'edge': old}),
                self.challenge, self.before, self.now)
        with self.assertRaisesRegex(ValueError, 'too large'):
            remote_worker.checked_post_health_edge(b' ' * (2 * 1024 * 1024 + 1),
                                                   self.challenge, self.before, self.now)

    def test_exchange_accepts_one_response_and_rejects_replay(self):
        with tempfile.TemporaryDirectory() as directory:
            stage = Path(directory)
            ticks = [0.0]

            def clock():
                return ticks[0]

            def sleep(seconds):
                ticks[0] += seconds
                (stage / 'post-health-edge-response.json').write_bytes(encode(self.proof))

            edge = remote_worker.await_post_health_edge(
                stage, self.challenge, self.before,
                clock=clock, sleep=sleep, now=lambda: self.now)
            self.assertEqual(edge, self.after)
            self.assertTrue((stage / 'post-health-edge-request.json').is_file())
            with self.assertRaisesRegex(ValueError, 'already exists'):
                remote_worker.await_post_health_edge(
                    stage, self.challenge, self.before, clock=clock, sleep=sleep, now=lambda: self.now)

    def test_exchange_timeout_is_bounded(self):
        with tempfile.TemporaryDirectory() as directory:
            stage = Path(directory)
            ticks = [0.0]

            def sleep(seconds):
                ticks[0] += 121

            with self.assertRaisesRegex(ValueError, 'timed out'):
                remote_worker.await_post_health_edge(
                    stage, self.challenge, self.before,
                    clock=lambda: ticks[0], sleep=sleep, now=lambda: self.now)


class WindowsEntrypointTests(unittest.TestCase):
    def test_ssh_forwards_timeout_and_capture(self):
        updater = windows_update.Update.__new__(windows_update.Update)
        updater.profile = {'ssh_alias': 'game'}
        completed = subprocess.CompletedProcess(['ssh'], 0, b'', b'')
        with patch.object(windows_update.subprocess, 'run', return_value=completed) as run:
            updater.ssh(['sudo', 'true'], b'code', capture=False, timeout=42)
        self.assertEqual(run.call_args.kwargs['timeout'], 42)
        self.assertIsNone(run.call_args.kwargs['stdout'])
        self.assertIsNone(run.call_args.kwargs['stderr'])

    def test_current_remote_selects_post_health_transport(self):
        updater = windows_update.Update.__new__(windows_update.Update)
        updater.worker_code = REMOTE_PATH.read_bytes()
        updater.profile = {'ssh_alias': 'game'}
        sentinel = object()
        with patch.object(publisher, 'run', return_value=sentinel) as run:
            result = updater.worker_transport('deploy', ['sudo', 'deploy'], True, 'a' * 64)
        self.assertIs(result, sentinel)
        run.assert_called_once_with(
            updater, ['sudo', 'deploy'], True, 'a' * 64, windows_update.SSH_OPTIONS)

    def test_historical_worker_without_exchange_uses_plain_ssh(self):
        updater = windows_update.Update.__new__(windows_update.Update)
        updater.worker_code = b'historical worker without edge exchange'
        updater.profile = {'ssh_alias': 'game'}
        sentinel = object()
        updater.ssh = lambda *args, **kwargs: sentinel
        self.assertIs(updater.worker_transport('deploy', ['sudo', 'deploy'], True, 'a' * 64),
                      sentinel)

    def test_exchange_is_inside_deploy_health_guard_and_installed_options_remain(self):
        source = REMOTE_PATH.read_text(encoding='utf-8')
        health = source.index('restart(); after = healthy(); actual = binary()')
        exchange = source.index('post_health_edge = await_post_health_edge(s,')
        rollback = source.index('except BaseException:', exchange)
        self.assertLess(health, exchange)
        self.assertLess(exchange, rollback)
        self.assertIn('def installed_candidate_options(', source)
        self.assertGreaterEqual(source.count('installed_candidate_options('), 3)


class TargetClockTests(unittest.TestCase):
    def test_future_timestamp_waits_once(self):
        target = 1_800_000_000.0
        sleeps = []
        values = iter([target, target + 11.48])

        def sleep(seconds):
            sleeps.append(seconds)

        stamp = datetime.fromtimestamp(target + 11.43, timezone.utc).isoformat()
        result = wait_for_remote_evidence_clock([stamp], lambda: next(values), sleep)
        self.assertEqual(len(sleeps), 1)
        self.assertGreaterEqual(result['oldestAgeSeconds'], 0)

    def test_expired_timestamp_is_rejected_without_sleep(self):
        target = 1_800_000_000.0
        sleeps = []
        stamp = datetime.fromtimestamp(target - 300.001, timezone.utc).isoformat()
        with self.assertRaises(ValueError):
            wait_for_remote_evidence_clock([stamp], lambda: target, sleeps.append)
        self.assertEqual(sleeps, [])


if __name__ == '__main__':
    unittest.main()
