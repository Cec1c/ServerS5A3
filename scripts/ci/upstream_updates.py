"""Stage upstream PRs and promote only the exact merge commit that passed CI.

Run this trusted script from the default branch, never from the incoming PR.
Validation runs separately with read-only credentials and no deployment secrets.
"""
import argparse
import base64
import json
import os
import re
import time
import urllib.error
import urllib.request

from sync_repositories import SyncError, git

BRANCH = "automation/gitgud-upstream"
GUARDED = (".github/", "scripts/ci/", ".gitlab/")


class GitHub:
    def __init__(self, repository, token):
        if not re.fullmatch(r"[\w.-]+/[\w.-]+", repository):
            raise SyncError("Invalid GitHub repository")
        self.repository, self.token = repository, token

    def request(self, method, path, body=None):
        request = urllib.request.Request(
            f"https://api.github.com/repos/{self.repository}/{path}",
            data=None if body is None else json.dumps(body).encode(), method=method,
            headers={"Authorization": f"Bearer {self.token}", "Accept": "application/vnd.github+json",
                     "Content-Type": "application/json", "X-GitHub-Api-Version": "2022-11-28"})
        try:
            with urllib.request.urlopen(request, timeout=30) as response:
                raw = response.read()
                return json.loads(raw) if raw else None
        except urllib.error.HTTPError as error:
            # Provider responses can contain credential-bearing URLs.
            raise SyncError(f"GitHub API {method} failed (HTTP {error.code})") from None
        except (urllib.error.URLError, TimeoutError):
            raise SyncError(f"GitHub API {method} connection failed") from None


def sha(value):
    if not re.fullmatch(r"[0-9a-f]{40}", value):
        raise SyncError("Invalid commit ID")
    return value


def ancestor(directory, before, after):
    return git(directory, "merge-base", "--is-ancestor", before, after, check=False).returncode == 0


def guarded_changes(directory, base, head):
    common = git(directory, "merge-base", base, head).stdout.strip()
    names = git(directory, "diff", "--name-only", "-z", common, head).stdout.split("\0")
    return any(p.startswith(GUARDED) or p == ".gitlab-ci.yml" for p in names)


def pr_body(base, head, reason):
    run = os.environ.get("GITHUB_RUN_ID", "")
    repo = os.environ.get("GITHUB_REPOSITORY", "")
    link = f"\n\nValidation: https://github.com/{repo}/actions/runs/{run}" if run and repo else ""
    return ("Bring the original GitGud upstream changes into this fork while retaining fork changes.\n\n"
            f"Base: `{base}`\nUpstream: `{head}`\n\n{reason}\n\n"
            "Only the exact merge commit that passes all validation jobs may be fast-forwarded to master. "
            "Conflicts, failures, CI changes or a moving base leave this PR open. This does not deploy a server."
            + link)


def prepare(directory, upstream, api, attempts=8):
    git(directory, "fetch", "--no-tags", "origin", "+refs/heads/master:refs/upstream-check/base")
    git(directory, "fetch", "--no-tags", upstream, "+refs/heads/master:refs/upstream-check/head")
    base = sha(git(directory, "rev-parse", "refs/upstream-check/base").stdout.strip())
    head = sha(git(directory, "rev-parse", "refs/upstream-check/head").stdout.strip())
    result = {"state": "current", "base": base, "head": head, "pr": "", "merge": ""}
    if ancestor(directory, head, base):
        return result
    existing = git(directory, "ls-remote", "origin", f"refs/heads/{BRANCH}").stdout.strip()
    if existing:
        git(directory, "fetch", "--no-tags", "origin", f"+refs/heads/{BRANCH}:refs/upstream-check/previous")
        if not ancestor(directory, "refs/upstream-check/previous", head):
            raise SyncError("Upstream or automation branch diverged; preserve it for manual review")
    git(directory, "push", "origin", f"{head}:refs/heads/{BRANCH}")
    owner = api.repository.split("/")[0]
    matches = api.request("GET", f"pulls?state=open&base=master&head={owner}:{BRANCH}")
    body = pr_body(base, head, "Preparing the merge candidate for validation.")
    if matches:
        number = int(matches[0]["number"])
        api.request("PATCH", f"pulls/{number}", {"body": body})
    else:
        created = api.request("POST", "pulls", {
            "title": "chore(sync): update from GitGud upstream", "head": BRANCH, "base": "master", "body": body})
        number = int(created["number"])
    result["pr"] = str(number)
    reason = ""
    if guarded_changes(directory, base, head):
        reason = "Incoming CI/workflow changes require manual review; automatic merge is disabled for this PR."
    elif git(directory, "merge-tree", "--write-tree", base, head, check=False).returncode:
        reason = "The upstream changes conflict with the fork; resolve and review this PR manually."
    if reason:
        api.request("PATCH", f"pulls/{number}", {"body": pr_body(base, head, reason)})
        result["state"] = "review"
        return result
    # GitHub asynchronously creates refs/pull/N/merge even for a PR opened by
    # GITHUB_TOKEN. Its automatic event suppression does not affect these jobs.
    for _ in range(attempts):
        pr = api.request("GET", f"pulls/{number}")
        if pr["head"]["sha"] != head or pr["base"]["sha"] != base:
            raise SyncError("PR changed while preparing; leave it open for the next run")
        fetched = git(directory, "fetch", "--no-tags", "origin",
                      f"+refs/pull/{number}/merge:refs/upstream-check/merge", check=False)
        if fetched.returncode == 0:
            merge = sha(git(directory, "rev-parse", "refs/upstream-check/merge").stdout.strip())
            parents = git(directory, "show", "-s", "--format=%P", merge).stdout.strip().split()
            if parents == [base, head]:
                result.update(state="validate", merge=merge)
                return result
        time.sleep(2)
    raise SyncError("GitHub merge candidate is not ready; PR retained for retry")


