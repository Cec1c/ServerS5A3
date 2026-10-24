"""Read-only real-resource audit and bounded V158 chapter-gate execution.

Reuses the original native harness. Level decode, tree insertion, video/UI,
clock and network are stubs; the real loader, gates and lifecycle hooks run.
"""
import argparse
import json
from pathlib import Path
import re
import struct

import story_digest_chapter_gates as patch
import verify_story_digest_gate as old


def audit_resources(tables, quests, pvf, catalog):
    if patch.sha256(pvf.read_bytes()) != catalog['pvf_sha256']:
        raise ValueError('PVF identity mismatch')
    for path, digest in catalog['tables'].items():
        assert patch.sha256((tables / path).read_bytes()) == digest, path
    index = {int(i): ('n_quest/' + path).replace('\\', '/').lower()
             for i, path in re.findall(r'(\d+)\s+`([^`]+)`', (tables / 'n_quest/quest.lst').read_text(encoding='utf-8-sig'))}
    segments = old.descriptors((tables / 'etc/storydigest/digest.etc').read_text(encoding='utf-8-sig'))
    scene_text = (tables / 'etc/storydigest/scene.etc').read_text(encoding='utf-8-sig')
    scenes = {int(i): body for i, body in re.findall(r'\[scene\]\s*(\d+)\s*(.*?)\[/scene\]', scene_text, re.S)}
    units = {}
    for body in re.findall(r'\[connect quest unit\](.*?)\[/connect quest unit\]',
                           (tables / 'n_quest/connectquestlist.etc').read_text(encoding='utf-8-sig'), re.S):
        key = int(re.search(r'\[parent key\]\s*(\d+)', body).group(1))
        sequence = re.search(r'\[sequence\](.*?)\[/sequence\]', body, re.S).group(1)
        units[key] = list(map(int, re.findall(r'\d+', sequence)))[::2]
    unit_keys = [1, 2, 3, 4, 5, 6, 7, 8, 8, 8, 9, 10, 11, 12, 13, 14, 15]
    for chapter, unit in zip(catalog['chapters'], unit_keys):
        alternatives = set(chapter['any_completed'])
        assert alternatives <= set(units[unit])
        for proof in chapter['proof']:
            assert index[proof['id']] == proof['path']
            raw = (quests / proof['path']).read_bytes()
            assert patch.sha256(raw) == proof['export_sha256'], proof['id']
            text = raw.decode('utf-8-sig')
            assert re.search(r'\[name\]\s*`([^`]+)`', text).group(1) == proof['name']
            prerequisites = [list(map(int, re.findall(r'\d+', group))) for group in
                             re.findall(r'\[pre required quest\](.*?)\[/pre required quest\]', text, re.S)]
            assert prerequisites == proof['pre_required_quest']
            if proof['id'] in chapter['successors']:
                assert prerequisites and all(alternatives.intersection(g) for g in prerequisites)
        if chapter['scene'] == 7:
            successor = next(p for p in chapter['proof'] if p['id'] == 3427)
            assert {g[0] for g in successor['pre_required_quest'] if len(g) == 1} == alternatives
            assert len(alternatives) == 73
        elif chapter['scene'] not in (8, 9):
            assert units[unit][-1] == chapter['any_completed'][0]
    assert set(catalog['endings']) == {str(s['ending']) for s in segments}
    assert scenes[916].strip() == scenes[917].strip()
    assert 'cinematic_11.avi' in scenes[916]
    assert catalog['endings']['916'] == catalog['endings']['917'] == [16, 17]
    return segments, dict(chapters=17, terminal_alternatives=89, quest_proofs=108,
                          tables=len(catalog['tables']), shared_ending_verified=True)


