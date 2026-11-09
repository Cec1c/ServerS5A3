"""Build only the selected eight-dungeon material-drop recipe; no live changes."""
import argparse
import hashlib
import json
from pathlib import Path
import re
import subprocess

BASE_SHA = 'd3507920cdb8038b900ab9897fdc181ff97840c775a06d4c2108b656cc952c56'
TOOL_SHA = '32954604a61342de3304b6412c8df8e038cd1998a4c94829ca5152c7ccef5178'
DUNGEONS = {310: 'metrocenter/time_plaza/time_plaza.dgn',
            311: 'metrocenter/canyon_of_beast/canyon_of_beast.dgn',
            312: 'metrocenter/hideout_of_fear/hideout_of_fear.dgn',
            313: 'metrocenter/gust_zone/gust_zone.dgn',
            314: 'metrocenter/forest_of_redwitch/forest_of_redwitch.dgn',
            315: 'metrocenter/special/special.dgn',
            316: 'metrocenter/hell/hell.dgn',
            7539: 'infernomode/inferno_metrocenter1.dgn'}
SECTION = 'servers5a3 dungeon material drop'


def sha(path):
    with Path(path).open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ('base', 'tool', 'output'):
        parser.add_argument('--'+name, type=Path, required=True)
    args = parser.parse_args()
    if sha(args.base) != BASE_SHA or sha(args.tool) != TOOL_SHA:
        raise ValueError('Unreviewed base/tool; rebase and reconcile cumulative inputs first')
    args.output.mkdir(parents=True, exist_ok=False)

    def run(label, *argv):
        r = subprocess.run([str(args.tool), *map(str, argv)], capture_output=True,
                           timeout=600, creationflags=getattr(subprocess, 'CREATE_NO_WINDOW', 0))
        (args.output/(label+'.stdout')).write_bytes(r.stdout)
        (args.output/(label+'.stderr')).write_bytes(r.stderr)
        if r.returncode:
            raise RuntimeError(label+' failed; preserve its logs')
        return r.stdout

    def read(label, path, fmt='tokens', pvf=None):
        out = args.output/(label+('.json' if fmt == 'tokens' else '.txt'))
        run(label, 'read', '--pvf', pvf or args.base, '--path', path, '--format', fmt, '--out', out)
        text = out.read_text('utf-8-sig')
        return json.loads(text) if fmt == 'tokens' else text

    mappings = {int(i):p.lower() for i,p in re.findall(r'(\d+)\s+`([^`]+)`', read('dungeons','dungeon/dungeon.lst','text'))}
    if any(mappings.get(i) != p for i,p in DUNGEONS.items()):
        raise ValueError('Dungeon identity changed')
    material = read('material','stackable/10163001/10163136.stk','text')
    if not re.search(r'\[name\]\s*`次元光辉`',material) or not re.search(r'\[stackable type\]\s*`\[material\]`',material):
        raise ValueError('Material identity changed')
    path = 'etc/independent_drop.etc'
    drops = read('drop-before',path)
    original = drops['tokens']
    if any(t.get('text','').strip('[]').lower() == SECTION for t in original):
        raise ValueError('Existing material rules require reconciliation')
    if original[-1].get('text','').lower() != '[/independent drop]':
        raise ValueError('Unexpected native drop table ending')
    added = [dict(type=3,text='['+SECTION+']')]
    rows = [[i,10163136,10000,10000,10000,100000] for i in DUNGEONS]
    for row in rows:
        added.extend(dict(type=0,payload_u32=v) for v in row)
    added.append(dict(type=3,text='[/'+SECTION+']'))
    edits = [dict(path=path,data_type=1,before_sha256=drops['raw_sha256'],
                  tokens=[dict(type=t['type'],payload_u32=t['payload_u32']) for t in original]+added)]
    epic_path = 'etc/epicproduction/epicproduction.etc'
    epic = read('epic-before',epic_path)
    plan = args.output/'plan.json'
    plan.write_text(json.dumps(dict(schema=2,input_sha256=BASE_SHA,edits=edits),ensure_ascii=False,indent=2)+'\n','utf8')
    candidate = args.output/'Script.pvf'
    receipts=[]
    for operation in ('patch','verify'):
        receipt=json.loads(run(operation,operation,'--pvf',args.base,'--plan',plan,'--out',candidate))
        if receipt['input_sha256'] != BASE_SHA or receipt['changed_entries'] != 1:
            raise ValueError('Unexpected archive diff')
        receipts.append(receipt)
    after = read('epic-after',epic_path,pvf=candidate)['tokens']
    if after != epic['tokens']:
        raise ValueError('Unselected epic configuration changed')
    drop_after = read('drop-after',path,pvf=candidate)['tokens']
    if len(after) != len(epic['tokens']) or drop_after[:len(original)] != original or len(drop_after) != len(original)+50:
        raise ValueError('Unreviewed token changes')
    for actual,expected in zip(drop_after[len(original):],added):
        if any(actual.get(k) != v for k,v in expected.items()):raise ValueError('Drop token mismatch')
    output_sha=sha(candidate)
    if any(r['output_sha256'] != output_sha for r in receipts) or sha(args.base) != BASE_SHA:
        raise ValueError('Archive identity changed')
    result=dict(passed=True,inputSHA256=BASE_SHA,candidateSHA256=output_sha,bytes=candidate.stat().st_size,
                toolSHA256=TOOL_SHA,planSHA256=sha(plan),changedEntries=1,
                unchangedEntries=receipts[-1]['unchanged_entries_verified'],rows=rows,
                epicConfigurationUnchanged=True,published=False,deployed=False,indexesGenerated=False)
    (args.output/'result.json').write_text(json.dumps(result,indent=2)+'\n','utf8')
    print(json.dumps(result))


if __name__ == '__main__':
    main()
