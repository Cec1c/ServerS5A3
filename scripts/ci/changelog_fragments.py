"""Validate independent change notes, render snapshots and resolve legacy additions.

Standard library only. Rendering never commits, publishes or deploys anything.
The legacy resolver only stages changlog.md after an exact three-way check.
"""
import argparse
from dataclasses import dataclass
from datetime import datetime
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys

PUBLIC = 'changelog.d'
PRIVATE = '.pos5a3/changelog.d'
TYPES = ('fix', 'feature', 'change', 'docs')
NAME = re.compile(r'(\d{8}-[a-z0-9]+(?:-[a-z0-9]+)*)\.(fix|feature|change|docs)\.md\Z')
MARKERS = re.compile(r'(?m)^(?:<{7}|={7}|>{7}|\|{7})(?: |$)')
MAX_NOTE = 16 * 1024


class NoteError(ValueError):
    pass


def require(ok, message):
    if not ok:
        raise NoteError(message)


def git(root, *args, check=True):
    p = subprocess.run(['git', '-c', 'core.hooksPath=/dev/null', *args], cwd=root,
                       stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    if check and p.returncode:
        raise NoteError('git ' + args[0] + ' failed; verify the requested refs and checkout')
    return p


def commit(root, ref):
    require(ref and not ref.startswith('-'), 'invalid Git ref')
    return git(root, 'rev-parse', '--verify', ref + '^{commit}').stdout.decode().strip()


def roots(scope):
    return [PUBLIC] if scope == 'public' else [PUBLIC, PRIVATE]


@dataclass(frozen=True)
class Note:
    path: str
    id: str
    kind: str
    category: str
    title: str
    body: str
    sha256: str


def parse_note(path, raw, *, legacy_sections=False):
    parent, name = path.rsplit('/', 1)
    match = NAME.fullmatch(name)
    require(parent in (PUBLIC, PRIVATE) and match, 'invalid fragment path: ' + path)
    try:
        datetime.strptime(match[1][:8], '%Y%m%d')
        text = raw.decode('utf-8')
    except (ValueError, UnicodeError):
        raise NoteError('invalid date or UTF-8: ' + path) from None
    require(0 < len(raw) <= MAX_NOTE, 'fragment exceeds size limit: ' + path)
    if legacy_sections:
        # Render an inherited CRLF note without rewriting its immutable blob.
        # New notes still require LF, and lone CR bytes remain invalid.
        text = text.replace('\r\n', '\n')
    require('\r' not in text and '\x00' not in text and text.endswith('\n'), 'use UTF-8/LF with final newline: ' + path)
    require(not MARKERS.search(text), 'conflict marker in ' + path)
    category = '功能修复' if parent == PUBLIC else '群服内容'
    frontmatters = ['---\ncategory: ' + category + '\n---\n\n']
    if legacy_sections and parent == PUBLIC:
        # Already merged public notes occasionally wrote the shorthand "修复"
        # instead of the full category label. Never accept it under PRIVATE or
        # accept the opposite scope category.
        frontmatters.append('---\ncategory: 修复\n---\n\n')
    matched_fm = next((fm for fm in frontmatters if text.startswith(fm)), None)
    if legacy_sections and not text.startswith('---\n'):
        # Some already merged notes are plain prose. Preserve them verbatim;
        # category and identity still come from the public/private path.
        require(text.strip(), 'empty historical fragment: ' + path)
        prose = text.lstrip('\n').rstrip()
        return Note(path, match[1], match[2], category, match[1], prose, hashlib.sha256(raw).hexdigest())
    require(matched_fm is not None, 'wrong category or fragment header: ' + path)
    rest = text[len(matched_fm):]
    if rest.startswith('# '):
        heading_rest = rest[2:]
    elif legacy_sections and rest.startswith('## '):
        # Already merged fragments used a level-two title. Select that parser
        # prefix only in inherited mode; keep the original blob/hash intact.
        heading_rest = rest[3:]
    elif legacy_sections:
        # Already merged notes with matching frontmatter followed directly by
        # prose (without a Markdown heading) remain immutable; keep their blob,
        # indentation, and hash intact while still requiring a matching category
        # and non-empty body.
        require(rest.strip(), 'empty historical fragment: ' + path)
        prose = rest.lstrip('\n').rstrip()
        require(not re.search(r'(?m)^# ', prose), 'multiple fragment titles: ' + path)
        return Note(path, match[1], match[2], category, match[1], prose, hashlib.sha256(raw).hexdigest())
    else:
        raise NoteError('wrong category or fragment header: ' + path)
    title, sep, body = heading_rest.partition('\n\n')
    require(sep and title.strip() == title and 1 <= len(title) <= 100 and '\n' not in title,
            'one short title is required: ' + path)
    sections = body.startswith('## 变更\n\n') and body.count('\n## 验证\n\n') == 1
    require(sections or legacy_sections, '变更 and 验证 sections are required: ' + path)
    if sections:
        change, validation = body[len('## 变更\n\n'):].split('\n## 验证\n\n')
        require(change.strip() and validation.strip(), 'empty change or validation: ' + path)
    else:
        require(body.strip(), 'empty historical fragment: ' + path)
    require(not re.search(r'(?m)^# ', body), 'multiple fragment titles: ' + path)
    return Note(path, match[1], match[2], category, title, body.strip(), hashlib.sha256(raw).hexdigest())


def note_blobs(root, scope, ref=None):
    found = {}
    if ref:
        for row in git(root, 'ls-tree', '-rz', '--full-tree', ref, '--', *roots(scope)).stdout.split(b'\0'):
            if not row:
                continue
            metadata, path = row.split(b'\t', 1)
            mode, kind, oid = metadata.decode().split()
            path = path.decode('utf-8')
            if path in [r + '/README.md' for r in roots(scope)]:
                continue
            require(mode == '100644' and kind == 'blob', 'fragment must be a regular text file: ' + path)
            found[path] = git(root, 'cat-file', 'blob', oid).stdout
    else:
        for directory in roots(scope):
            base = Path(root, directory)
            require(not base.is_symlink(), 'fragment directory must not be a symlink')
            if not base.exists():
                continue
            for p in sorted(base.iterdir()):
                if p.name == 'README.md':
                    continue
                require(p.is_file() and not p.is_symlink(), 'fragment must be a regular file: ' + p.name)
                found[directory + '/' + p.name] = p.read_bytes()
    return found


def load_notes(blobs):
    notes = [parse_note(path, raw) for path, raw in sorted(blobs.items())]
    ids = [n.id for n in notes]
    require(len(ids) == len(set(ids)), 'duplicate fragment ID, including across public/private or types')
    return notes


def changed_paths(root, base):
    tracked = git(root, 'diff', '--name-only', '-z', '--no-renames', base, '--').stdout
    untracked = git(root, 'ls-files', '--others', '--exclude-standard', '-z').stdout
    return {p.decode('utf-8') for p in (tracked + untracked).split(b'\0') if p}


def inherited_notes(root, scope, ref='HEAD'):
    """无基线时，把目标提交父提交里已有的精确片段内容视为已合并。

    第一父提交已有路径优先固定其主线版本，其余父提交只补充新引入的历史片段，
    防止合并分支重写既有历史片段被误认为合法 legacy；且必须字节完全一致。
    """
    p = git(root, 'rev-parse', commit(root, ref) + '^@', check=False)
    if p.returncode:
        return {}
    found = {}
    for parent_ref in p.stdout.decode().split():
        for path, raw in note_blobs(root, scope, parent_ref).items():
            found.setdefault(path, raw)
    return found


def load_notes_tolerant(blobs, inherited):
    """仅对与继承基线字节完全一致的片段允许历史格式兼容，其余强制当前严格格式。

    已合并片段的不可变性由显式 --base / --since 检查保证；在未提供基线的分支本地校验或
    渲染中，若片段与父提交字节完全一致则允许走 legacy 解析，若在 PR 合并前被作者修订则须
    满足当前严格格式，既不阻断合并前修订，也防止就地篡改历史 legacy 片段绕过校验。
    """
    notes, legacy = [], []
    for path, raw in sorted(blobs.items()):
        if inherited.get(path) == raw:
            try:
                notes.append(parse_note(path, raw))
            except NoteError:
                # Keep legacy prose in rendering; still check file identity,
                # encoding, category when declared, and duplicate IDs.
                notes.append(parse_note(path, raw, legacy_sections=True))
                legacy.append(path)
            continue
        notes.append(parse_note(path, raw))
    ids = [n.id for n in notes]
    require(len(ids) == len(set(ids)), 'duplicate fragment ID, including across public/private or types')
    return notes, legacy


def check(root, scope, base=None):
    current = note_blobs(root, scope)
    if base:
        base = commit(root, base)
        previous = note_blobs(root, scope, base)
        for path, raw in previous.items():
            current_raw = current.get(path)
            if (current_raw != raw and current_raw is not None and
                    current_raw.replace(b'\r\n', b'\n') == raw and
                    git(root, 'diff', '--quiet', '--no-ext-diff', base, '--', path, check=False).returncode == 0):
                # Git's text checkout may materialize CRLF while the exact
                # committed note remains LF. Accept only that byte transform
                # plus Git's own unchanged-path proof; validate the base blob.
                current[path] = raw
            require(current.get(path) == raw, 'merged fragments are immutable; add a correction: ' + path)
        paths = changed_paths(root, base)
        if scope == 'public':
            require(not any(p.startswith('.pos5a3/') for p in paths), 'private content cannot enter a public contribution')
        enabled = git(root, 'cat-file', '-e', base + ':changelog.d/README.md', check=False).returncode == 0
        if enabled:
            require('changlog.md' not in paths, 'changlog.md is historical; add an independent fragment')
        added = set(current) - set(previous)
        if scope == 'group':
            added = {p for p in added if p.startswith(PRIVATE + '/')}
        require(not paths or added, 'this change requires a new ' + ('group' if scope == 'group' else scope) + ' fragment')
        inherited = previous
    else:
        inherited = inherited_notes(root, scope)
        head_blobs = None
        for path, raw in list(current.items()):
            if b'\r\n' in raw:
                if head_blobs is None:
                    head_blobs = note_blobs(root, scope, 'HEAD')
                normalized = raw.replace(b'\r\n', b'\n')
                if (head_blobs.get(path) == normalized and
                        git(root, 'diff', '--quiet', '--no-ext-diff', 'HEAD', '--', path, check=False).returncode == 0):
                    current[path] = normalized
    notes, legacy = load_notes_tolerant(current, inherited)
    for path in legacy:
        print('warning: merged fragment predates the current format and cannot be edited in place: ' + path,
              file=sys.stderr)
    return {'scope': scope, 'base': base, 'fragments': len(notes)}



def render(root, scope, since=None, until='HEAD'):
    require(scope in ('public', 'all'), 'render scope must be public or all')
    until = commit(root, until)
    if since:
        since = commit(root, since)
        require(git(root, 'merge-base', '--is-ancestor', since, until, check=False).returncode == 0,
                'since must be an ancestor of until')
    blobs = note_blobs(root, scope, until)
    old = note_blobs(root, scope, since) if since else {}
    for path, raw in old.items():
        require(blobs.get(path) == raw, 'released fragment changed or removed: ' + path)
    inherited = dict(old)
    for path, raw in inherited_notes(root, scope, until).items():
        inherited.setdefault(path, raw)
    notes, legacy = load_notes_tolerant(blobs, inherited)
    selected = sorted((n for n in notes if n.path not in old), key=lambda n: (n.id, n.kind, n.path))
    lines = ['# 变更记录汇总', '', '> 以下为所选源码范围的记录；发布、部署和实机验收以各条验证状态及发布回执为准。', '',
             '范围：`' + (since or '全部历史片段') + '` → `' + until + '`', '']
    manifest = {'schemaVersion': 1, 'scope': scope, 'since': since, 'until': until, 'fragments': []}
    for n in selected:
        lines += ['## ' + n.title, '', '`' + n.id + '` · ' + n.category + ' · ' + n.kind, '',
                  n.body.replace('## 变更\n', '### 变更\n').replace('## 验证\n', '### 验证\n'), '']
        manifest['fragments'].append({'id': n.id, 'path': n.path, 'type': n.kind,
                                      'category': n.category, 'sha256': n.sha256,
                                      'legacyFormat': n.path in legacy})
    if not selected:
        lines += ['此范围没有新增变更记录。', '']
    return '\n'.join(lines), manifest


def write_render(root, destination, markdown, manifest):
    out = Path(destination).resolve()
    root = Path(root).resolve()
    # Generated outputs cannot replace source notes or the historical log.
    forbidden = [root / PUBLIC, root / '.pos5a3', root / '.git']
    require(out != root and not any(out == p or p in out.parents for p in forbidden),
            'choose a generated output directory, not a source directory')
    out.mkdir(parents=True, exist_ok=True)
    for name, body in [('release-notes.md', markdown), ('manifest.json', json.dumps(manifest, ensure_ascii=False, indent=2) + '\n')]:
        path = out / name
        require(not path.exists() and not path.is_symlink(), 'output already exists: ' + name)
    (out / 'release-notes.md').write_text(markdown, encoding='utf-8', newline='\n')
    (out / 'manifest.json').write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + '\n', encoding='utf-8', newline='\n')


