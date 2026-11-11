"""Publish an exact reviewed client draft from the current trusted default branch."""
import hashlib
import json
import os
from pathlib import Path
import re
import urllib.error
import urllib.request

import sync_client_releases as sync

REPOSITORY = 'Cec1c/ServerS5A3'
ACTORS = {'Cec1c': 170788854, '16www': 45917572}


def require(condition, message):
    if not condition:
        raise sync.SyncError(message)


def publish(*, stage, repository, head, tag, release_id, zip_sha, exe_sha,
            actor, event, ref, workflow_sha, call, fetch):
    require(repository == REPOSITORY and event == 'workflow_dispatch' and ref == 'refs/heads/master',
            'trusted default-branch manual workflow required')
    require(actor in ACTORS, 'trusted release actor required')
    identity = call('GET', 'users/' + actor)
    require(identity.get('id') == ACTORS[actor], 'release actor identity differs')
    permission = call('GET', f'repos/{repository}/collaborators/{actor}/permission')
    require(permission.get('permission') in ('write', 'maintain', 'admin'), 'repository write permission required')
    require(stage in ('prepare-tag', 'publish') and re.fullmatch(r'[0-9a-f]{40}', head or '')
            and workflow_sha == head, 'workflow/head identity differs')
    require(sync.retained_release_tag(tag), 'retained numeric client tag required')
    prefix = 'repos/' + repository

    def latest():
        require(call('GET', prefix + '/branches/master')['commit']['sha'] == head,
                'default branch moved; inspect frozen publication')

    def tag_ref():
        return call('GET', prefix + '/git/ref/tags/' + tag, missing=True)

    latest()
    target = tag_ref()
    if target:
        require(target.get('object', {}).get('type') == 'commit' and target['object'].get('sha') == head,
                'tag differs; never move an existing tag')
    if stage == 'prepare-tag':
        if not target:
            call('POST', prefix + '/git/refs', {'ref': 'refs/tags/' + tag, 'sha': head})
        actual = tag_ref()
        require(actual and actual['object']['type'] == 'commit' and actual['object']['sha'] == head,
                'prepared tag readback differs')
        return dict(stage=stage, state='prepared-tag', tag=tag, head=head, tagAlreadyPresent=bool(target))

    require(target is not None, 'prepare the exact tag in trusted CI first')
    require(str(release_id).isdigit() and re.fullmatch(r'[0-9a-f]{64}', zip_sha or '')
            and re.fullmatch(r'[0-9a-f]{64}', exe_sha or ''), 'exact release and content identities required')
    row = call('GET', prefix + '/releases/' + str(release_id))
    preview = row.get('prerelease')
    require(row.get('tag_name') == tag and type(preview) is bool,
            'release tag/channel differs')
    record = sync.provenance(row.get('body') or '')
    name = tag + '.zip'
    require(record and record.get('tag') == tag and record.get('commit') == head
            and record.get('preview') is preview and set(record.get('assets', {})) == {name},
            'release provenance differs')
    prior_latest = call('GET', prefix + '/releases/latest')['id'] if preview else None
    require(not preview or prior_latest != int(release_id),
            'experimental release must not be stable Latest')
    require(len(row.get('assets', [])) == 1, 'one exact client ZIP required')
    asset = row['assets'][0]
    expected = record['assets'][name]
    require(asset.get('name') == name and asset.get('state') == 'uploaded'
            and asset.get('digest') == 'sha256:' + zip_sha
            and expected.get('sha256') == zip_sha and expected.get('length') == asset.get('size')
            and expected.get('exe', {}).get('sha256') == exe_sha,
            'release asset metadata differs')
    archive = Path(fetch(asset['id'], zip_sha))
    require(sync.digest(archive) == zip_sha and archive.stat().st_size == asset['size'],
            'downloaded ZIP identity differs')
    exe = sync.inspect_zip(archive)
    require(exe == expected['exe'] and exe['sha256'] == exe_sha, 'downloaded EXE identity differs')
    latest()
    current_tag = tag_ref()
    require(current_tag and current_tag['object']['type'] == 'commit'
            and current_tag['object']['sha'] == head, 'tag moved before publication')
    if row.get('draft'):
        call('PATCH', prefix + '/releases/' + str(release_id),
             {'draft': False, 'make_latest': 'false' if preview else 'true'})
    actual = call('GET', prefix + '/releases/tags/' + tag)
    stable_asset_keys = ('id', 'name', 'size', 'state', 'digest')
    observed_assets = [tuple(a.get(k) for k in stable_asset_keys) for a in actual.get('assets', [])]
    original_assets = [tuple(a.get(k) for k in stable_asset_keys) for a in row['assets']]
    require(actual.get('id') == int(release_id) and actual.get('draft') is False
            and actual.get('prerelease') is preview
            and observed_assets == original_assets
            and sync.provenance(actual.get('body') or '') == record,
            'published release readback differs')
    require(call('GET', prefix + '/releases/latest')['id'] == (prior_latest if preview else int(release_id)),
            'Latest pointer differs')
    return dict(stage=stage, state='published', releaseId=int(release_id), tag=tag, head=head,
                zipSHA256=zip_sha, exe=exe, fullDownloadVerified=True, prerelease=preview,
                stableLatestPreserved=preview,
                alreadyPublished=not row.get('draft'), url=actual['html_url'])


