import json
from pathlib import Path
import tempfile
import unittest

from source_merge import merge, identity
from merge_admission import admit

HEAD, BASE, MERGED = "a" * 40, "b" * 40, "c" * 40
REPO = "Cec1c/ServerS5A3"


class FakeGitHub:
    def __init__(self):
        self.actor = {"login": "Cec1c", "id": 170788854}
        self.permission = "admin"
        self.head, self.base, self.status = HEAD, BASE, "ahead"
        self.merged, self.fail_merge = False, False
        self.calls = []

    def __call__(self, method, path, data=None):
        self.calls.append((method, path, data))
        if path == "user": return self.actor
        if path.endswith("/permission"): return {"permission": self.permission}
        if method == "GET" and path.endswith("/pulls/7"):
            return dict(head=dict(sha=self.head, repo=dict(full_name=REPO)), base=dict(ref="master"),
                        draft=True, state="open", node_id="PR_NODE", merged=self.merged, merge_commit_sha=MERGED)
        if "/branches/" in path: return {"commit": {"sha": self.base}}
        if "/compare/" in path: return {"status": self.status}
        if path.endswith("/comments"): return {"html_url": "https://github.com/repo/pull/7#issuecomment-1"}
        if path.endswith("/merge"):
            if self.fail_merge: raise RuntimeError("connection lost")
            self.merged = True
            return {"merged": True, "sha": MERGED}
        if method == "POST": return {}
        raise AssertionError((method, path))


class QuickTests(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory(); self.addCleanup(temp.cleanup)
        self.output = Path(temp.name) / "receipt.json"
        self.api = FakeGitHub()

    def run_merge(self):
        return merge(REPO, HEAD, "Local affected tests and Linux build passed; PRIVATE evidence path", self.output, pr=7, call=self.api)

    def test_exact_sha_attestation_and_merge_without_cloud_polling(self):
        receipt = self.run_merge()
        self.assertEqual(receipt["mergeSha"], MERGED)
        self.assertFalse(receipt["remoteCIWaited"])
        paths = [p for m,p,d in self.api.calls]
        self.assertFalse(any("actions/" in p for p in paths))
        status = next(d for m,p,d in self.api.calls if "/statuses/" in p)
        self.assertEqual(status["context"], "merge-ready")
        self.assertIn("local-attested", status["description"])
        comment = next(d for m,p,d in self.api.calls if p.endswith("/comments"))
        self.assertNotIn("PRIVATE", comment["body"])
        self.assertEqual(next(d for m,p,d in self.api.calls if p.endswith("/merge"))["sha"], HEAD)

    def test_only_two_accounts_with_write_access(self):
        for actor, permission in [({"login":"external", "id":123}, "write"),
                                  ({"login":"Cec1c", "id":123}, "admin"),
                                  ({"login":"16www", "id":45917572}, "read")]:
            self.api.actor, self.api.permission = actor, permission
            with self.assertRaises(ValueError): identity(REPO, self.api)
        self.api.actor = {"login":"16www", "id":45917572}; self.api.permission="write"
        self.assertEqual(identity(REPO, self.api)["login"], "16www")
        self.assertFalse(any(m != "GET" for m,p,d in self.api.calls))

    def test_wrong_head_and_changed_mainline_rejected_before_writes(self):
        for head,status in [("d"*40,"ahead"),(HEAD,"diverged")]:
            self.api = FakeGitHub(); self.api.head, self.api.status = head,status
            with self.assertRaises(ValueError): self.run_merge()
            self.assertFalse(any(m != "GET" for m,p,d in self.api.calls))

    def test_idempotent_success_readback_only(self):
        self.run_merge(); self.api.calls=[]
        self.assertEqual(self.run_merge()["state"], "merged")
        self.assertFalse(any(m != "GET" for m,p,d in self.api.calls))

    def test_unknown_write_retained_no_automatic_retry(self):
        self.api.fail_merge=True
        with self.assertRaises(RuntimeError): self.run_merge()
        self.assertEqual(sum(p.endswith("/merge") for m,p,d in self.api.calls),1)
        self.assertEqual(json.loads(self.output.read_text())["state"],"inspect-original-operation")
        for _ in range(2):
            self.api.calls=[]
            with self.assertRaises(ValueError): self.run_merge()
            self.assertFalse(any(m != "GET" for m,p,d in self.api.calls))
            self.assertTrue(json.loads(self.output.read_text())["mergeSubmitted"])

    def test_existing_receipt_cannot_be_reused_for_other_commit(self):
        self.run_merge()
        with self.assertRaises(ValueError): merge(REPO,"d"*40,"changed",self.output,pr=7,call=self.api)


class CIAdmissionTests(unittest.TestCase):
    def run_ci(self, required="success", **changes):
        run=dict(path=".github/workflows/verify.yml",event="pull_request",status="completed",
                 conclusion="success",head_sha=HEAD,id=123,run_attempt=1,html_url="https://github.com/run/123")
        run.update(changes);calls=[]
        def api(method,path,data=None):
            calls.append((method,path,data))
            if "/jobs?" in path:return {"jobs":[{"name":"required","conclusion":required}]}
            if path.endswith("/statuses?per_page=100"):return []
            return {}
        return admit(REPO,run,api),calls

    def test_successful_full_ci_gets_separate_admission(self):
        result,calls=self.run_ci()
        self.assertEqual(result["mode"],"github-ci")
        self.assertEqual(calls[-1][2]["context"],"merge-ready")

    def test_draft_quick_or_failed_ci_cannot_claim_normal_success(self):
        for changes in [dict(required="skipped"),dict(conclusion="failure"),dict(path=".github/workflows/other.yml")]:
            result,calls=self.run_ci(**changes)
            self.assertFalse(any(m=="POST" for m,p,d in calls))


if __name__ == "__main__": unittest.main()
