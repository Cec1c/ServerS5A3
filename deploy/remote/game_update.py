"""Game-only deployment worker. Call through GAME_UPDATE_REMOTE.bat from an agent.

The same deployment lock protects preparation, switching, rollback and retention.
All credentials are read on the server and never returned in snapshots.
"""

TOURNAMENT_KEY = 'DNFBRIDGE_DUNGEON_TOURNAMENT'
MAIN_QUEST_EXP_KEY = 'DNFBRIDGE_MAIN_QUEST_EXP_MULTIPLIER'
MAIN_QUEST_PERCENT_KEY = 'DNFBRIDGE_MAIN_QUEST_EXP_PERCENT'
GAME_CONFIG_FILES = ['runtime/docker/instance.json', 'runtime/docker/compose.json',
                     'runtime/docker/server.env', 'runtime/active-game-baseline.json']
RUNTIME_CHANNEL_TARGET = 'runtime/docker/data/channel.override.etc'
RUNTIME_CHANNEL_CONTAINER = '/runtime/data/dnf/channel.override.etc'
RUNTIME_CHANNEL_FILE = 'channel.override.etc'


def checked_runtime_channel_phase(spec, channel_source, channel_sha256, mode):
    if mode not in ('prepare', 'generate', 'deploy', 'rollback'):
        raise ValueError('invalid runtime channel phase')
    # Rollback is invoked after the successful transition and must recognize
    # the exact deployed state. Preparation/switch still require the old state.
    prefix = '' if mode == 'rollback' else 'base_'
    if channel_source != spec[prefix + 'channel_source']:
        raise ValueError('active channel source differs for ' + mode)
    if channel_sha256 != spec[prefix + 'sha256']:
        raise ValueError('active channel bytes differ for ' + mode)


def checked_empire_area_map_delta(current, candidate):
    newline = b'\r\n' if b'\r\n' in current else b'\n'
    old = b'[dungeon]' + newline + b'`[tagtournament]`' + newline + '`帝国竞技场`'.encode() + newline + b'[/dungeon]' + newline
    new = old.replace(b'[/dungeon]' + newline, b'320' + newline + b'336' + newline + b'[/dungeon]' + newline)
    if current.count(old) != 1 or candidate != current.replace(old, new, 1):
        raise ValueError('Imperial channel update changes bytes beyond the reviewed dungeon mapping')


def expected_server_ports(bind, ports):
    if (not isinstance(ports, list) or not ports or
            any(type(port) is not int or port < 10001 or port > 10999 for port in ports) or
            len(ports) != len(set(ports))):
        raise ValueError('invalid game channel ports')
    return ([f'127.0.0.1:{port}:{port}/tcp' for port in (7001, 17002, 18111)] +
            [f'{bind}:{port}:{port}/tcp' for port in ports] +
            [f'{bind}:30000-30063:30000-30063/udp'])


def channel_info_env_path(file_name):
    if file_name not in ('channel_info.etc', 'channel.override.etc'):
        raise ValueError('invalid channel file name')
    return 'data/dnf/' + file_name


def player_endpoint_projection(instance, catalog, manage):
    bind, advertised, bootstrap = manage.player_endpoints(instance, catalog)
    return {'bind': bind, 'advertised': advertised, 'bootstrap': bootstrap,
            'serverPorts': expected_server_ports(bind, catalog['ports'])}


def projected_server_ports(instance, catalog, manage):
    return player_endpoint_projection(instance, catalog, manage)['serverPorts']


def checked_runtime_channel_host(instance, catalog, manage, published_ports, port):
    endpoint = player_endpoint_projection(instance, catalog, manage)
    bindings = (published_ports or {}).get(f'{port}/tcp') or []
    if not any((binding or {}).get('HostIp') == endpoint['bind'] and
               (binding or {}).get('HostPort') == str(port) for binding in bindings):
        raise ValueError('runtime channel host binding differs from manage.player_endpoints bind')
    return endpoint


def rollback_config_paths(extra=()):
    return list(dict.fromkeys(GAME_CONFIG_FILES + list(extra)))


def tournament_mode(manifest):
    mode = manifest.get('dungeonTournament')
    if 'dungeonTournament' in manifest and mode not in ('off', 'vendor'):
        raise ValueError('dungeonTournament must be off or vendor')
    return mode


def checked_main_quest_exp(value):
    if type(value) is not int or not 1 <= value <= 100:
        raise ValueError('main quest experience multiplier must be an integer from 1 to 100')
    return value


def main_quest_exp_multiplier(manifest):
    if 'mainQuestExpMultiplier' not in manifest: return None
    return checked_main_quest_exp(manifest['mainQuestExpMultiplier'])


def checked_main_quest_percent(value):
    if type(value) is not int or not 0 <= value <= 100:
        raise ValueError('main quest experience percent must be an integer from 0 to 100')
    return value


def main_quest_exp_percent(manifest):
    if 'mainQuestExpPercent' not in manifest: return None
    return checked_main_quest_percent(manifest['mainQuestExpPercent'])


def target_instance(instance, image, mode, multiplier=None, percent=None, channel_source=None):
    updated = dict(instance, server_image=image)
    if mode is not None:
        if mode not in ('off', 'vendor'): raise ValueError('Invalid tournament mode')
        updated['dungeon_tournament'] = mode
    if multiplier is not None:
        updated['main_quest_exp_multiplier'] = checked_main_quest_exp(multiplier)
    if percent is not None:
        updated['main_quest_exp_percent'] = checked_main_quest_percent(percent)
    if channel_source is not None:
        if channel_source != 'external:approved-119-empire-arena-pvfmap-v2':
            raise ValueError('Invalid runtime channel source')
        updated['channel_source'] = channel_source
    return updated


def target_environment(environment, mode, multiplier=None, percent=None):
    updated = dict(environment)
    if mode is not None:
        if mode not in ('off', 'vendor'): raise ValueError('Invalid tournament mode')
        updated[TOURNAMENT_KEY] = mode
    if multiplier is not None:
        updated[MAIN_QUEST_EXP_KEY] = str(checked_main_quest_exp(multiplier))
    if percent is not None:
        updated[MAIN_QUEST_PERCENT_KEY] = str(checked_main_quest_percent(percent))
    return updated


def target_env_bytes(before, mode, multiplier=None, percent=None):
    if mode is None and multiplier is None and percent is None: return before
    changes = {}
    if mode is not None:
        if mode not in ('off', 'vendor'): raise ValueError('Invalid tournament mode')
        changes[TOURNAMENT_KEY] = mode
    if multiplier is not None:
        changes[MAIN_QUEST_EXP_KEY] = str(checked_main_quest_exp(multiplier))
    if percent is not None:
        changes[MAIN_QUEST_PERCENT_KEY] = str(checked_main_quest_percent(percent))
    lines = before.splitlines(keepends=True)
    if before and not before.endswith(b'\n'):
        raise ValueError('Ambiguous environment file')
    for key, value in changes.items():
        prefix = (key + '=').encode()
        found = [i for i, line in enumerate(lines) if line.startswith(prefix)]
        if len(found) > 1: raise ValueError('Ambiguous environment file')
        replacement = prefix + value.encode() + b'\n'
        if found: lines[found[0]] = replacement
        else:
            order = (TOURNAMENT_KEY, MAIN_QUEST_EXP_KEY, MAIN_QUEST_PERCENT_KEY)
            later = tuple((k + '=').encode() for k in order[order.index(key) + 1:])
            index = next((i for i, line in enumerate(lines) if later and line.startswith(later)), len(lines))
            lines.insert(index, replacement)
    return b''.join(lines)


