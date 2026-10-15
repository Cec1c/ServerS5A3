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
        pushes=[c.args[0] for c in run.call_args_list if 'push' in c.args[0]]
        self.assertEqual(len(pushes),1)
        self.assertEqual(pushes[0][-4:],['-o','ci.skip',self.gl.git,'refs/sync/source:refs/tags/client-v105'])

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
