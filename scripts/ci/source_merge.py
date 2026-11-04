"""Trusted developers may attest local validation and merge without waiting for cloud CI."""
import argparse
from datetime import datetime, timezone
import hashlib
import json
from pathlib import Path
import re
import subprocess

ACTORS = {"Cec1c": 170788854, "16www": 45917572}
CONTEXT = "merge-ready"


def api(method, path, payload=None):
    args = ["gh", "api", "--method", method, path]
    if payload is not None:
        args += ["--input", "-"]
    proc = subprocess.run(args, input=None if payload is None else json.dumps(payload),
                          text=True, encoding="utf-8", capture_output=True)
    if proc.returncode:
        raise RuntimeError(proc.stderr.strip() or "GitHub request failed; inspect original operation")
    return json.loads(proc.stdout) if proc.stdout.strip() else None


def identity(repo, call=api):
    if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", repo):
        raise ValueError("invalid repository")
    actor = call("GET", "user")
    if actor.get("login") not in ACTORS or ACTORS[actor["login"]] != actor.get("id"):
        raise ValueError("quick is limited to Cec1c and 16www")
    permission = call("GET", f"repos/{repo}/collaborators/{actor['login']}/permission")
    if permission.get("permission") not in ("write", "maintain", "admin"):
        raise ValueError("current repository write permission required")
    return {"login": actor["login"], "id": actor["id"]}