def legacy_sections(prefix):
    if not prefix:
        return []
    try:
        text = prefix.decode('utf-8')
    except UnicodeError:
        raise NoteError('legacy addition is not UTF-8') from None
    require(not MARKERS.search(text) and '\r' not in text, 'legacy markers or line endings require review')
    chunks = re.split(r'(?m)(?=^## \d{4}-\d{2}-\d{2}(?: |$))', text)
    require(chunks[0] == '', 'legacy additions must start with a dated section')
    sections = []
    for block in chunks[1:]:
        title, sep, body = block.partition('\n\n')
        require(sep and body.strip() and block.endswith('\n\n'), 'incomplete legacy section')
        try:
            datetime.strptime(title[3:13], '%Y-%m-%d')
        except ValueError:
            raise NoteError('invalid legacy date') from None
        require(not re.search(r'(?m)^## ', body), 'unexpected heading in legacy section')
        sections.append((title, block.encode('utf-8')))
    return sections


def combine_legacy(base, ours, theirs):
    require(base and ours.endswith(base) and theirs.endswith(base),
            'legacy history was edited, deleted or reordered; manual review required')
    try:
        history = base.decode('utf-8')
    except UnicodeError:
        raise NoteError('legacy base is not UTF-8') from None
    require(not MARKERS.search(history), 'legacy base contains conflict markers')
    sections = {}
    old_titles = set(re.findall(rb'(?m)^## [^\n]+', base))
    for title, raw in legacy_sections(ours[:-len(base)]) + legacy_sections(theirs[:-len(base)]):
        require(title.encode() not in old_titles, 'new legacy title duplicates historical entry')
        require(title not in sections or sections[title] == raw, 'same legacy title has different text')
        sections[title] = raw
    require(sections, 'no complete new legacy sections')
    result = b''.join(sections[k] for k in sorted(sections, reverse=True)) + base
    return result, {'entries': len(sections), 'baseSha256': hashlib.sha256(base).hexdigest(),
                    'resultSha256': hashlib.sha256(result).hexdigest()}


