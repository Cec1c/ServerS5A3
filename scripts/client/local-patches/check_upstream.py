"""查看上游客户端 EXE 发布，找出 UPSTREAM.md 还没记录的新版本。

上游发布页是 GitGud（https://gitgud.io/16ww/servers5a3/-/releases），但它前面有
Cloudflare 验证，脚本访问不了。上游会把每个客户端 Release 同步到 GitHub
Cec1c/ServerS5A3，并在说明里嵌一段 `client-release-sync:v1` JSON（GitGud 原链接、
zip 与 EXE 的 SHA256、源码提交）。本脚本只读 GitHub 的公开 API。

    # 列出最近的上游客户端版本，标出比 UPSTREAM.md 已记录的最新版本更晚的
    python -B check_upstream.py

    # 下载某个版本，核对 zip 和 EXE 摘要后解出 DNF.exe（输出目录必须是新的）
    python -B check_upstream.py --download client-v211-town-display-20261104 --out 某个新目录

下载后接着跑 `apply_stack.py --input 某个新目录/DNF.exe --check`。
"""
import argparse
import hashlib
import io
import json
import re
import sys
import urllib.request
import zipfile
from pathlib import Path

sys.dont_write_bytecode = True
HERE = Path(__file__).resolve().parent
API = 'https://api.github.com/repos/Cec1c/ServerS5A3/releases?per_page=100'
SYNC = re.compile(r'<!--\s*client-release-sync:v1\s*(\{.*?\})\s*-->', re.S)


def fetch(url):
    request = urllib.request.Request(url, headers={'User-Agent': 'local-patches-check',
                                                   'Accept': 'application/vnd.github+json'})
    with urllib.request.urlopen(request, timeout=120) as response:
        return response.read()


def client_releases():
    rows = []
    for release in json.loads(fetch(API)):
        if not release['tag_name'].startswith('client-'):
            continue
        match = SYNC.search(release.get('body') or '')
        sync = json.loads(match.group(1)) if match else {}
        asset_name, asset = next(iter(sync.get('assets', {}).items()), (None, {}))
        title = (release.get('body') or '').strip().splitlines()[0] if release.get('body') else release['name']
        rows.append(dict(tag=release['tag_name'], published=release['published_at'], title=title,
                         exeSHA256=asset.get('exe', {}).get('sha256'), zipName=asset_name,
                         zipSHA256=asset.get('sha256'), commit=sync.get('commit'),
                         gitgud=sync.get('source'), preview=sync.get('preview'),
                         assets={a['name']: a['browser_download_url'] for a in release['assets']}))
    return sorted(rows, key=lambda r: r['published'], reverse=True)


def recorded_tags():
    text = (HERE / 'UPSTREAM.md').read_text(encoding='utf-8')
    return set(re.findall(r'client-v[0-9A-Za-z.\-]+', text))


def stack_bases():
    stack = json.loads((HERE / 'stack.json').read_text(encoding='utf-8'))
    bases = {}
    for release in stack['releases']:
        bases.setdefault(release['upstreamSHA256'], []).append(release['version'])
    return bases


def list_releases(limit):
    recorded, bases = recorded_tags(), stack_bases()
    every = client_releases()
    # 只把比已记录的最新版本更晚发布的算作“新”，更早的历史版本不跟踪。
    latest = max((r['published'] for r in every if r['tag'] in recorded), default='')
    rows = every[:limit]
    new = [r['tag'] for r in every if r['tag'] not in recorded and r['published'] > latest]
    for r in rows:
        mark = '新' if r['tag'] in new else '  '
        derived = ','.join(bases.get(r['exeSHA256'], [])) or '-'
        print(f"{mark} {r['published'][:16]}  {r['tag']}\n     {r['title']}\n"
              f"     EXE {r['exeSHA256']}  我方派生: {derived}")
    print()
    print(f'UPSTREAM.md 未记录: {", ".join(new)}' if new else 'UPSTREAM.md 已记录全部列出的版本')


def download(tag, out):
    row = next((r for r in client_releases() if r['tag'] == tag), None)
    if row is None or not row['zipName'] or row['zipName'] not in row['assets']:
        raise SystemExit(f'找不到 {tag} 或它没有同步信息')
    out.mkdir(parents=True, exist_ok=False)
    data = fetch(row['assets'][row['zipName']])
    if hashlib.sha256(data).hexdigest() != row['zipSHA256']:
        raise SystemExit('zip 摘要与 Release 同步信息不符')
    with zipfile.ZipFile(io.BytesIO(data)) as archive:
        exe = archive.read('DNF.exe')
        if hashlib.sha256(exe).hexdigest() != row['exeSHA256']:
            raise SystemExit('DNF.exe 摘要与 Release 同步信息不符')
        (out / row['zipName']).write_bytes(data)
        (out / 'DNF.exe').write_bytes(exe)
        for name in ('README.txt', 'manifest.json', 'SHA256SUMS.txt'):
            if name in archive.namelist():
                (out / name).write_bytes(archive.read(name))
    print(json.dumps(dict(tag=tag, exe=str(out / 'DNF.exe'), exeSHA256=row['exeSHA256'],
                          zipSHA256=row['zipSHA256'], commit=row['commit'], gitgud=row['gitgud']),
                     ensure_ascii=False, indent=2))


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument('--limit', type=int, default=8, help='列出最近多少个客户端版本')
    parser.add_argument('--download', metavar='TAG')
    parser.add_argument('--out', type=Path)
    args = parser.parse_args()
    if args.download:
        if not args.out:
            parser.error('--download 需要 --out')
        download(args.download, args.out)
    else:
        list_releases(args.limit)


if __name__ == '__main__':
    main()