def promote(directory, api, base, head, merge, number):
    base, head, merge = sha(base), sha(head), sha(merge)
    pr = api.request("GET", f"pulls/{int(number)}")
    if (pr["state"] != "open" or pr["head"]["sha"] != head or pr["base"]["sha"] != base
            or pr["head"]["ref"] != BRANCH or pr["head"]["repo"]["full_name"] != api.repository):
        raise SyncError("PR/base changed after validation; nothing was merged")
    git(directory, "fetch", "--no-tags", "origin", "+refs/heads/master:refs/upstream-check/base",
        f"+refs/pull/{int(number)}/merge:refs/upstream-check/merge")
    if git(directory, "rev-parse", "refs/upstream-check/base").stdout.strip() != base:
        raise SyncError("Master advanced after validation; nothing was merged")
    if git(directory, "rev-parse", "refs/upstream-check/merge").stdout.strip() != merge:
        raise SyncError("Merge candidate changed after validation; nothing was merged")
    if git(directory, "show", "-s", "--format=%P", merge).stdout.strip().split() != [base, head]:
        raise SyncError("Unexpected merge parents")
    if guarded_changes(directory, base, head):
        raise SyncError("CI changes require manual review")
    # A normal non-force push atomically refuses an intervening divergent base.
    # The tested merge itself reaches master; no new untested merge is created.
    git(directory, "push", "origin", f"{merge}:refs/heads/master")
    observed = git(directory, "ls-remote", "origin", "refs/heads/master").stdout.split()[0]
    if observed != merge:
        raise SyncError("Master changed after promotion; inspect before retrying")
    # GITHUB_TOKEN pushes do not trigger another push workflow. Explicitly
    # dispatch the trusted mirror workflow so the writable GitGud fork follows.
    api.request("POST", "actions/workflows/repository-sync.yml/dispatches", {"ref": "master"})
    return {"state": "merged", "pr": str(number), "merge": merge}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("prepare", "promote"))
    parser.add_argument("--upstream")
    for name in ("base", "head", "merge", "pr"):
        parser.add_argument("--" + name)
    args = parser.parse_args()
    repository, token = os.environ.get("GITHUB_REPOSITORY", ""), os.environ.pop("GH_TOKEN", "")
    api = GitHub(repository, token)
    if not token:
        raise SyncError("GitHub token required")
    # GitHub-only authentication; never send it to GitGud or echo it in URLs.
    encoded = base64.b64encode(f"x-access-token:{token}".encode()).decode()
    os.environ.update(GIT_CONFIG_COUNT="1", GIT_CONFIG_KEY_0="http.https://github.com/.extraheader",
                      GIT_CONFIG_VALUE_0=f"AUTHORIZATION: basic {encoded}")
    if args.action == "prepare":
        if not args.upstream or not re.fullmatch(r"https://gitgud\.io/[A-Za-z0-9_./-]+\.git", args.upstream):
            raise SyncError("Expected a public GitGud HTTPS repository URL")
        result = prepare(".", args.upstream, api)
    else:
        result = promote(".", api, args.base or "", args.head or "", args.merge or "", args.pr or "0")
    print(json.dumps(result, indent=2))
    if output := os.environ.get("GITHUB_OUTPUT"):
        with open(output, "a", encoding="utf-8") as stream:
            for key, value in result.items():
                stream.write(f"{key}={value}\n")


if __name__ == "__main__":
    try:
        main()
    except SyncError as error:
        print(f"Upstream integration stopped: {error}")
        raise SystemExit(1)
