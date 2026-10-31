"""Regression tests use disposable Git repositories, never a developer checkout."""
from pathlib import Path
import subprocess
import tempfile
import unittest

import changelog_fragments as c


def note(title='示例', category='功能修复'):
    return ('---\ncategory: ' + category + '\n---\n\n# ' + title +
            '\n\n## 变更\n\n- 修复具体行为。\n\n## 验证\n\n- 回归通过；未部署。\n').encode()


def legacy(title, body='完整条目'):
    return ('## 2026-10-31 — ' + title + '\n\n- ' + body + '\n\n').encode()


class FormatTests(unittest.TestCase):
    path = 'changelog.d/20261031-example.fix.md'

    def test_valid_categories(self):
        self.assertEqual(c.parse_note(self.path, note()).kind, 'fix')
        p = self.path.replace('changelog.d/', '.pos5a3/changelog.d/')
        self.assertEqual(c.parse_note(p, note(category='群服内容')).category, '群服内容')
        with self.assertRaises(c.NoteError):
            c.parse_note(p, note())

    def test_reject_invalid_format(self):
        samples = [note()[:-1], note().replace(b'\n', b'\r\n'), b'\xff',
                   note() + b'<<<<<<< HEAD\n', note() + b'x' * c.MAX_NOTE,
                   note().replace('## 验证'.encode(), b'## Test'),
                   note().replace('功能修复'.encode(), '群服内容'.encode())]
        for raw in samples:
            with self.subTest(raw=raw[:30]), self.assertRaises(c.NoteError):
                c.parse_note(self.path, raw)

    def test_reject_invalid_path_date_and_duplicate_id(self):
        for path in ['changelog.d/20260230-example.fix.md', 'changelog.d/a.fix.md',
                     'changelog.d/sub/20261031-example.fix.md']:
            with self.subTest(path=path), self.assertRaises(c.NoteError):
                c.parse_note(path, note())
        with self.assertRaises(c.NoteError):
            c.load_notes({self.path: note(), self.path.replace('.fix.', '.docs.'): note()})
        with self.assertRaises(c.NoteError):
            c.load_notes({self.path: note(), '.pos5a3/' + self.path: note(category='群服内容')})

    def test_legacy_preserves_both_and_deduplicates(self):
        base = legacy('历史')
        result, receipt = c.combine_legacy(base, legacy('甲') + base, legacy('乙') + base)
        self.assertTrue(result.endswith(base))
        self.assertEqual(receipt['entries'], 2)
        self.assertEqual(result, c.combine_legacy(base, legacy('乙') + base, legacy('甲') + base)[0])
        self.assertEqual(c.combine_legacy(base, legacy('甲') + base, legacy('甲') + base)[0], legacy('甲') + base)

    def test_legacy_refuses_edits_and_ambiguous_sections(self):
        base = legacy('历史')
        for ours, theirs in [(legacy('甲'), legacy('乙') + base),
                             (legacy('甲') + base, legacy('甲', '不同') + base),
                             (b'not a section\n' + base, base),
                             (legacy('历史') + base, base),
                             (legacy('甲')[:-1] + base, base),
                             (legacy('甲').replace(b'10-31', b'02-30') + base, base)]:
            with self.subTest(ours=ours[:40]), self.assertRaises(c.NoteError):
                c.combine_legacy(base, ours, theirs)
        with self.assertRaises(c.NoteError):
            c.combine_legacy(b'\xff', legacy('甲') + b'\xff', b'\xff')


class GitTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.git('init', '-q', '-b', 'master')
        self.git('config', 'user.name', 'Test')
        self.git('config', 'user.email', 'test@example.invalid')
        self.git('config', 'commit.gpgsign', 'false')
        self.git('config', 'core.autocrlf', 'false')
        self.write('changelog.d/README.md', b'template\n')
        self.write('changlog.md', legacy('历史'))
        self.base = self.commit()

    def git(self, *args, check=True):
        p = c.git(self.root, *args, check=check)
        return p.stdout.decode().strip() if check else p

    def write(self, path, data):
        p = self.root / path
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_bytes(data)

    def add_note(self, slug='first', private=False):
        path = (c.PRIVATE if private else c.PUBLIC) + '/20261031-' + slug + '.fix.md'
        self.write(path, note(slug, '群服内容' if private else '功能修复'))
        return path

    def commit(self):
        self.git('add', '.')
        self.git('commit', '-qm', 'test')
        return self.git('rev-parse', 'HEAD')

    def test_concurrent_independent_fragments_merge(self):
        self.git('checkout', '-qb', 'first')
        self.add_note('first')
        self.commit()
        self.git('checkout', '-qb', 'second', self.base)
        self.add_note('second')
        self.commit()
        self.git('merge', '--no-edit', 'first')
        self.assertEqual(c.check(self.root, 'public', self.base)['fragments'], 2)
        self.assertEqual((self.root / 'changlog.md').read_bytes(), legacy('历史'))

    def test_new_fragment_can_be_edited_until_merged(self):
        p = self.add_note()
        self.commit()
        self.write(p, note('修改尚未合入的片段'))
        c.check(self.root, 'public', self.base)

    def test_merged_fragment_immutable(self):
        p = self.add_note()
        base = self.commit()
        for raw in [note('改旧条目'), None]:
            if raw:
                self.write(p, raw)
            else:
                (self.root / p).unlink()
            with self.assertRaises(c.NoteError):
                c.check(self.root, 'public', base)

    def test_requires_note_and_freezes_legacy(self):
        self.write('code.txt', b'change\n')
        with self.assertRaises(c.NoteError):
            c.check(self.root, 'public', self.base)
        self.add_note()
        c.check(self.root, 'public', self.base)
        self.write('changlog.md', legacy('新增') + legacy('历史'))
        with self.assertRaises(c.NoteError):
            c.check(self.root, 'public', self.base)

    def test_private_contribution_requires_private_note(self):
        self.add_note()
        with self.assertRaises(c.NoteError):
            c.check(self.root, 'group', self.base)
        self.add_note('group-only', private=True)
        c.check(self.root, 'group', self.base)
        with self.assertRaises(c.NoteError):
            c.check(self.root, 'public', self.base)

    def test_render_fixed_tree_range_and_public_exclusion(self):
        p = self.add_note()
        start = self.commit()
        self.add_note('next')
        self.add_note('private-marker', private=True)
        end = self.commit()
        self.write(p, note('工作区修改不进入渲染'))
        text, manifest = c.render(self.root, 'public', start, end)
        self.assertNotIn('private-marker', text)
        self.assertNotIn('工作区修改', text)
        self.assertEqual(len(manifest['fragments']), 1)
        self.assertEqual(manifest['until'], end)
        self.assertEqual((text, manifest), c.render(self.root, 'public', start, end))
        self.assertIn('private-marker', c.render(self.root, 'all', start, end)[0])

    def test_render_rejects_changed_history_and_wrong_ancestry(self):
        p = self.add_note()
        start = self.commit()
        self.write(p, note('edited'))
        end = self.commit()
        with self.assertRaises(c.NoteError):
            c.render(self.root, 'public', start, end)
        with self.assertRaises(c.NoteError):
            c.render(self.root, 'public', end, start)
        with self.assertRaises(c.NoteError):
            c.render(self.root, 'public', 'missing-ref')

    def test_output_cannot_replace_source_or_existing_file(self):
        for destination in [self.root, self.root / c.PUBLIC, self.root / '.pos5a3/output']:
            with self.assertRaises(c.NoteError):
                c.write_render(self.root, destination, 'notes', {})
        out = self.root / 'dist'
        c.write_render(self.root, out, 'notes', {})
        with self.assertRaises(c.NoteError):
            c.write_render(self.root, out, 'changed', {})
        self.assertEqual((out / 'release-notes.md').read_text(), 'notes')

    def test_nested_directory_and_git_symlink_rejected(self):
        self.write('changelog.d/nested/file.md', note())
        with self.assertRaises(c.NoteError):
            c.check(self.root, 'public')
        (self.root / 'changelog.d/nested/file.md').unlink()
        (self.root / 'changelog.d/nested').rmdir()
        p = self.add_note()
        oid = self.git('hash-object', '-w', p)
        self.git('update-index', '--add', '--cacheinfo', '120000,' + oid + ',' + p)
        self.git('commit', '-qm', 'symlink fixture')
        with self.assertRaises(c.NoteError):
            c.render(self.root, 'public')

    def conflict(self, second=False, edit_history=False):
        if second:
            self.write('other.txt', b'base\n')
            self.commit()
        branch_base = self.git('rev-parse', 'HEAD')
        self.git('checkout', '-qb', 'ours')
        self.write('changlog.md', legacy('甲') + legacy('历史'))
        if second:
            self.write('other.txt', b'ours\n')
        self.commit()
        self.git('checkout', '-qb', 'theirs', branch_base)
        self.write('changlog.md', legacy('乙') + legacy('旧历史改动' if edit_history else '历史'))
        self.write('clean.txt', b'clean merge\n')
        if second:
            self.write('other.txt', b'theirs\n')
        self.commit()
        self.git('checkout', 'ours')
        self.assertNotEqual(self.git('merge', '--no-commit', 'theirs', check=False).returncode, 0)

    def test_resolver_dry_run_then_stages_only_log(self):
        self.conflict()
        before = self.git('ls-files', '-s')
        raw = (self.root / 'changlog.md').read_bytes()
        self.assertFalse(c.resolve_legacy_merge(self.root)['applied'])
        self.assertEqual(self.git('ls-files', '-s'), before)
        self.assertEqual((self.root / 'changlog.md').read_bytes(), raw)
        self.assertTrue(c.resolve_legacy_merge(self.root, apply=True)['applied'])
        self.assertEqual(self.git('diff', '--name-only', '--diff-filter=U'), '')
        self.assertEqual(self.git('show', ':clean.txt'), 'clean merge')
        self.assertEqual((self.root / 'changlog.md').read_bytes().count(legacy('历史')), 1)

    def test_resolver_rejects_second_conflict(self):
        self.conflict(second=True)
        with self.assertRaises(c.NoteError):
            c.resolve_legacy_merge(self.root, apply=True)

    def test_resolver_rejects_changed_history(self):
        self.conflict(edit_history=True)
        with self.assertRaises(c.NoteError):
            c.resolve_legacy_merge(self.root, apply=True)

    def test_resolver_rejects_non_merge(self):
        with self.assertRaises(c.NoteError):
            c.resolve_legacy_merge(self.root, apply=True)


if __name__ == '__main__':
    unittest.main()