def verify(source, candidate, receipt, segments, catalog):
    assert patch.sha256(source) in patch.SUPPORTED_SOURCES
    assert patch.sha256(source) == receipt['source_sha256']
    assert patch.sha256(candidate) == receipt['candidate_sha256']
    assert patch.sha256(patch.CATALOG.read_bytes()) == receipt['catalog_sha256']
    src = old.Native(source, dict(state=patch.STATE), segments)
    dst = old.Native(candidate, receipt['entries'], segments)
    rows = []
    representatives = {c['scene']: c['any_completed'][0] for c in catalog['chapters']}
    all_completed = sorted({3190, 3215, *representatives.values()})

    def check(label, level, last, completed, want):
        dst.reset(level, last)
        dst.snapshot(tuple(sorted(completed)))
        assert not dst.state['events'] and not dst.state['packets']
        before = bytes(dst.u.mem_read(dst.manager, 0x100))
        result = dst.auto()
        row = dst.capture(label, result)
        assert bool(result) == want, (label, row)
        if not want:
            assert not row['packets'] and row['last'] == last
            assert bytes(dst.u.mem_read(dst.manager, 0x100)) == before
        rows.append(row)
        return row

    # Red/green uses the same actual client version and the same input state.
    for level, last in ((71, 70), (75, 74)):
        src.reset(level, last); src.snapshot((3190, 3215))
        baseline = src.capture('baseline_' + str(level), src.auto())
        assert baseline['return_bool'] and baseline['packets'] == [1443]
        row = check('regression_no_later_quests_' + str(level), level, last, (3190, 3215), False)
        row['original'] = baseline

    for segment in segments:
        chapter = segment['id']
        required = {chapter, *catalog['endings'][str(segment['ending'])]}
        complete = {representatives[i] for i in required}
        check('chapter_%02d_unsubmitted' % chapter, segment['lo'], segment['lo'] - 1, (), False)
        row = check('chapter_%02d_terminal_committed' % chapter, segment['lo'], segment['lo'] - 1, complete, True)
        assert row['segment'] == chapter - 1 and row['first'] == chapter - 1
        # End of the same PVF level range obeys the identical completion gate.
        check('chapter_%02d_range_end' % chapter, min(segment['hi'], 90), segment['lo'] - 1, complete, True)

    # All 73 class/grow alternatives are independently sufficient. None of the
    # other branches or just their common boss predecessor may substitute them.
    check('northmyre_boss_without_branch_end', 50, 49, (3356,), False)
    for quest in catalog['chapters'][6]['any_completed']:
        check('northmyre_alternative_%d' % quest, 50, 49, (quest,), True)

    check('old_boss_only_skycastle', 24, 23, (3190,), False)
    check('old_boss_only_behemoth', 30, 29, (3215,), False)
    check('gent_boss_before_farewell', 71, 70, (3553,), False)
    check('sea_war_ended_before_chain_end', 75, 74, (3603, 3604), False)
    check('shared_video_waits_for_luke_chain', 87, 86, (3883,), False)
    check('shared_video_requires_both_chains', 90, 89, (3945,), False)
    for chapter, quest in representatives.items():
        check('cross_all_missing_%02d' % chapter, 90, 0, set(all_completed) - {quest}, False)
    check('cross_all_complete', 90, 0, all_completed, True)
    check('cross_to_seatrain', 75, 0, {3190, 3215, *(q for i, q in representatives.items() if i <= 12)}, True)
    for level, last in ((24, 24), (71, 74), (75, 75), (90, 90), (30, 90), (16, 0)):
        check('already_seen_or_no_segment_%d_%d' % (level, last), level, last, (), False)

    dst.reset(75, 74)
    assert not dst.auto(); rows.append(dst.capture('missing_snapshot', 0))
    dst.snapshot((3605,)); dst.snapshot(())
    assert not dst.auto(); rows.append(dst.capture('replacement_empty_snapshot', 0))
    dst.snapshot((3605,)); dst.w(dst.context + 0x70, dst.player + 0x400)
    assert not dst.auto(); rows.append(dst.capture('different_actor', 0))
    dst.reset(75, 74); dst.snapshot((3605,)); dst.clear()
    assert dst.rd(patch.STATE) == 0 and dst.rd(patch.STATE + 4) == 0 and not dst.auto()
    rows.append(dst.capture('native_clear_invalidates_all_chapters', 0))
    dst.snapshot((3605,)); assert dst.auto()
    rows.append(dst.capture('new_snapshot_after_clear_reopens', 1))
    dst.state['events'] = []; dst.state['packets'] = []
    assert not dst.auto(); rows.append(dst.capture('second_call_does_not_repeat', 0))
    for kind in ('select', 'return'):
        for success in (False, True):
            src.reset(75, 74); dst.reset(75, 74)
            src.snapshot((3605,)); dst.snapshot((3605,))
            assert src.lifecycle(kind, success) == dst.lifecycle(kind, success)
            result = dst.auto()
            assert bool(result) != success
            rows.append(dst.capture(kind + ('_success_invalidates' if success else '_failure_keeps'), result))
    dst.reset(75, 74, mode_block=1); dst.snapshot((3605,))
    assert not dst.auto(); rows.append(dst.capture('native_mode_block', 0))
    dst.reset(75, 74, actor=0); dst.state['no_actor'] = True; dst.snapshot((3605,))
    assert dst.rd(patch.STATE) == 0 and not dst.auto(); rows.append(dst.capture('no_actor', 0))
    dst.reset(75, 74); dst.snapshot((3605,))
    dst.w(dst.buffer, 74); dst.w(0x57f9ad0, dst.buffer); dst.w(0x57f9ad4, 4); dst.w(0x57f9af8, 0)
    dst.run(0x1455210, 0)
    assert not dst.state['events'] and not dst.state['packets']
    rows.append(dict(case='1377_only_sets_seen_level', passed=True))
    # 1377 cannot grant chapter permission to an empty authoritative snapshot.
    dst.snapshot(()); assert not dst.auto()
    rows.append(dst.capture('1377_does_not_grant_permission', 0))
    dst.reset(75, 74); assert dst.run(old.patch.ORIGINAL, dst.manager, (0,), 4)
    rows.append(dst.capture('original_nonautomatic_entry_preserved', 1))
    for label, entry, end in (('noti1_cleanup', 0x202396a, 0x2023974),
                              ('second_cleanup', 0x691c7c1, 0x691c7cc)):
        dst.reset(75, 74); dst.snapshot((3605,)); dst.clear(entry, end)
        assert dst.rd(patch.STATE) == 0 and not dst.auto()
        rows.append(dst.capture(label, 0))

    # An ending is checked even if all protected scenes have been removed from
    # the newly selected suffix; unknown resource IDs remain unavailable.
    for ending in (911, 916, 917, 999):
        dst.reset(75, 74); dst.snapshot((3605,))
        descriptor = dst.desc + 11 * 0x44
        dst.w(descriptor + 0x2c, ending)
        assert not dst.auto(); rows.append(dst.capture('ending_alone_%d' % ending, 0))
    dst.reset(75, 74); dst.snapshot(all_completed)
    dst.w(dst.desc + 0x2000 + 11 * 0x100 + 11 * 4, 18)
    assert not dst.auto(); rows.append(dst.capture('unknown_scene', 0))

    # Full old-file preservation independently covers all existing author/AT,
    # direction-release, trade and opcode-admission changes, not just samples.
    restored = bytearray(candidate[:len(source)])
    for change in receipt['changes']:
        at, size = change['offset'], change['length']
        assert restored[at:at + size] == bytes.fromhex(change['after'])
        restored[at:at + size] = bytes.fromhex(change['before'])
    assert bytes(restored) == source
    for before, after in zip(src.pe.sections, dst.pe.sections):
        assert before.__pack__() == after.__pack__()
    assert dst.pe.verify_checksum()
    rows.append(dict(case='entire_original_file_preserved_outside_declared_windows', passed=True))
    return dict(status='PASS', count=len(rows), cases=rows,
                source_sha256=patch.sha256(source), candidate_sha256=patch.sha256(candidate),
                executed_addresses=len(dst.visited),
                stubs=['tree insertion/allocation/free', 'encoded level', 'video/UI', 'clock', 'network'],
                not_executed=['full 124 handler', '356 decompression', 'player network', 'video rendering', 'manual UI'])


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ('source', 'candidate', 'receipt', 'tables', 'quests', 'pvf', 'output'):
        parser.add_argument('--' + name, type=Path, required=True)
    args = parser.parse_args()
    catalog = patch.load_catalog()
    segments, audit = audit_resources(args.tables, args.quests, args.pvf, catalog)
    result = verify(args.source.read_bytes(), args.candidate.read_bytes(),
                    json.loads(args.receipt.read_text()), segments, catalog)
    result['resource_audit'] = audit
    args.output.write_text(json.dumps(result, ensure_ascii=False, indent=2), encoding='utf-8')
    print(json.dumps(dict(status=result['status'], cases=result['count'], resources=audit)))


if __name__ == '__main__':
    main()