def restore_game_configs(root, backup, atomic, extra=()):
    for rel in rollback_config_paths(extra):
        atomic(root / rel, (backup / rel).read_bytes())


def tournament_snapshot(instance, container_env, process_env):
    keys = (TOURNAMENT_KEY, 'DNFBRIDGE_CURRENT_CLIENT_VERSION',
            'DNFBRIDGE_DUNGEON_TOURNAMENT_REWARD_ACK', 'DNFBRIDGE_DUNGEON_TOURNAMENT_REWARD_SOLICIT')
    current = {k: container_env.get(k) for k in keys}
    process = {k: process_env.get(k) for k in keys}
    assert current == process, 'process and container tournament profile differ'
    mode = instance.get('dungeon_tournament')
    if mode is not None:
        assert mode in ('off', 'vendor') and current[TOURNAMENT_KEY] == mode
    return {'sourceMode': mode, 'container': current, 'process': process}


def main_quest_exp_snapshot(instance, container_env, process_env):
    source = instance.get('main_quest_exp_multiplier')
    if 'main_quest_exp_multiplier' in instance: checked_main_quest_exp(source)
    current = container_env.get(MAIN_QUEST_EXP_KEY)
    process = process_env.get(MAIN_QUEST_EXP_KEY)
    assert current == process, 'process and container main quest experience multiplier differ'
    if source is not None:
        assert current == str(source), 'source and runtime main quest experience multiplier differ'
    else:
        assert current in (None, '1'), 'implicit main quest experience multiplier must remain 1'
    percent = instance.get('main_quest_exp_percent')
    if 'main_quest_exp_percent' in instance: checked_main_quest_percent(percent)
    current_percent = container_env.get(MAIN_QUEST_PERCENT_KEY)
    process_percent = process_env.get(MAIN_QUEST_PERCENT_KEY)
    assert current_percent == process_percent, 'process and container main quest experience percent differ'
    if percent is not None:
        assert current_percent == str(percent), 'source and runtime main quest experience percent differ'
    else:
        assert current_percent in (None, '0'), 'implicit main quest experience percent must remain 0'
    return {'sourceMultiplier': source, 'effectiveMultiplier': source if source is not None else 1,
            'containerValue': current, 'processValue': process,
            'sourcePercent': percent, 'effectivePercent': percent if percent is not None else 0,
            'containerPercent': current_percent, 'processPercent': process_percent}

def archive_members(archive, files):
    """Validate the complete archive before creating any source file."""
    from pathlib import PurePosixPath
    members = archive.getmembers()
    names = []
    for member in members:
        path = PurePosixPath(member.name)
        if (not member.isfile() or path.is_absolute() or "\\" in member.name or ":" in member.name or
                path.as_posix() != member.name or any(p in (".", "..") for p in member.name.split("/"))):
            raise ValueError('Unsafe source archive member')
        names.append(member.name)
    if len(names) != len(set(names)) or set(names) != set(files):
        raise ValueError('Source archive differs from manifest')
    return members


def management_containers(instance):
    """An explicitly disabled legacy container requires the gateway adapter."""
    return ['server', 'db'] if instance.get('gm_enabled') is False else ['server', 'gm', 'db']


def gateway_management_snapshot(root, run, sha):
    import hashlib, http.client, ipaddress, json, ssl
    import urllib.parse, urllib.request
    from pathlib import Path
    cfg = json.loads((root / 'runtime/player-gateway/gateway.json').read_text())
    management = cfg.get('management')
    assert management, 'disabled legacy GM requires configured gateway management'
    active = json.loads((root / 'runtime/purplemagic/active.json').read_text())
    host, port = management['listen'].rsplit(':', 1)
    assert ipaddress.ip_address(host).is_loopback and cfg['launcher_hub']['admin_listen'] == management['listen']
    pid = run('systemctl', 'show', 'servers5a3-player-gateway.service', '-p', 'MainPID', '--value')
    assert pid.isdecimal() and int(pid) > 0
    binary = sha(Path('/proc') / pid / 'exe')
    assert binary == active['gatewaySha256']
    pvf = sha(Path(management['pvfPath']))
    assert pvf == management['pvfSha256'] == active['pvfSha256']
    index_root = Path(management['itemIndexPath']).parent.resolve()
    expected_names = {'items.json', 'pvf-directory.bin', 'item-details-v3-' + pvf + '.bin',
                      'quests-v1-' + pvf + '.bin', 'professions-v1-' + pvf + '.bin', 'expert-jobs-v1-' + pvf + '.json'}
    assert set(active['indexes']) == expected_names, 'incomplete GM index generation'
    assert Path(management['pvfIndexPath']).resolve() == index_root / 'pvf-directory.bin'
    files = [root / 'runtime/purplemagic/active.json']
    for name, digest in active['indexes'].items():
        path = index_root / name
        assert sha(path) == digest, 'GM index changed: ' + name
        files.append(path)
    catalog = management.get('catalog')
    external = active.get('externalGM')
    headers = {}
    if catalog:
        assert external and external['pvfSha256'] == pvf and external['indexes'] == active['indexes'], 'external GM receipt mismatch'
        endpoint = urllib.parse.urlsplit(catalog['url'])
        assert endpoint.scheme == 'http' and ipaddress.ip_address(endpoint.hostname).is_loopback
        assert not endpoint.username and not endpoint.password and endpoint.path in ('', '/') and not endpoint.query and not endpoint.fragment
        assert catalog['pvfSha256'] == pvf and len(catalog['token']) >= 32 and len(management['serviceToken']) >= 32
        protocol = 'purplemagic-service-v1'
        request = urllib.request.Request(catalog['url'].rstrip('/') + '/internal/catalog/v1/resolve',
            data=b'{"operation":"metadata"}', headers={'Content-Type': 'application/json',
            'Authorization': 'Bearer ' + catalog['token'], 'X-PurpleMagic-Protocol': protocol, 'X-PVF-SHA256': pvf})
        class NoRedirect(urllib.request.HTTPRedirectHandler):
            def redirect_request(self, *args, **kwargs): return None
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
        with opener.open(request, timeout=10) as response:
            assert response.status == 200 and response.headers.get('X-PurpleMagic-Protocol') == protocol and response.headers.get('X-PVF-SHA256') == pvf
            metadata = json.loads(response.read(65537))
            assert not metadata.get('error') and metadata['value']['ManifestHash'] == external['catalogManifestHash']
        headers = {'Authorization': 'Bearer ' + management['serviceToken'], 'X-PurpleMagic-Protocol': protocol, 'X-PVF-SHA256': pvf}
    else:
        assert not external, 'external GM receipt requires catalog configuration'
    certificate = ssl.PEM_cert_to_DER_cert(Path(cfg['certificate']).read_text())
    connection = http.client.HTTPSConnection(host, int(port), context=ssl._create_unverified_context(), timeout=10)
    try:
        connection.connect()
        assert hashlib.sha256(connection.sock.getpeercert(True)).digest() == hashlib.sha256(certificate).digest()
        if catalog:
            connection.request('GET', '/api/v1/capabilities', headers=headers)
        else:
            connection.request('GET', '/api/status')
        response = connection.getresponse()
        status = json.loads(response.read(65537))
        if catalog:
            assert response.status == 200 and response.getheader('X-PurpleMagic-Protocol') == protocol and response.getheader('X-PVF-SHA256') == pvf
            assert status['data']['apiVersion'] == '1' and status['data']['itemIndexReady'] and status['data']['features']['accounts.read']
        else:
            assert response.status == 200 and status.get('configured') and status.get('toolVersion') == '90cn-gm-v1'
    finally:
        connection.close()
    state = {'mode': 'external-gm' if catalog else 'gateway', 'pid': int(pid), 'gatewaySha256': binary, 'pvfSha256': pvf, 'indexes': active['indexes']}
    if catalog:
        state['externalGM'] = external
    return state, files



