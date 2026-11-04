"""Publish normal CI admission from a trusted default-branch workflow, including fork PRs."""
import json
import os
from pathlib import Path

from source_merge import api, CONTEXT, ACTORS


def admit(repo, run, call=api):
    if run.get("path") != ".github/workflows/verify.yml" or run.get("event") not in ("pull_request", "workflow_dispatch"):
        return {"state": "unrelated-run"}
    if run.get("status") != "completed" or run.get("conclusion") != "success":
        return {"state": "not-successful"}
    head = run["head_sha"]
    jobs = call("GET", f"repos/{repo}/actions/runs/{run['id']}/attempts/{run['run_attempt']}/jobs?per_page=100")["jobs"]
    if not any(j["name"] == "required" and j["conclusion"] == "success" for j in jobs):
        return {"state": "no-complete-required-gate"}
    # Successful local admission on this same immutable SHA remains valid.
    statuses = call("GET", f"repos/{repo}/commits/{head}/statuses?per_page=100")
    latest = next((s for s in statuses if s["context"] == CONTEXT), None)
    if latest and latest["state"] == "success" and latest.get("description", "").startswith("local-attested by ") and ACTORS.get(latest.get("creator", {}).get("login")) == latest.get("creator", {}).get("id"):
        return {"state": "local-admission-preserved"}
    call("POST", f"repos/{repo}/statuses/{head}", dict(state="success", context=CONTEXT,
         description="github-ci: all selected checks passed", target_url=run["html_url"]))
    return {"state": "admitted", "head": head, "mode": "github-ci"}


if __name__ == "__main__":
    event = json.loads(Path(os.environ["GITHUB_EVENT_PATH"]).read_text())
    print(json.dumps(admit(os.environ["GITHUB_REPOSITORY"], event["workflow_run"])))
