import copy
import io
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
import urllib.request
from unittest.mock import patch
import zipfile

import sync_client_releases as sync


class ClientReleaseTests(unittest.TestCase):
    def setUp(self):
        self.up=sync.Repository("upstream","gitlab",1)
        self.gl=sync.Repository("gitgud","gitlab",2,"token",True)
        self.gh=sync.Repository("github","github","owner/repo","token",True)
        self.item={"tag":"client-v105","commit":"a"*40,"source":"https://gitgud.io/source/repo/-/releases/client-v105","assets":{"DNF-v105.zip":{"sha256":"b"*64,"length":100}},"repo":self.up}

    def test_readonly_inventory_is_never_a_destination(self):
        actions=sync.plan({"upstream":[self.item],"gitgud":[],"github":[]},[self.up,self.gl,self.gh])
        self.assertEqual([r.key for r,_ in actions],["gitgud","github"])

    def test_bidirectional_and_second_run_noop(self):
        for source,target in [(self.gl,self.gh),(self.gh,self.gl)]:
            rows={source.key:[self.item],target.key:[]}
            actions=sync.plan(rows,[source,target])
            self.assertEqual([r.key for r,_ in actions],[target.key])
            rows[target.key]=[copy.deepcopy(self.item)]
            self.assertEqual(sync.plan(rows,[source,target]),[])

    def test_conflict_prevents_unrelated_release_plan(self):
        conflict=copy.deepcopy(self.item);conflict['assets']['DNF-v105.zip']['sha256']='c'*64
        additional=copy.deepcopy(self.item);additional['tag']='client-v106'
        with self.assertRaises(sync.SyncError):
            sync.plan({'github':[self.item,additional],'gitgud':[conflict]},[self.gh,self.gl])

    def test_tag_target_and_asset_set_are_immutable(self):
        for mutation in ['commit','asset']:
            changed=copy.deepcopy(self.item)
            if mutation=='commit':changed['commit']='d'*40
            else:changed['assets']['extra.zip']=changed['assets']['DNF-v105.zip']
            with self.assertRaises(sync.SyncError):
                sync.plan({'github':[self.item],'gitgud':[changed]},[self.gh,self.gl])

    def test_zip_hashes_actual_executable(self):
        with tempfile.TemporaryDirectory() as d:
            p=Path(d)/'client.zip'
            with zipfile.ZipFile(p,'w') as z:z.writestr('DNF.exe',b'MZtest');z.writestr('中文说明.txt','测试')
            self.assertEqual(sync.inspect_zip(p),{'sha256':sync.hashlib.sha256(b'MZtest').hexdigest(),'length':6})

    def test_unsafe_client_archives_rejected(self):
        for name,raw in [('../DNF.exe',b'MZx'),('DNF.exe',b'bad'),('other.exe',b'MZx'),('dir\\DNF.exe',b'MZx')]:
            with tempfile.TemporaryDirectory() as d:
                p=Path(d)/'bad.zip'
                with zipfile.ZipFile(p,'w') as z:z.writestr(name.replace('\\','/'),raw)
                if '\\' in name:
                    p.write_bytes(p.read_bytes().replace(name.replace('\\','/').encode(),name.encode()))
                with self.assertRaises(sync.SyncError):sync.inspect_zip(p)

    def test_python_sources_under_scripts_are_only_read_as_archive_members(self):
        with tempfile.TemporaryDirectory() as d:
            p=Path(d)/'client.zip';marker=Path(d)/'executed'
            script=f'from pathlib import Path\nPath({str(marker)!r}).touch()\nraise RuntimeError("must not execute")\n'
            with zipfile.ZipFile(p,'w') as z:
                z.writestr('client/DNF.exe',b'MZtest')
                z.writestr('scripts/patch.py',script)
                z.writestr('scripts/client/test_patch.py',script)
                z.writestr('README.md','source evidence')
            self.assertEqual(sync.inspect_zip(p),{'sha256':sync.hashlib.sha256(b'MZtest').hexdigest(),'length':6})
            self.assertEqual(list(Path(d).iterdir()),[p])
            self.assertFalse(marker.exists())

    def test_python_member_allowance_does_not_extend_to_other_paths_or_types(self):
        for name in ['patch.py','other/patch.py','client/scripts/patch.py','Scripts/patch.py',
                     'scripts/patch.pyc','scripts/patch.py.exe','scripts/patch.sh',
                     'scripts/../patch.py','scripts/./patch.py','scripts//patch.py',
                     '/scripts/patch.py','scripts/drive:patch.py']:
            with self.subTest(name=name),tempfile.TemporaryDirectory() as d:
                p=Path(d)/'client.zip'
                with zipfile.ZipFile(p,'w') as z:z.writestr('DNF.exe',b'MZtest');z.writestr(name,b'pass')
                with self.assertRaises(sync.SyncError):sync.inspect_zip(p)

    def test_python_members_keep_duplicate_and_non_file_guards(self):
        for kind in ['duplicate','symlink','directory','backslash','nul']:
            with self.subTest(kind=kind),tempfile.TemporaryDirectory() as d:
                p=Path(d)/'client.zip'
                with zipfile.ZipFile(p,'w') as z:
                    z.writestr('DNF.exe',b'MZtest')
                    if kind=='duplicate':
                        z.writestr('scripts/a.py',b'pass');z.writestr('scripts/A.py',b'pass')
                    elif kind=='symlink':
                        entry=zipfile.ZipInfo('scripts/link.py');entry.create_system=3;entry.external_attr=0o120777<<16
                        z.writestr(entry,b'../outside')
                    elif kind=='directory':z.writestr('scripts/',b'')
                    else:z.writestr('scripts/a.py',b'pass')
                if kind=='backslash':p.write_bytes(p.read_bytes().replace(b'scripts/a.py',b'scripts\\a.py'))
                if kind=='nul':p.write_bytes(p.read_bytes().replace(b'scripts/a.py',b'scripts/\0.py'))
                with self.assertRaises(sync.SyncError):sync.inspect_zip(p)

    def test_python_members_keep_executable_and_archive_limits(self):
        for kind in ['missing-exe','multiple-exe','invalid-exe','size','count']:
            with self.subTest(kind=kind),tempfile.TemporaryDirectory() as d:
                p=Path(d)/'client.zip'
                with zipfile.ZipFile(p,'w') as z:
                    z.writestr('scripts/patch.py',b'pass')
                    if kind!='missing-exe':z.writestr('DNF.exe',b'bad' if kind=='invalid-exe' else b'MZtest')
                    if kind=='multiple-exe':z.writestr('client/DNF.exe',b'MZtest')
                    if kind=='count':
                        for i in range(19):z.writestr(f'scripts/a{i}.py',b'pass')
                with patch.object(sync,'MAX_ASSET',9 if kind=='size' else sync.MAX_ASSET):
                    with self.assertRaises(sync.SyncError):sync.inspect_zip(p)

    def test_python_source_crc_and_download_digest_are_still_checked(self):
        with tempfile.TemporaryDirectory() as d:
            p=Path(d)/'client.zip'
            with zipfile.ZipFile(p,'w') as z:z.writestr('DNF.exe',b'MZtest');z.writestr('scripts/a.py',b'original source')
            expected=sync.digest(p);tampered=p.read_bytes().replace(b'original source',b'modified source')
            p.write_bytes(tampered)
            with self.assertRaisesRegex(sync.SyncError,'Invalid client ZIP'):sync.inspect_zip(p)
            with patch.object(sync.OPENER,'open',return_value=io.BytesIO(tampered)):
                with self.assertRaisesRegex(sync.SyncError,'digest conflicts'):
                    sync.download('https://example.test/client.zip',Path(d)/'cache',expected,fresh=True)

    def test_plain_http_and_embedded_credentials_rejected(self):
        for url in ['http://example.test/a','https://user:secret@example.test/a','https://example.test/a#x']:
            with self.assertRaises(sync.SyncError):sync.safe_url(url)

    def test_authenticated_redirect_is_never_forwarded(self):
        for header in ['Authorization','PRIVATE-TOKEN']:
            req=urllib.request.Request('https://api.example.test/a',headers={header:'test'})
            with self.assertRaises(sync.SyncError):sync.HTTPSRedirect().redirect_request(req,None,302,'',{},'https://cdn.example.test/a')

    def test_foreign_asset_refused_before_download(self):
        row={'tag_name':'client-v105','commit':{'id':'a'*40},'description':'','_links':{'self':'https://gitgud.io/o/r/-/releases/client-v105'},'assets':{'links':[{'url':'https://other.test/client.zip'}]}}
        with patch.object(sync,'download') as download:
            with self.assertRaises(sync.SyncError):self.gl.identity(row,'.')
            download.assert_not_called()

    def test_malformed_provenance_rejected(self):
        with self.assertRaises(sync.SyncError):sync.provenance(sync.MARKER+'{} -->')

    def test_invalid_project_is_refused(self):
        with self.assertRaises(sync.SyncError):sync.validate_configuration('owner/repo',0)

    def test_preview_identity_survives_gitlab_hop(self):
        self.item['preview']=True
        record=sync.release_record(self.item)
        self.assertTrue(record['preview'])
        other=copy.deepcopy(self.item);other['preview']=False
        self.assertNotEqual(sync.content_identity(self.item),sync.content_identity(other))

    def test_no_release_write_when_target_appears(self):
        with patch.object(self.gl,'prepare',return_value=None),patch.object(self.gl,'get',return_value={'id':1}),patch.object(self.gl,'api') as api:
            with self.assertRaises(sync.SyncError):self.gl.publish(self.item,'.','.')
            api.assert_not_called()

    def test_bare_tag_conflict_detected_without_release(self):
        with patch.object(self.gl,'tag_commit',return_value='f'*40):
            with self.assertRaises(sync.SyncError):self.gl.prepare(self.item)

    def test_orphan_package_conflict_precedes_tag_write(self):
        with patch.object(self.gl,'tag_commit',return_value=None),patch.object(sync,'download',side_effect=sync.SyncError('digest mismatch')),patch.object(self.gl,'ensure_tag') as tag:
            with self.assertRaises(sync.SyncError):self.gl.prepare(self.item,'.')
            tag.assert_not_called()

    def test_matching_annotated_tag_needs_no_push(self):
        with patch.object(self.gh,'tag_commit',return_value='a'*40),patch.object(sync.subprocess,'run') as run:
            self.gh.ensure_tag(self.item,'.')
            run.assert_not_called()

    def test_new_github_tag_requires_non_recursive_ci_token(self):
        with patch.dict(sync.os.environ,{},clear=True),patch.object(self.gh,'tag_commit',return_value=None),patch.object(sync.subprocess,'run') as run:
            with self.assertRaisesRegex(sync.SyncError,'trusted CI'):
                self.gh.ensure_tag(self.item,'.')
            run.assert_not_called()
        with patch.dict(sync.os.environ,{'GITHUB_ACTIONS':'true','GITHUB_TOKEN':'token'},clear=True):
            self.gh.require_tag_policy(None)
        with patch.dict(sync.os.environ,{'GITHUB_ACTIONS':'true','GITHUB_TOKEN':'different'},clear=True):
            with self.assertRaises(sync.SyncError):self.gh.require_tag_policy(None)

    def test_gitgud_tag_push_skips_pipeline_and_never_pushes_branch(self):
        self.gl.git='https://gitgud.io/owner/repo.git'
        self.item['repo']=self.gh
        def fake_run(argv,**kwargs):
            return SimpleNamespace(returncode=0,stdout=('a'*40).encode() if 'rev-parse' in argv else b'')
        with patch.object(self.gl,'tag_commit',return_value=None),patch.object(sync.subprocess,'run',side_effect=fake_run) as run:
            self.gl.ensure_tag(self.item,'.')
        commands=[c.args[0] for c in run.call_args_list]
        stages=[next(stage for stage in ('init','fetch','rev-parse','push') if stage in command) for command in commands]
        self.assertEqual(stages,['init','fetch','rev-parse','push'])
        pushes=[command for command in commands if 'push' in command]
        self.assertEqual(len(pushes),1)
        self.assertEqual(pushes[0][-4:],['-o','ci.skip',self.gl.git,'refs/sync/source:refs/tags/client-v105'])
        self.assertFalse(any(argument in ('-f','--force','--mirror') for argument in pushes[0]))

    def test_tag_fetch_failure_reports_stage_exit_and_safe_classification(self):
        self.gl.git='https://gitgud.io/owner/repo.git'
        self.item['repo']=self.gh
        def fake_run(argv,**kwargs):
            if 'fetch' in argv:
                return SimpleNamespace(returncode=128,stdout=b'',stderr=b'fatal: unable to access remote: Could not resolve host: gitgud.io')
            return SimpleNamespace(returncode=0,stdout=b'',stderr=b'')
        with patch.object(self.gl,'tag_commit',return_value=None),patch.object(sync.subprocess,'run',side_effect=fake_run):
            with self.assertRaisesRegex(sync.SyncError,r'tag=client-v105; stage=fetch; exit=128; classification=network'):
                self.gl.ensure_tag(self.item,'.')

    def test_tag_push_permission_failure_is_classified(self):
        self.gl.git='https://gitgud.io/owner/repo.git'
        self.item['repo']=self.gh
        def fake_run(argv,**kwargs):
            if 'push' in argv:
                return SimpleNamespace(returncode=128,stdout=b'',stderr=b'remote: Permission to owner/repo.git denied to bot')
            return SimpleNamespace(returncode=0,stdout=('a'*40).encode() if 'rev-parse' in argv else b'',stderr=b'')
        with patch.object(self.gl,'tag_commit',return_value=None),patch.object(sync.subprocess,'run',side_effect=fake_run):
            with self.assertRaisesRegex(sync.SyncError,r'tag=client-v105; stage=push; exit=128; classification=workflow-permission-or-auth'):
                self.gl.ensure_tag(self.item,'.')

    def test_unknown_tag_stderr_never_leaks_sensitive_values(self):
        self.gl.git='https://gitgud.io/owner/repo.git'
        self.item['repo']=self.gh
        sensitive=b'raw-secret Authorization: Basic dXNlcjpwYXNz https://user:password@example.test/repo'
        def fake_run(argv,**kwargs):
            if 'push' in argv:
                return SimpleNamespace(returncode=42,stdout=b'',stderr=b'unclassified failure: '+sensitive)
            return SimpleNamespace(returncode=0,stdout=('a'*40).encode() if 'rev-parse' in argv else b'',stderr=b'')
        with patch.object(self.gl,'tag_commit',return_value=None),patch.object(sync.subprocess,'run',side_effect=fake_run):
            with self.assertRaises(sync.SyncError) as raised:
                self.gl.ensure_tag(self.item,'.')
        message=str(raised.exception)
        self.assertIn('tag=client-v105; stage=push; exit=42; classification=unknown',message)
        for value in ('raw-secret','Authorization','dXNlcjpwYXNz','user:password'):
            self.assertNotIn(value,message)

    def test_trusted_ci_creates_existing_commit_ref_and_reads_back(self):
        self.item['repo']=self.gl; self.gl.git='https://gitgud.io/owner/repo.git'
        commit='a'*40; tag='refs/tags/client-v105'
        def fake_run(argv,**kwargs):
            out=commit if 'rev-parse' in argv else 'commit' if 'cat-file' in argv else ''
            return SimpleNamespace(returncode=0,stdout=out.encode(),stderr=b'')
        with patch.dict(sync.os.environ,{'GITHUB_ACTIONS':'true','GITHUB_TOKEN':'token'},clear=True), \
             patch.object(self.gh,'tag_commit',side_effect=[None,commit]), \
             patch.object(self.gh,'api',side_effect=[{'sha':commit},{'ref':tag,'object':{'sha':commit}}]) as api, \
             patch.object(sync.subprocess,'run',side_effect=fake_run) as run:
            self.gh.ensure_tag(self.item,'.')
        self.assertEqual(api.call_args_list[-1].kwargs,{'method':'POST','body':{'ref':tag,'sha':commit}})
        self.assertFalse(any('push' in c.args[0] for c in run.call_args_list))

    def test_ref_api_refusal_does_not_fall_back_to_another_credential(self):
        self.item['repo']=self.gl; self.gl.git='https://gitgud.io/owner/repo.git'
        def fake_run(argv,**kwargs):
            return SimpleNamespace(returncode=0,stdout=(('a'*40) if 'rev-parse' in argv else 'commit' if 'cat-file' in argv else '').encode(),stderr=b'')
        with patch.dict(sync.os.environ,{'GITHUB_ACTIONS':'true','GITHUB_TOKEN':'token'},clear=True), \
             patch.object(self.gh,'tag_commit',return_value=None), \
             patch.object(self.gh,'api',side_effect=[{'sha':'a'*40},sync.SyncError('API POST failed with HTTP 403')]), \
             patch.object(sync.subprocess,'run',side_effect=fake_run) as run:
            with self.assertRaisesRegex(sync.SyncError,'HTTP 403'):
                self.gh.ensure_tag(self.item,'.')
        self.assertFalse(any('push' in c.args[0] for c in run.call_args_list))

    def test_workflow_permission_has_specific_safe_classification(self):
        self.assertEqual(sync._git_failure_classification(b'refusing to allow a GitHub App to create or update workflow private-file without workflows permission'), 'github-workflow-write-permission')

    def test_interrupted_draft_resumes_only_with_verified_assets(self):
        draft={'id':1,'tag_name':'client-v105','draft':True,'prerelease':False,'body':sync.MARKER+sync.json.dumps(sync.release_record(self.item))+' -->','assets':[{'name':'DNF-v105.zip','size':100,'digest':'sha256:'+'b'*64}]}
        with patch.object(self.gh,'tag_commit',return_value='a'*40),patch.object(self.gh,'list',return_value=[draft]):
            self.assertEqual(self.gh.prepare(self.item),draft)
            draft['assets'][0]['digest']='sha256:'+'c'*64
            with self.assertRaises(sync.SyncError):self.gh.prepare(self.item)

    def test_draft_preview_mismatch_prevents_publication(self):
        self.item['preview']=True
        draft={'id':1,'tag_name':'client-v105','draft':True,'prerelease':False,'body':sync.MARKER+sync.json.dumps(sync.release_record(self.item))+' -->','assets':[]}
        with patch.object(self.gh,'tag_commit',return_value='a'*40),patch.object(self.gh,'list',return_value=[draft]),patch.object(self.gh,'ensure_tag') as tag,patch.object(self.gh,'api') as api:
            with self.assertRaisesRegex(sync.SyncError,'preview state'):
                self.gh.publish(self.item,'.','.')
            tag.assert_not_called()
            api.assert_not_called()


if __name__=='__main__':unittest.main()