def checked_edge_evidence(raw, expected_sha256, now):
    """The controller reads this over its pinned SSH connection for each worker call."""
    import hashlib, json, datetime
    if len(raw) > 1024 * 1024 or hashlib.sha256(raw).hexdigest() != expected_sha256:
        raise ValueError('Split gateway evidence digest mismatch')
    value = json.loads(raw)
    captured = datetime.datetime.fromisoformat(value['checkedAt'])
    if captured.tzinfo is None or not 0 <= (now - captured).total_seconds() <= 300:
        raise ValueError('Split gateway evidence expired')
    if value.get('format') != 1 or value['management']['mode'] != 'external-gm':
        raise ValueError('Unsupported split gateway evidence')
    if value['published']['profile']['PvfSha256'] != value['management']['pvfSha256']:
        raise ValueError('Split gateway PVF mismatch')
    if not value.get('configHashes'):
        raise ValueError('Split gateway configuration evidence missing')
    return value


def backup_game_context(root, hub, backup, config_hashes, edge_evidence=None, extra_paths=()):
    import shutil
    paths = list(dict.fromkeys([root / rel for rel in config_hashes] + list(extra_paths)))
    if not edge_evidence:
        paths.append(hub / 'state.json')
    for path in paths:
        target = backup / path.relative_to(root)
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(path, target)
    if edge_evidence:
        shutil.copy2(edge_evidence, backup / 'edge-snapshot.json')


def checked_emergency_scope(src, old):
    if src.get('integrationMode') != 'emergency-selected-mr':
        return
    e = src['emergency']
    if any(key in src for key in ('dungeonTournament', 'mainQuestExpMultiplier', 'mainQuestExpPercent')):
        raise ValueError('Emergency cannot change runtime settings')
    if e['baseArchiveSha256'] != old['active']['sourceArchiveSha256']:
        raise ValueError('Emergency active archive changed')
    required = {'Docs/CHARACTER_SELECTION_TIMEOUT.md', 'changlog.md'} | {
        'go-server/internal/services/dnfbridge/' + name for name in
        ('game_select_scene_entry.go', 'game_select_state.go', 'character_rename_current.go', 'character_select_deadline_test.go')}
    changes = e['changes']
    if len(changes) != 6 or {x['path'] for x in changes} != required or set(src['changedFiles']) != required:
        raise ValueError('Emergency requires the exact MR226 paths')
    expected = dict(old['sourceManifest']['files'])
    for entry in changes:
        if expected.get(entry['path']) != entry['beforeSha256']:
            raise ValueError('Emergency before bytes differ')
        expected[entry['path']] = entry['afterSha256']
    if expected != src['files']:
        raise ValueError('Unreviewed emergency bytes')
    if src['upstream'] != old['sourceManifest']['upstream']:
        raise ValueError('Emergency must not claim full integration')


def checked_authority_proof(raw, expected_digest, nonce, run_id, phase, manifest, now):
    import hashlib, json, datetime
    if not expected_digest or hashlib.sha256(raw).hexdigest() != expected_digest:
        raise ValueError('Authoritative pull proof hash mismatch')
    proof = json.loads(raw)
    expected = {'format': 1, 'runId': run_id, 'nonce': nonce, 'phase': phase,
                'repository': 'https://gitgud.io/16ww/servers5a3.git', 'branch': 'master',
                'passed': True, 'containsMaster': True, 'after': manifest['upstream'],
                'candidate': manifest['sourceCommit']}
    if manifest.get('integrationMode') == 'emergency-selected-mr':
        emergency = manifest['emergency']
        if (emergency.get('selectedMR') != 226 or
            emergency.get('selectedMergeCommit') != '963eabb6b125b09fba4312f650a81d90a96cc96a' or
            emergency.get('candidateCommit') != manifest['sourceCommit'] or
            emergency.get('baseCommit') != manifest['baseCommit']):
            raise ValueError('Emergency MR/source binding differs')
        expected.update(containsMaster=False, after=manifest['observedUpstream'],
                        integrationMode='emergency-selected-mr', selectedMR=226,
                        selectedMergeCommit=emergency['selectedMergeCommit'],
                        emergencyEvidenceSha256=emergency['evidenceSha256'])
    if not nonce or manifest.get('authorityInvocation') != nonce or any(proof.get(k) != v for k, v in expected.items()):
        raise ValueError('Authoritative pull proof belongs to another run, phase or candidate')
    age = (now - datetime.datetime.fromisoformat(proof['checkedAt'])).total_seconds()
    if not 0 <= age <= 300:
        raise ValueError('Authoritative pull proof expired')
    return proof