def resolve_legacy_merge(root, apply=False):
    require(git(root, 'rev-parse', '--verify', 'MERGE_HEAD', check=False).returncode == 0,
            'resolver requires an in-progress merge')
    paths = git(root, 'diff', '--name-only', '--diff-filter=U', '-z').stdout.split(b'\0')
    require([p for p in paths if p] == [b'changlog.md'], 'only changlog.md may be conflicted')
    stages = git(root, 'ls-files', '-u', '--', 'changlog.md').stdout.decode().splitlines()
    require(len(stages) == 3 and all(line.startswith('100644 ') for line in stages), 'unexpected legacy index modes')
    blobs = [git(root, 'show', ':' + str(i) + ':changlog.md').stdout for i in (1, 2, 3)]
    result, receipt = combine_legacy(*blobs)
    receipt['applied'] = apply
    if apply:
        before = git(root, 'ls-files', '-s', '-z', '--', '.', ':(exclude)changlog.md').stdout
        path = Path(root, 'changlog.md')
        require(not path.is_symlink(), 'legacy log must not be a symlink')
        path.write_bytes(result)
        git(root, 'add', '--', 'changlog.md')
        require(git(root, 'show', ':changlog.md').stdout == result, 'legacy staged readback mismatch')
        require(git(root, 'ls-files', '-s', '-z', '--', '.', ':(exclude)changlog.md').stdout == before,
                'non-log index changed')
    return receipt


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--root', type=Path, default=Path('.'))
    sub = p.add_subparsers(dest='command', required=True)
    for name in ('check', 'ci'):
        s = sub.add_parser(name)
        s.add_argument('--scope', choices=['public', 'group', 'all'], default='public')
        s.add_argument('--base')
        if name == 'ci':
            s.add_argument('--output-dir', type=Path, required=True)
    s = sub.add_parser('render')
    s.add_argument('--scope', choices=['public', 'all'], default='public')
    s.add_argument('--since')
    s.add_argument('--until', default='HEAD')
    s.add_argument('--output-dir', type=Path, required=True)
    s = sub.add_parser('resolve-legacy')
    s.add_argument('--apply', action='store_true')
    a = p.parse_args()
    try:
        if a.command == 'resolve-legacy':
            result = resolve_legacy_merge(a.root, a.apply)
        elif a.command == 'render':
            text, result = render(a.root, a.scope, a.since, a.until)
            write_render(a.root, a.output_dir, text, result)
        else:
            base = a.base
            if a.command == 'ci' and not base:
                base = next((os.environ[k] for k in ('CHANGELOG_BASE', 'CI_MERGE_REQUEST_DIFF_BASE_SHA', 'CI_COMMIT_BEFORE_SHA')
                             if os.environ.get(k) and set(os.environ[k]) != {'0'}), None)
            result = check(a.root, a.scope, base)
            if a.command == 'ci':
                text, manifest = render(a.root, 'public' if a.scope == 'public' else 'all', base)
                write_render(a.root, a.output_dir, text, manifest)
        print(json.dumps(result, ensure_ascii=False, indent=2))
    except NoteError as error:
        p.exit(1, str(error) + '\n')


if __name__ == '__main__':
    main()