def save(path, value):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def merge(repo, head, statement, output, pr=None, branch=None, base="master", call=api):
    """No candidate code is executed. One exact-head merge; never retry an unknown write."""
    if not re.fullmatch(r"[0-9a-f]{40}", head) or base not in ("main", "master"):
        raise ValueError("full SHA and main/master target required")
    if not isinstance(statement, str) or not statement.strip():
        raise ValueError("provide the developer's existing local validation statement")
    actor = identity(repo, call)
    digest = hashlib.sha256(statement.encode("utf-8")).hexdigest()
    record = dict(schema=1, mode="local-attested", actor=actor, repository=repo,
                  attestedHead=head, base=base, statement=statement, statementSha256=digest,
                  remoteCIWaited=False, testsExecuted=False, state="started",
                  startedAt=datetime.now(timezone.utc).isoformat())
    # Do not lose an earlier receipt or replay a successful merge.
    old = {}
    if Path(output).exists():
        old = json.loads(Path(output).read_text(encoding="utf-8"))
        if any(old.get(k) != record[k] for k in ("repository", "attestedHead", "base", "statementSha256")):
            raise ValueError("receipt belongs to different frozen inputs")
        if old.get("state") == "merged":
            live = call("GET", f"repos/{repo}/pulls/{old['pr']}")
            if not live.get("merged") or live.get("merge_commit_sha") != old.get("mergeSha"):
                raise ValueError("saved merge receipt differs from GitHub")
            return old
        if old.get("pr"):
            pr = old["pr"]
    save(output, record)
    try:
        if pr is None:
            branch = branch or "quick/" + head[:16]
            if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._/-]*", branch) or ".." in branch:
                raise ValueError("invalid branch")
            refs = call("GET", f"repos/{repo}/git/matching-refs/heads/{branch}")
            exact = [r for r in refs if r["ref"] == "refs/heads/" + branch]
            if exact and exact[0]["object"]["sha"] != head:
                raise ValueError("branch differs from attested SHA")
            if not exact:
                call("POST", f"repos/{repo}/git/refs", {"ref": "refs/heads/" + branch, "sha": head})
            existing = call("GET", f"repos/{repo}/pulls?state=open&head={repo.split('/')[0]}:{branch}&base={base}")
            if existing:
                pr = existing[0]["number"]
            else:
                created = call("POST", f"repos/{repo}/pulls", dict(title="quick: local validated " + head[:12],
                    head=branch, base=base, draft=True,
                    body="Trusted local validation; exact statement digest will be recorded before merge."))
                pr = created["number"]
        record["pr"] = int(pr)
        save(output, record)
        pull = call("GET", f"repos/{repo}/pulls/{pr}")
        if pull["head"]["sha"] != head or pull["base"]["ref"] != base:
            raise ValueError("PR head/base differs from frozen request")
        if pull["head"].get("repo", {}).get("full_name") != repo:
            raise ValueError("quick requires a same-repository branch owned by trusted writers")
        if pull.get("merged"):
            record.update(state="merged", mergeSha=pull["merge_commit_sha"], readbackOnly=True)
            save(output, record)
            return record
        if pull["state"] != "open":
            raise ValueError("PR is closed without merge")
        if old.get("mergeSubmitted"):
            raise ValueError("previous merge outcome needs inspection; no automatic write replay")
        current = call("GET", f"repos/{repo}/branches/{base}")["commit"]["sha"]
        comparison = call("GET", f"repos/{repo}/compare/{current}...{head}")
        if comparison["status"] not in ("ahead", "identical"):
            raise ValueError("integrate current mainline into the candidate first; only affected checks need updating")
        record["baseSha"] = current
        body = ("<!-- trusted-quick:" + head + ":" + digest + " -->\n"
                "**Local validation attested; cloud CI is not claimed as passed.**\n\n"
                f"Developer: `{actor['login']}` / `{actor['id']}`\n\n"
                f"Frozen source: `{head}`\n\nMainline: `{current}`\n\n"
                f"Local statement SHA256: `{digest}`\n\n"
                "Original statement is retained in the operator receipt; no private logs are published.")
        comment = call("POST", f"repos/{repo}/issues/{pr}/comments", {"body": body})
        record["attestationUrl"] = comment["html_url"]
        save(output, record)
        # Trusted labels suppress redundant full CI when a draft becomes ready.
        call("POST", f"repos/{repo}/issues/{pr}/labels", {"labels": ["quick-local-ci"]})
        if pull.get("draft"):
            call("POST", "graphql", {"query": "mutation($id:ID!){markPullRequestReadyForReview(input:{pullRequestId:$id}){pullRequest{id}}}",
                                       "variables": {"id": pull["node_id"]}})
        latest = call("GET", f"repos/{repo}/pulls/{pr}")
        if latest["head"]["sha"] != head or call("GET", f"repos/{repo}/branches/{base}")["commit"]["sha"] != current:
            raise ValueError("head/base moved before admission; inspect the current candidate")
        call("POST", f"repos/{repo}/statuses/{head}", dict(state="success", context=CONTEXT,
             description="local-attested by " + actor["login"] + "; cloud CI not required", target_url=comment["html_url"]))
        record["state"] = "merge-submitted"
        record["mergeSubmitted"] = True
        save(output, record)
        result = call("PUT", f"repos/{repo}/pulls/{pr}/merge", {"sha": head, "merge_method": "merge"})
        if not result.get("merged"):
            raise RuntimeError("merge not completed: " + result.get("message", "inspect original PR"))
        live = call("GET", f"repos/{repo}/pulls/{pr}")
        if not live.get("merged") or live["merge_commit_sha"] != result["sha"]:
            raise RuntimeError("merge response/readback differs; inspect original PR")
        record.update(state="merged", mergeSha=result["sha"], completedAt=datetime.now(timezone.utc).isoformat())
        save(output, record)
        return record
    except Exception as error:
        record.update(state="inspect-original-operation", error=str(error))
        save(output, record)
        raise


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--quick", action="store_true", required=True)
    parser.add_argument("--repo", required=True)
    parser.add_argument("--head", required=True)
    parser.add_argument("--pr", type=int)
    parser.add_argument("--branch")
    parser.add_argument("--base", choices=("master", "main"), default="master")
    parser.add_argument("--local-ci", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    result = merge(args.repo, args.head, args.local_ci.read_text(encoding="utf-8-sig"), args.output,
                   pr=args.pr, branch=args.branch, base=args.base)
    print(json.dumps({k: result[k] for k in ("state", "repository", "pr", "mergeSha", "mode")}, ensure_ascii=False))


if __name__ == "__main__":
    main()