def main():
    import argparse
    from pathlib import Path
    import re
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('mode', choices=['snapshot', 'edge-snapshot', 'prepare', 'generate', 'deploy', 'verify', 'rollback'])
    parser.add_argument('--root', type=Path, required=True)
    parser.add_argument('--run-id', default='inspect')
    parser.add_argument('--project', default='servers5a3-remote')
    parser.add_argument('--image-base')
    parser.add_argument('--edge-evidence', type=Path)
    parser.add_argument('--edge-evidence-sha256')
    parser.add_argument('--authority-nonce')
    parser.add_argument('--authority-proof-sha256')
    args = parser.parse_args()
    if not re.fullmatch(r'/[A-Za-z0-9_./-]+', str(args.root)) or '..' in args.root.parts or str(args.root) == '/':
        raise ValueError('Invalid remote root')
    if not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9._-]{0,79}', args.run_id):
        raise ValueError('Invalid run ID')
    if not re.fullmatch(r'[a-z0-9][a-z0-9_-]{0,62}', args.project):
        raise ValueError('Invalid compose project')
    import json, hashlib, subprocess, os, fcntl, sys, tarfile, shutil, time, datetime, gzip, socket, urllib.request, importlib.util

    os.umask(0o077)
    r = args.root.resolve()
    s = r / 'runtime/game-updates' / args.run_id
    d = r / 'runtime/docker'
    h = r / 'runtime/launcher-hub'
    lock = (r / 'runtime/guild-deployment.lock').open('a')
    fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
    def read(p): return json.loads(p.read_text())
    def sha(p):
        with p.open('rb') as f: return hashlib.file_digest(f, 'sha256').hexdigest()
    def run(*args): return subprocess.check_output(args, text=True, stderr=subprocess.PIPE).strip()
    def inspect(name): return json.loads(run('docker', 'inspect', args.project + '-' + name + '-1'))[0]
    def digest_json(value): return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(',', ':')).encode()).hexdigest()
    def write(path, value): path.write_text(json.dumps(value, indent=2))
    def binary():
        pids = [line.split()[0] for line in run('docker', 'top', args.project + '-server-1', '-eo', 'pid,comm').splitlines()[1:] if line.split()[1] == 'DNF90Server']
        assert len(pids) == 1
        return {'pid': int(pids[0]), 'sha256': sha(Path('/proc') / pids[0] / 'exe')}
    def atomic(path, data):
        temporary = path.with_name(path.name + '.game-update-new')
        assert not path.is_symlink() and not temporary.exists()
        stat = path.stat()
        with temporary.open('xb') as f: f.write(data); f.flush(); os.fsync(f.fileno())
        os.chmod(temporary, stat.st_mode & 0o777); os.chown(temporary, stat.st_uid, stat.st_gid)
        os.replace(temporary, path)
    if args.mode == 'edge-snapshot':
        management, evidence_files = gateway_management_snapshot(r, run, sha)
        cfg = read(r / 'runtime/player-gateway/gateway.json')
        evidence_files += [r / 'runtime/player-gateway/gateway.json', Path(cfg['launcher_hub']['profile_file']), h / 'content-origins/Script.pvf.json']
        print(json.dumps({'format': 1, 'checkedAt': datetime.datetime.now(datetime.timezone.utc).isoformat(),
                          'published': read(h / 'state.json')['published'], 'management': management,
                          'configHashes': {str(p): sha(p) for p in evidence_files}}))
        return
    def edge():
        if args.edge_evidence is None: return None
        expected = r / 'runtime/game-update-edge' / (args.run_id + '.json')
        assert args.edge_evidence == expected and not expected.is_symlink(), 'Invalid edge evidence path'
        return checked_edge_evidence(expected.read_bytes(), args.edge_evidence_sha256, datetime.datetime.now(datetime.timezone.utc))
    def publication():
        value = edge()
        return value['published'] if value else read(h / 'state.json')['published']
    def channel_file_name():
        name = read(d / 'instance.json').get('channel_file', 'channel_info.etc')
        assert Path(name).name == name and name in ('channel_info.etc', 'channel.override.etc')
        return name
    def channel_path():
        return d / 'data' / channel_file_name()
    def channel_container_path():
        return '/runtime/data/dnf/' + channel_file_name()
    def channel_rows(raw):
        section = re.search(rb'(?ms)^[ \t]*\[server\][^\r\n]*\r?\n(.*?)^[ \t]*\[/server\][ \t]*\r?\n?', raw)
        assert section, 'missing complete [server] channel section'
        row_pattern = re.compile(rb'(?m)^[ \t]*(\d+)[ \t]+`([^`\r\n]*)`[ \t]+(\d+)[ \t]+`([^`\r\n]*)`[^\r\n]*\r?$')
        rows = []
        for match in row_pattern.finditer(section.group(1)):
            group = match.group(4).decode('utf-8').strip()
            assert group.startswith('[') and group.endswith(']'), 'invalid channel group'
            rows.append((int(match.group(1)), match.group(2).decode('utf-8'),
                         int(match.group(3)), group[1:-1]))
        return rows
    def channel_metadata_path():
        prior = d / 'data/channel-manifest.json'
        return prior if prior.is_file() else d / 'data/manifest.json'
    container_names = management_containers(read(d / 'instance.json'))
    def management_snapshot():
        if 'gm' in container_names:
            return {'mode': 'container'}, []
        assert 'gm' not in read(d / 'compose.json')['services'], 'legacy GM is still in active compose'
        value = edge()
        if value:
            return dict(value['management'], splitConfigHashes=value['configHashes']), []
        legacy = inspect('gm')
        assert not legacy['State']['Running'], 'legacy GM must remain stopped'
        return gateway_management_snapshot(r, run, sha)
    if args.mode in ['snapshot', 'verify']:
        active = read(r / 'runtime/active-game-baseline.json')
        manifest_path = Path(active['sourceDirectory']).parent / 'game-source-manifest.json'
        manifest = read(manifest_path)
        assert active['sourceCommit'] == manifest['sourceCommit']
        for rel, digest in manifest['files'].items():
            assert sha(Path(active['sourceDirectory']) / rel) == digest, 'deployed source changed: ' + rel
        containers = {}
        for name in container_names:
            c = json.loads(run('docker', 'inspect', args.project + '-' + name + '-1'))[0]
            env = dict(x.split('=', 1) for x in c['Config']['Env'])
            containers[name] = {'id': c['Id'], 'image': c['Image'], 'health': c['State'].get('Health', {}).get('Status'), 'started': c['State']['StartedAt'], 'restartCount': c['RestartCount'], 'environmentSha256': digest_json(env), 'user': c['Config']['User']}
            assert containers[name]['health'] == 'healthy', name
        pids = [line.split()[0] for line in run('docker', 'top', args.project + '-server-1', '-eo', 'pid,comm').splitlines()[1:] if line.split()[1] == 'DNF90Server']
        assert len(pids) == 1
        binary_hash = sha(Path('/proc') / pids[0] / 'exe')
        assert binary_hash == active['gameExe']['sha256'] == manifest['serverSha256']
        assert containers['server']['image'] == active['image']
        pub = publication()
        pvf = sha(d / 'data/Script.pvf')
        assert pub['profile']['ExeSha256'] == active['exeSha256']
        assert pub['profile']['PvfSha256'] == active['pvfSha256'] == pvf
        for name, path in [('server', '/runtime/data/dnf/Script.pvf')] + ([('gm', '/assets/Script.pvf')] if 'gm' in container_names else []):
            assert run('docker', 'exec', args.project + '-' + name + '-1', 'sha256sum', path).split()[0] == pvf
        management_state, management_files = management_snapshot()
        if 'gm' in container_names:
            assert pvf in (d / 'gm/state/item-index.json').read_text()
        else:
            assert management_state['pvfSha256'] == pvf
        channel_file = channel_file_name()
        channel = sha(channel_path())
        assert channel == active['channelSha256']
        channel_container = run('docker', 'exec', args.project + '-server-1', 'sha256sum', channel_container_path()).split()[0]
        assert channel_container == channel, 'container channel file differs from host file'
        channel_entries = channel_rows(channel_path().read_bytes())
        channel_catalog = read(channel_metadata_path())
        assert channel_catalog['channels'] == len(channel_entries), 'channel metadata count differs from ETC'
        assert channel_catalog['ports'] == [10000 + row[0] for row in channel_entries], 'channel metadata ports differ from ETC'
        server_projection = inspect('server')
        server_environment = dict(x.split('=', 1) for x in server_projection['Config']['Env'])
        assert server_environment.get('DNFBRIDGE_CHANNEL_INFO_PATH') == channel_info_env_path(channel_file), 'container channel path differs'
        channel_state = {'name': channel_file, 'channelSource': read(d / 'instance.json').get('channel_source'),
                         'hostPath': str(channel_path().relative_to(r)),
                         'containerPath': channel_container_path(), 'sha256': channel,
                         'containerSha256': channel_container, 'rows': len(channel_entries),
                         'empireRows': [list(row) for row in channel_entries if row[2] == 39 or row[3] == 'tagtournament'],
                         'ports': channel_catalog['ports'], 'catalogSha256': sha(channel_metadata_path())}
        files = [d / x for x in ['instance.json', 'compose.json', 'server.env', 'database.env', 'data/manifest.json']]
        files.append(channel_metadata_path())
        if 'gm' in container_names: files.append(d / 'gm/config.json')
        files += [r / 'runtime/active-game-baseline.json', r / 'deploy/docker/manage.py', r / 'deploy/remote/backup.py']
        if not edge():
            files += [h / 'content-origins/Script.pvf.json', r / 'runtime/player-gateway/gateway.json', Path(read(r / 'runtime/player-gateway/gateway.json')['launcher_hub']['profile_file'])]
        files += management_files
        config_hashes = {str(p.relative_to(r)): sha(p) for p in files}
        inst = read(d / 'instance.json')
        channel_state['listenerChecks'] = []
        channel_state['listenerHostSource'] = 'manage.player_endpoints.bind'
        empire_entries = [row for row in channel_entries if row[2] == 39 or row[3] == 'tagtournament']
        if empire_entries:
            manage_spec = importlib.util.spec_from_file_location('manage_snapshot', r / 'deploy/docker/manage.py')
            manage_snapshot = importlib.util.module_from_spec(manage_spec); manage_spec.loader.exec_module(manage_snapshot)
            endpoint = player_endpoint_projection(inst, channel_catalog, manage_snapshot)
            published_ports = server_projection['NetworkSettings']['Ports']
            for row in empire_entries:
                port = 10000 + row[0]
                checked_runtime_channel_host(inst, channel_catalog, manage_snapshot, published_ports, port)
                with socket.create_connection((endpoint['bind'], port), timeout=3):
                    pass
                channel_state['listenerChecks'].append(port)
        req = urllib.request.Request('http://' + inst['server_ip'] + ':17002/api/player/admin', data=b'{"action":"sessions"}', headers={'Content-Type': 'application/json', 'Authorization': 'Bearer ' + inst['player_admin_secret']})
        sessions = json.load(urllib.request.urlopen(req, timeout=10))
        assert sessions['ok']
        counts = {'leases': len(sessions['sessions']), 'connections': sum(x.get('connections', 0) for x in sessions['sessions'])}
        snapshot = ({'checkedAt': datetime.datetime.now(datetime.timezone.utc).isoformat(), 'active': active, 'sourceManifestSha256': sha(manifest_path), 'sourceManifest': manifest, 'verifiedSourceFiles': len(manifest['files']), 'containers': containers, 'serverPid': int(pids[0]), 'serverSha256': binary_hash, 'pvfSha256': pvf, 'channelSha256': channel, 'channel': channel_state, 'exeSha256': pub['profile']['ExeSha256'], 'sequence': pub['sequence'], 'publishedSha256': digest_json(pub), 'configHashes': config_hashes, 'sessions': counts})
        snapshot['management'] = management_state
        process_env = dict(line.decode().split('=', 1) for line in (Path('/proc') / pids[0] / 'environ').read_bytes().split(b'\0') if b'=' in line)
        assert process_env.get('DNFBRIDGE_CHANNEL_INFO_PATH') == channel_info_env_path(channel_file), 'process channel path differs'
        channel_state['processPath'] = process_env['DNFBRIDGE_CHANNEL_INFO_PATH']
        snapshot['tournament'] = tournament_snapshot(inst, dict(x.split('=', 1) for x in inspect('server')['Config']['Env']), process_env)
        snapshot['mainQuestExperience'] = main_quest_exp_snapshot(inst, dict(x.split('=', 1) for x in inspect('server')['Config']['Env']), process_env)
        if args.mode == "verify":
            snapshot = {key: snapshot[key] for key in ["active", "serverSha256", "containers", "pvfSha256", "channelSha256", "channel", "exeSha256", "sequence", "sessions", "management", "tournament", "mainQuestExperience"]}
        print(json.dumps(snapshot))
        return
    old = read(s / 'runtime-before.json')
    src = read(s / 'game-source-manifest.json')
    config_mode = tournament_mode(src)
    config_multiplier = main_quest_exp_multiplier(src)
    config_percent = main_quest_exp_percent(src)
    config_changed = config_mode is not None or config_multiplier is not None or config_percent is not None
    runtime_channel = src.get('runtimeChannelUpdate')
    if runtime_channel is not None:
        required = {'target', 'container_path', 'channel_file', 'base_channel_source', 'channel_source', 'base_sha256', 'sha256',
                    'pvf_sha256', 'base_channels', 'channels', 'channel_id', 'port',
                    'channel_type', 'group', 'name'}
        assert isinstance(runtime_channel, dict) and set(runtime_channel) == required, 'invalid runtime channel binding'
        assert runtime_channel['target'] == RUNTIME_CHANNEL_TARGET, 'runtime channel target differs'
        assert runtime_channel['container_path'] == RUNTIME_CHANNEL_CONTAINER, 'runtime channel container path differs'
        assert runtime_channel['channel_file'] == RUNTIME_CHANNEL_FILE, 'runtime channel file differs'
        assert runtime_channel['base_channel_source'] == 'external:approved-119-empire-arena-v1', 'runtime channel base source differs'
        assert runtime_channel['channel_source'] == 'external:approved-119-empire-arena-pvfmap-v2', 'runtime channel source differs'
        assert runtime_channel['target'] == str(channel_path().relative_to(r)), 'active channel path differs'
        assert read(d / 'instance.json').get('channel_file', 'channel_info.etc') == RUNTIME_CHANNEL_FILE
        checked_runtime_channel_phase(runtime_channel, read(d / 'instance.json').get('channel_source'), sha(channel_path()), args.mode)
        assert runtime_channel['base_sha256'] == old['channelSha256'], 'runtime channel base differs'
        assert runtime_channel['pvf_sha256'] == old['pvfSha256'], 'runtime channel PVF binding differs'
        assert (runtime_channel['base_channels'], runtime_channel['channels'], runtime_channel['channel_id'],
                runtime_channel['port'], runtime_channel['channel_type'], runtime_channel['group'], runtime_channel['name']) == (119, 119, 187, 10187, 39, 'tagtournament', '帝国竞技场')
        if args.mode != 'rollback':
            assert sha(s / RUNTIME_CHANNEL_FILE) == runtime_channel['sha256'], 'runtime channel candidate differs'
    rollback_extra = list(dict.fromkeys(
        [runtime_channel['target'], str(channel_metadata_path().relative_to(r))]
        if runtime_channel is not None else []))
    rollback_configs = rollback_config_paths(rollback_extra)
    assert src['baseCommit'] == old['active']['sourceCommit']
    assert re.fullmatch(r'[0-9a-f]{40}', src['sourceCommit'])
    assert sha(s / 'DNF90Server') == src['serverSha256']
    assert sha(s / 'game-source.tar.gz') == src['sourceArchiveSha256']
    if args.mode != 'rollback':
        assert sha(s / 'game-source.bundle') == src['sourceGitBundleSha256'], 'source Git bundle differs'
    def authority_check():
        proof = checked_authority_proof((s / 'authority-current.json').read_bytes(),
            args.authority_proof_sha256, args.authority_nonce, args.run_id, args.mode, src,
            datetime.datetime.now(datetime.timezone.utc))
        write(s / ('authority-' + args.mode + '.json'), proof)
    def validate_before():
        authority_check()
        checked_emergency_scope(src, old)
        assert read(r / 'runtime/active-game-baseline.json') == old['active'], 'active baseline moved'
        for rel, digest in old['configHashes'].items(): assert sha(r / rel) == digest, 'configuration changed: ' + rel
        assert digest_json(publication()) == old['publishedSha256'], 'player publication changed'
        assert sha(d / 'data/Script.pvf') == old['pvfSha256']
        assert sha(channel_path()) == old['channelSha256']
        assert binary()['sha256'] == old['serverSha256']
        assert management_snapshot()[0] == old.get('management', {'mode': 'container'}), 'GM deployment changed'
        for name, previous in old['containers'].items():
            current = inspect(name)
            assert current['Id'] == previous['id'] and current['Image'] == previous['image'], name + ' changed'
            assert current['State'].get('Health', {}).get('Status') == 'healthy', name + ' unhealthy'
            assert digest_json(dict(x.split('=', 1) for x in current['Config']['Env'])) == previous['environmentSha256']

    mode = args.mode
    if mode == 'prepare':
        assert not (s / 'prepared-game.json').exists()
        validate_before()
        assert shutil.disk_usage(r).free > 2 * 1024**3, 'insufficient backup/build space'
        tree = s / 'source'; tree.mkdir()
        with tarfile.open(s / 'game-source.tar.gz') as archive:
            for member in archive_members(archive, src['files']):
                target = tree / member.name
                assert member.isfile() and target.resolve().is_relative_to(tree.resolve()) and member.name in src['files']
                target.parent.mkdir(parents=True, exist_ok=True)
                with archive.extractfile(member) as stream: target.write_bytes(stream.read())
                target.chmod(member.mode & 0o777)
        for rel, digest in src['files'].items(): assert sha(tree / rel) == digest, rel
        current = inspect('server')
        base = 'servers5a3:before-' + args.run_id; tag = 'servers5a3:game-' + args.run_id
        run('docker', 'tag', current['Image'], base)
        image_dir = s / 'image'; image_dir.mkdir()
        shutil.copy2(s / 'DNF90Server', image_dir / 'DNF90Server'); (image_dir / 'DNF90Server').chmod(0o755)
        assert args.image_base and re.fullmatch(r'sha256:[0-9a-f]{64}', args.image_base), 'pinned runtime image base is required'
        (image_dir / 'Dockerfile').write_text('FROM ' + args.image_base + '\nCOPY --chown=10001:10001 DNF90Server /usr/local/bin/DNF90Server\nLABEL org.opencontainers.image.revision="' + src['sourceCommit'] + '"\n')
        subprocess.run(['docker', 'build', '--network=none', '--pull=false', '-t', tag, str(image_dir)], check=True)
        image = json.loads(run('docker', 'image', 'inspect', tag))[0]
        base_config = json.loads(run('docker', 'image', 'inspect', current['Image']))[0]['Config']
        for key in ['User', 'Entrypoint', 'Cmd', 'Env', 'WorkingDir', 'ExposedPorts', 'Healthcheck']:
            assert image['Config'].get(key) == base_config.get(key), key
        check = run('docker', 'run', '--rm', '--network=none', '--entrypoint', 'sha256sum', tag, '/usr/local/bin/DNF90Server').split()[0]
        assert check == src['serverSha256']
        if runtime_channel is not None:
            prior_catalog = read(channel_metadata_path())
            assert prior_catalog['channels'] == len(prior_catalog['ports']) == 119, 'live channel metadata is not the approved 119-row base'
            base_raw = channel_path().read_bytes()
            candidate_raw = (s / RUNTIME_CHANNEL_FILE).read_bytes()
            checked_empire_area_map_delta(base_raw, candidate_raw)
            base_entries = channel_rows(base_raw)
            candidate_entries = channel_rows(candidate_raw)
            assert len(base_entries) == len(candidate_entries) == 119, 'runtime channel row count differs'
            assert len({row[0] for row in base_entries}) == len({row[0] for row in candidate_entries}) == 119, 'runtime channel IDs are not unique'
            assert candidate_entries == base_entries, 'runtime channel update changed a channel row'
            assert candidate_entries[-1] == (187, '帝国竞技场', 39, 'tagtournament'), 'runtime channel tail row differs'
            (s / RUNTIME_CHANNEL_FILE).chmod(0o644)
            assert current['Config']['User'], 'runtime image user is missing'
            report = run('docker', 'run', '--rm', '--network', 'none', '--user', current['Config']['User'],
                         '--mount', 'type=bind,source=' + str(d / 'data/Script.pvf') + ',target=/assets/Script.pvf,readonly',
                         '--mount', 'type=bind,source=' + str(s / RUNTIME_CHANNEL_FILE) + ',target=/candidate/channel.override.etc,readonly',
                         '--tmpfs', '/out:rw,size=1m,mode=1777',
                         '--entrypoint', 'DNF90Assets', tag, '-pvf', '/assets/Script.pvf',
                         '-channel', '/candidate/channel.override.etc', '-output', '/out/channel.override.etc')
            channel_catalog = json.loads(report)
            assert channel_catalog['source'] == 'external:' + RUNTIME_CHANNEL_FILE
            assert channel_catalog['sha256'] == runtime_channel['sha256']
            assert channel_catalog['channels'] == 119
            assert channel_catalog['ports'] == [10000 + row[0] for row in candidate_entries]
            assert channel_catalog['ports'] == prior_catalog['ports']
            assert runtime_channel['port'] in channel_catalog['ports']
            channel_catalog['pvf_sha256'] = src['pvfSha256']
            write(s / 'channel-catalog.json', channel_catalog)
        prepared = {'image': image['Id'], 'tag': tag, 'beforeImage': current['Image'], 'beforeTag': base, 'serverSha256': check, 'runtimeBaseImage': args.image_base, 'sourceDirectory': str(tree), 'sourceFiles': len(src['files'])}
        if runtime_channel is not None:
            prepared['runtimeChannel'] = {**runtime_channel, 'catalogSha256': sha(s / 'channel-catalog.json')}
        write(s / 'prepared-game.json', prepared)
        print('PASS image and immutable source prepared; running game unchanged', flush=True)
    elif mode == 'generate':
        validate_before(); prepared = read(s / 'prepared-game.json')
        assert json.loads(run('docker', 'image', 'inspect', prepared['tag']))[0]['Id'] == prepared['image']
        prior = read(d / 'compose.json'); instance = read(d / 'instance.json')
        updated = target_instance(instance, prepared['tag'], config_mode, config_multiplier, config_percent,
                                  runtime_channel['channel_source'] if runtime_channel is not None else None)
        prior_catalog = read(channel_metadata_path())
        generated_catalog = read(s / 'channel-catalog.json') if runtime_channel is not None else prior_catalog
        spec = importlib.util.spec_from_file_location('manage', r / 'deploy/docker/manage.py')
        manage = importlib.util.module_from_spec(spec); spec.loader.exec_module(manage)
        uid, gid = map(int, prior['services']['server']['user'].split(':'))
        os.getuid = lambda: uid; os.getgid = lambda: gid
        manage.RUNTIME = s / 'generated'; manage.generate(updated, generated_catalog)
        expected = json.loads(json.dumps(prior)); expected['services']['server']['image'] = prepared['tag']
        if runtime_channel is not None:
            expected['services']['server']['ports'] = projected_server_ports(updated, generated_catalog, manage)
            assert generated_catalog['ports'] == prior_catalog['ports']
            assert expected['services']['server']['ports'] == prior['services']['server']['ports'], 'Imperial mapping changed published ports'
            write(s / 'generated-channel-metadata.json', generated_catalog)
        assert read(s / 'generated/compose.json') == expected, 'compose differs beyond image'
        for rel in ['server.env', 'database.env'] + (['gm/config.json'] if 'gm' in container_names else []):
            if rel.endswith('.json'): assert read(s / 'generated' / rel) == read(d / rel)
            elif rel == 'server.env': assert (s / 'generated' / rel).read_bytes() == target_env_bytes((d / rel).read_bytes(), config_mode, config_multiplier, config_percent), 'unapproved server environment change'
            else: assert (s / 'generated' / rel).read_bytes() == (d / rel).read_bytes()
        write(s / 'generated/instance.json', updated)
        changed_keys = sorted(k for k in updated.keys() | instance.keys() if updated.get(k) != instance.get(k))
        changed_compose_keys = ['services.server.image']
        write(s / 'config-generation.json', {'changedInstanceKeys': changed_keys, 'changedComposeKeys': changed_compose_keys, 'environmentPreserved': not config_changed, 'tournamentMode': config_mode, 'mainQuestExpMultiplier': config_multiplier, 'mainQuestExpPercent': config_percent, 'deploymentUIDPreserved': True, 'runtimeChannel': runtime_channel is not None})
        print('PASS configuration generated; only image and explicit tournament/quest experience settings allowed', flush=True)
    elif mode == 'deploy':
        assert not (s / 'game-deployment.json').exists()
        validate_before(); prepared = read(s / 'prepared-game.json')
        compatibility = read(s / 'compatibility-readback.json')
        assert compatibility['signatureVerified'] and compatibility['sequence'] == old['sequence']
        assert compatibility['exeSha256'] == old['exeSha256'] and compatibility['pvfSha256'] == old['pvfSha256']
        before = {name: inspect(name) for name in container_names}
        instance = read(d / 'instance.json')
        req = urllib.request.Request('http://' + instance['server_ip'] + ':17002/api/player/admin', data=b'{"action":"sessions"}', headers={'Content-Type': 'application/json', 'Authorization': 'Bearer ' + instance['player_admin_secret']})
        sessions = json.load(urllib.request.urlopen(req, timeout=10)); assert sessions['ok']
        counts = {'leases': len(sessions['sessions']), 'connections': sum(x.get('connections', 0) for x in sessions['sessions'])}
        write(s / 'sessions-before-deploy.json', counts); print('Current session counts:', json.dumps(counts), flush=True)
        backup = r / 'runtime/backups' / ('game-update-' + datetime.datetime.now(datetime.timezone.utc).strftime('%Y%m%dT%H%M%SZ'))
        backup.mkdir()
        # In split topology the Hub lives on the edge host and is not mutated here.
        # Keep its verified snapshot; never require a fictitious local Hub state file.
        backup_extra = [r / runtime_channel['target']] if runtime_channel is not None else []
        backup_game_context(r, h, backup, old['configHashes'], args.edge_evidence, backup_extra)
        started = time.time()
        backup_output = run('python3', str(r / 'deploy/remote/backup.py'), '--runtime-dir', str(r / 'runtime'), '--container', args.project + '-db-1', '--no-retention')
        match = re.fullmatch(r'Database backup: (database-\d{8}T\d{6}Z\.sql\.gz)', backup_output)
        assert match, 'database backup did not return its exact recovery point'
        dump = r / 'runtime/backups' / match[1]
        assert dump.stat().st_mtime >= started and dump.stat().st_size > 0
        with gzip.open(dump, 'rb') as stream:
            assert b'MariaDB' in stream.read(4096)
            while stream.read(1024 * 1024): pass
        run('docker', 'image', 'save', '-o', str(backup / 'game-before.tar'), prepared['beforeTag'])
        backup_receipt = {'backup': str(backup), 'databaseBackup': str(dump), 'databaseBackupSha256': sha(dump), 'beforeImage': prepared['beforeImage'], 'beforeTag': prepared['beforeTag'], 'beforeImageArchiveSha256': sha(backup / 'game-before.tar'), 'beforeChannelSha256': old['channelSha256'], 'beforeChannelCatalogSha256': sha(channel_metadata_path()), 'rollbackPreservesPlayerDatabase': True}
        write(s / 'backup-receipt.json', backup_receipt)
        project = before['server']['Config']['Labels']['com.docker.compose.project']
        compose = ['docker', 'compose', '--project-directory', str(d), '-p', project, '-f', str(d / 'compose.json')]
        def restart(): subprocess.run(compose + ['up', '-d', '--no-deps', '--force-recreate', '--no-build', '--pull', 'never', 'server'], check=True)
        def healthy():
            for attempt in range(100):
                after = {name: inspect(name) for name in container_names}
                if all(c['State'].get('Health', {}).get('Status') == 'healthy' for c in after.values()): return after
                if after['server']['State'].get('OOMKilled') or after['server']['State']['Status'] == 'exited': raise RuntimeError('game startup failed')
                if attempt % 10 == 0: print('Waiting for game health:', attempt * 2, 'seconds', flush=True)
                time.sleep(2)
            raise RuntimeError('game health timeout')
        rollback = backup / 'rollback-game.py'
        rollback.write_text('from pathlib import Path\nimport shutil,subprocess,fcntl,os,json,hashlib\nr=Path(' + repr(str(r)) + ');b=Path(' + repr(str(backup)) + ')\nlock=(r/"runtime/guild-deployment.lock").open("a");fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB)\na=json.loads((r/"runtime/active-game-baseline.json").read_text());assert a["sourceCommit"] in ' + repr([src['sourceCommit'], old['active']['sourceCommit']]) + ',"another deployment is active"\nassert hashlib.sha256((b/"game-before.tar").read_bytes()).hexdigest()==' + repr(backup_receipt['beforeImageArchiveSha256']) + '\nsubprocess.run(["docker","image","load","-i",str(b/"game-before.tar")],check=True)\nsubprocess.run(["docker","tag",' + repr(prepared['beforeImage']) + ',' + repr(read(d / 'compose.json')['services']['server']['image']) + '],check=True)\nfor rel in ' + repr(rollback_configs) + ':\n p=r/rel;st=p.stat();t=p.with_name(p.name+".rollback-new");assert not t.exists();shutil.copy2(b/rel,t);os.chmod(t,st.st_mode&0o777);os.chown(t,st.st_uid,st.st_gid);t.replace(p)\nsubprocess.run(' + repr(compose + ['up', '-d', '--no-deps', '--force-recreate', '--no-build', '--pull', 'never', 'server']) + ',check=True)\nprint("Game image/config restored; player database preserved. Verify process hash and health.")\n')
        validate_before()
        prior = read(d / 'compose.json'); new = read(s / 'generated/compose.json')
        new_instance = read(s / 'generated/instance.json')
        expected = json.loads(json.dumps(prior)); expected['services']['server']['image'] = prepared['tag']
        prior_catalog = read(channel_metadata_path())
        channel_catalog = read(s / 'channel-catalog.json') if runtime_channel is not None else prior_catalog
        if runtime_channel is not None:
            assert len(prior_catalog['ports']) == 119 and channel_catalog['ports'] == prior_catalog['ports']
            assert runtime_channel['port'] in channel_catalog['ports']
            spec = importlib.util.spec_from_file_location('manage', r / 'deploy/docker/manage.py')
            manage = importlib.util.module_from_spec(spec); spec.loader.exec_module(manage)
            expected['services']['server']['ports'] = projected_server_ports(new_instance, channel_catalog, manage)
            assert sha(s / 'generated-channel-metadata.json') == prepared['runtimeChannel']['catalogSha256'], 'generated channel metadata changed'
        assert new == expected
        assert json.loads(run('docker', 'image', 'inspect', prepared['tag']))[0]['Id'] == prepared['image'], 'prepared image tag moved'
        assert new_instance == target_instance(instance, prepared['tag'], config_mode, config_multiplier, config_percent,
                                                runtime_channel['channel_source'] if runtime_channel is not None else None), 'unapproved instance change'
        assert (s / 'generated/server.env').read_bytes() == target_env_bytes((d / 'server.env').read_bytes(), config_mode, config_multiplier, config_percent)
        print('Verified database/config/image backups; switching game service only', flush=True)
        try:
            if runtime_channel is not None:
                atomic(channel_path(), (s / RUNTIME_CHANNEL_FILE).read_bytes())
                atomic(channel_metadata_path(), (s / 'generated-channel-metadata.json').read_bytes())
            atomic(d / 'instance.json', (s / 'generated/instance.json').read_bytes())
            atomic(d / 'compose.json', (s / 'generated/compose.json').read_bytes())
            if config_changed:
                atomic(d / 'server.env', (s / 'generated/server.env').read_bytes())
            restart(); after = healthy(); actual = binary()
            assert actual['sha256'] == src['serverSha256'] and after['server']['Image'] == prepared['image']
            assert after['server']['RestartCount'] == 0
            assert dict(x.split('=', 1) for x in after['server']['Config']['Env']) == target_environment(dict(x.split('=', 1) for x in before['server']['Config']['Env']), config_mode, config_multiplier, config_percent)
            process_env = dict(line.decode().split('=', 1) for line in (Path('/proc') / str(actual['pid']) / 'environ').read_bytes().split(b'\0') if b'=' in line)
            tournament_state = tournament_snapshot(new_instance, dict(x.split('=', 1) for x in after['server']['Config']['Env']), process_env)
            assert after['server']['Config']['User'] == before['server']['Config']['User']
            for name in [n for n in container_names if n != 'server']:
                assert after[name]['Id'] == before[name]['Id'] and after[name]['State']['StartedAt'] == before[name]['State']['StartedAt']
            for name, path in [('server', '/runtime/data/dnf/Script.pvf')] + ([('gm', '/assets/Script.pvf')] if 'gm' in container_names else []):
                assert run('docker', 'exec', args.project + '-' + name + '-1', 'sha256sum', path).split()[0] == old['pvfSha256']
            expected_channel = runtime_channel['sha256'] if runtime_channel is not None else old['channelSha256']
            assert sha(channel_path()) == expected_channel
            assert run('docker', 'exec', args.project + '-server-1', 'sha256sum', channel_container_path()).split()[0] == expected_channel, 'container channel file differs after deploy'
            updated_server_env = dict(x.split('=', 1) for x in after['server']['Config']['Env'])
            expected_channel_env = channel_info_env_path(channel_file_name())
            assert updated_server_env.get('DNFBRIDGE_CHANNEL_INFO_PATH') == expected_channel_env
            assert process_env.get('DNFBRIDGE_CHANNEL_INFO_PATH') == expected_channel_env
            if runtime_channel is not None:
                assert sha(channel_metadata_path()) == sha(s / 'generated-channel-metadata.json')
                published_ports = after['server']['NetworkSettings']['Ports']
                assert f"{runtime_channel['port']}/tcp" in published_ports
                runtime_endpoint = checked_runtime_channel_host(new_instance, channel_catalog, manage, published_ports, runtime_channel['port'])
                assert all(f'{port}/tcp' in published_ports for port in prior_catalog['ports']), 'an original channel port disappeared'
                with socket.create_connection((runtime_endpoint['bind'], runtime_channel['port']), timeout=3):
                    pass
            assert management_snapshot()[0] == old.get('management', {'mode': 'container'})
            assert digest_json(publication()) == old['publishedSha256']
            for rel, digest in old['configHashes'].items():
                changed_configs = list(dict.fromkeys(['runtime/docker/instance.json', 'runtime/docker/compose.json'] + (['runtime/docker/server.env'] if config_changed else []) + (rollback_extra if runtime_channel is not None else [])))
                if rel not in changed_configs: assert sha(r / rel) == digest
            assert (d / 'server.env').read_bytes() == target_env_bytes((backup / 'runtime/docker/server.env').read_bytes(), config_mode, config_multiplier, config_percent)
            receipt = {'sourceCommit': src['sourceCommit'], 'baseCommit': src['baseCommit'], 'upstream': src['upstream'], 'publicGuildCommit': src['publicGuildCommit'], 'publicFixCommits': src['publicFixCommits'], 'gameExe': actual, 'image': prepared['image'], 'sourceArchiveSha256': src['sourceArchiveSha256'], 'sourceDirectory': prepared['sourceDirectory'], 'sourceFiles': prepared['sourceFiles'], 'liveWorkingSourcePreserved': True, 'sequence': old['sequence'], 'exeSha256': old['exeSha256'], 'pvfSha256': old['pvfSha256'], 'channelSha256': expected_channel, 'gameHealthy': True, 'gmHealthy': True, 'gmContainerUnchanged': True, 'databaseContainerUnchanged': True, 'serverEnvironmentPreserved': True, 'deployedAt': datetime.datetime.now(datetime.timezone.utc).isoformat(), 'nativeAcceptance': 'pending', 'runtimeBaseImage': prepared['runtimeBaseImage'], **backup_receipt}
            receipt['gmMode'] = management_snapshot()[0]['mode']
            receipt['channelFile'] = new_instance.get('channel_file')
            receipt['channelSource'] = new_instance.get('channel_source')
            receipt['tournament'] = tournament_state
            receipt['mainQuestExperience'] = main_quest_exp_snapshot(new_instance, dict(x.split('=', 1) for x in after['server']['Config']['Env']), process_env)
            receipt['serverEnvironmentPreserved'] = not config_changed
            receipt['onlyApprovedEnvironmentChange'] = config_changed
            if runtime_channel is not None:
                receipt['runtimeChannel'] = runtime_channel
                receipt['runtimeChannelPortCheck'] = {'port': runtime_channel['port'], 'connected': True}
            receipt['gmContainerUnchanged'] = 'gm' in container_names
            receipt['sourceGitBundleSha256'] = src['sourceGitBundleSha256']
            if 'sourceRecovery' in src: receipt['sourceRecovery'] = src['sourceRecovery']
            receipt['authoritativePull'] = read(s / 'authority-deploy.json')
            if src.get('integrationMode') == 'emergency-selected-mr':
                receipt['integrationMode'] = src['integrationMode']
                receipt['observedUpstream'] = src['observedUpstream']
                receipt['emergency'] = src['emergency']
            atomic(r / 'runtime/active-game-baseline.json', json.dumps(receipt, indent=2).encode())
            write(s / 'game-deployment.json', receipt)
        except BaseException:
            restore_game_configs(r, backup, atomic, rollback_extra)
            restart(); healthy(); assert binary()['sha256'] == old['serverSha256']
            write(s / 'game-rollback.json', {'backup': str(backup), 'databasePreserved': True, 'oldProcessVerified': True})
            raise
        print('DEPLOYED: process SHA, image, health, PVF/channel and unchanged GM/database verified', flush=True)
    elif mode == 'rollback':
        receipt = read(s / 'game-deployment.json')
        assert read(r / 'runtime/active-game-baseline.json') == receipt, 'another deployment is active'
        assert management_snapshot()[0] == old.get('management', {'mode': 'container'}), 'GM deployment changed; restore its topology before this game rollback'
        backup = Path(receipt['backup'])
        assert backup.resolve().parent == (r / 'runtime/backups').resolve()
        before = inspect('server')
        project = before['Config']['Labels']['com.docker.compose.project']
        assert sha(backup / 'game-before.tar') == receipt['beforeImageArchiveSha256']
        for rel in rollback_configs:
            expected = old['channelSha256'] if runtime_channel is not None and rel == runtime_channel['target'] else old['configHashes'][rel]
            assert sha(backup / rel) == expected, 'rollback config changed'
        run('docker', 'image', 'load', '-i', str(backup / 'game-before.tar'))
        run('docker', 'tag', receipt['beforeImage'], read(backup / 'runtime/docker/compose.json')['services']['server']['image'])
        restore_game_configs(r, backup, atomic, rollback_extra)
        subprocess.run(['docker', 'compose', '--project-directory', str(d), '-p', project, '-f', str(d / 'compose.json'), 'up', '-d', '--no-deps', '--force-recreate', '--no-build', '--pull', 'never', 'server'], check=True)
        for attempt in range(100):
            if inspect('server')['State'].get('Health', {}).get('Status') == 'healthy': break
            if attempt % 10 == 0: print('Waiting for rollback health', attempt * 2, flush=True)
            time.sleep(2)
        else: raise RuntimeError('rollback health timeout')
        assert binary()['sha256'] == old['serverSha256']
        assert sha(channel_path()) == old['channelSha256']
        assert run('docker', 'exec', args.project + '-server-1', 'sha256sum', channel_container_path()).split()[0] == old['channelSha256']
        write(s / 'game-rollback.json', {'beforeCommit': old['active']['sourceCommit'], 'beforeChannelSha256': old['channelSha256'], 'processVerified': True, 'databasePreserved': True})
        print('ROLLED BACK: old process restored; player database preserved')
    else:
        raise ValueError(mode)


if __name__ == "__main__": main()