def fetch_draft_asset(repository, token, asset_id, expected, cache):
    """Use auth only at the fixed GitHub API; follow its CDN location anonymously."""
    class NoRedirect(urllib.request.HTTPRedirectHandler):
        def redirect_request(self, *args, **kwargs):
            return None

    req = urllib.request.Request(
        f'https://api.github.com/repos/{repository}/releases/assets/{int(asset_id)}',
        headers={'Authorization': 'Bearer ' + token, 'Accept': 'application/octet-stream',
                 'User-Agent': 'ServerS5A3-trusted-client-publication/1'})
    try:
        response = urllib.request.build_opener(NoRedirect()).open(req, timeout=60)
    except urllib.error.HTTPError as error:
        if error.code != 302:
            raise sync.SyncError('Draft asset API download failed') from None
        location = error.headers.get('Location', '')
        parsed = sync.safe_url(location)
        require(not parsed.username and not parsed.password
                and parsed.hostname in ('release-assets.githubusercontent.com', 'objects.githubusercontent.com')
                and parsed.port in (None, 443), 'unexpected release CDN origin')
        return sync.download(location, cache, expected=expected, fresh=True)
    except (TimeoutError, urllib.error.URLError):
        raise sync.SyncError('Draft asset API transport failed') from None
    with response:
        path = Path(cache) / 'reviewed-client.zip'
        path.parent.mkdir(parents=True, exist_ok=True)
        total = 0
        with path.open('wb') as stream:
            while block := response.read(1024 * 1024):
                total += len(block)
                require(total <= sync.MAX_ASSET, 'draft asset exceeds download limit')
                stream.write(block)
        require(sync.digest(path) == expected, 'draft asset digest differs')
        return path


def main():
    token = os.environ.get('GITHUB_TOKEN', '')
    require(bool(token), 'workflow token required')

    def call(method, path, body=None, missing=False):
        return sync.request('https://api.github.com/' + path, method=method,
                            token=token, provider='github', body=body, missing=missing)

    cache = Path(os.environ['RUNNER_TEMP']) / 'reviewed-client-publication'
    result = publish(stage=os.environ['CLIENT_PUBLICATION_STAGE'], repository=os.environ['GITHUB_REPOSITORY'],
                     head=os.environ['CLIENT_EXPECTED_HEAD'], tag=os.environ['CLIENT_RELEASE_TAG'],
                     release_id=os.environ.get('CLIENT_RELEASE_ID', ''), zip_sha=os.environ.get('CLIENT_ZIP_SHA256', ''),
                     exe_sha=os.environ.get('CLIENT_EXE_SHA256', ''), actor=os.environ['GITHUB_ACTOR'],
                     event=os.environ['GITHUB_EVENT_NAME'], ref=os.environ['GITHUB_REF'],
                     workflow_sha=os.environ['GITHUB_SHA'], call=call,
                     fetch=lambda asset_id, expected: fetch_draft_asset(REPOSITORY, token, asset_id, expected, cache))
    output = Path(os.environ['RUNNER_TEMP']) / 'public-client-publication.json'
    output.write_text(json.dumps(result, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
    print(json.dumps(result, ensure_ascii=False))


if __name__ == '__main__':
    main()
