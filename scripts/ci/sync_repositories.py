"""Fast-forward one branch between two repositories; never merge or force push."""
import argparse
import base64
import json
import os
import subprocess
import tempfile
from pathlib import Path


class SyncError(RuntimeError):
    pass


def git(directory, *arguments, check=True):
    result = subprocess.run(
        ["git", "-C", str(directory), *arguments],
        text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        env={**os.environ, "GIT_TERMINAL_PROMPT": "0"},
    )
    if check and result.returncode:
        # Do not echo remote URLs or credential-bearing arguments.
        raise SyncError(f"git {arguments[0]} failed (exit {result.returncode}): {result.stderr.strip()}")
    return result


def synchronize(github, gitgud, branch="master", apply=False):
    with tempfile.TemporaryDirectory(prefix="servers5a3-sync-") as temporary:
        git(temporary, "init", "--bare", "--quiet")
        if git(temporary, "check-ref-format", "--branch", branch, check=False).returncode:
            raise SyncError("Invalid branch name")
        reference = f"refs/heads/{branch}"
        for name, url in (("github", github), ("gitgud", gitgud)):
            git(temporary, "remote", "add", name, url)
            git(temporary, "fetch", "--no-tags", name, f"{reference}:refs/sync/{name}")
        heads = {name: git(temporary, "rev-parse", f"refs/sync/{name}").stdout.strip()
                 for name in ("github", "gitgud")}
        result = {"branch": branch, "before": heads, "applied": False, "direction": "equal"}
        if heads["github"] == heads["gitgud"]:
            return result
        if git(temporary, "merge-base", "--is-ancestor", heads["gitgud"], heads["github"], check=False).returncode == 0:
            source, destination = "github", "gitgud"
        elif git(temporary, "merge-base", "--is-ancestor", heads["github"], heads["gitgud"], check=False).returncode == 0:
            source, destination = "gitgud", "github"
            changed = git(temporary, "diff", "--name-only", "-z", heads["github"], heads["gitgud"]).stdout.split("\0")
            if any(path.startswith((".github/", "scripts/ci/")) for path in changed):
                raise SyncError("GitGud changes CI definitions or synchronization code; review and merge on GitHub first")
        else:
            raise SyncError("Branches diverged; review and merge explicitly. Neither remote was changed")
        result["direction"] = f"{source}-to-{destination}"
        result["commit"] = heads[source]
        if apply:
            # No force flag: an intervening destination commit is rejected by receive-pack.
            git(temporary, "push", destination, f"{heads[source]}:{reference}")
            observed = {}
            for name in ("github", "gitgud"):
                output = git(temporary, "ls-remote", "--exit-code", name, reference).stdout
                observed[name] = output.split()[0]
            result["after"] = observed
            result["applied"] = True
            if observed["github"] != observed["gitgud"]:
                raise SyncError("Remote changed during synchronization; fetch again on the next run")
        return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--github", required=True)
    parser.add_argument("--gitgud", required=True)
    parser.add_argument("--branch", default="master")
    parser.add_argument("--apply", action="store_true")
    args = parser.parse_args()
    token = os.environ.pop("GH_SYNC_TOKEN", "")
    if token:
        # URL-scoped config prevents forwarding GitHub credentials to GitGud.
        encoded = base64.b64encode(f"x-access-token:{token}".encode()).decode()
        os.environ.update({
            "GIT_CONFIG_COUNT": "1",
            "GIT_CONFIG_KEY_0": "http.https://github.com/.extraheader",
            "GIT_CONFIG_VALUE_0": f"AUTHORIZATION: basic {encoded}",
        })
    try:
        result = synchronize(args.github, args.gitgud, args.branch, args.apply)
    except SyncError as error:
        print(f"Synchronization stopped: {error}")
        raise SystemExit(1)
    rendered = json.dumps(result, indent=2)
    print(rendered)
    if summary := os.environ.get("GITHUB_STEP_SUMMARY"):
        with Path(summary).open("a", encoding="utf-8") as output:
            output.write(f"```json\n{rendered}\n```\n")


if __name__ == "__main__":
    main()
