# GitHub / GitGud fork synchronization

GitHub Actions can keep a GitHub repository and one writable GitGud fork on the
same `master` commit. It does not push to the original author's repository.

Set the repository variable `GITGUD_SYNC_REPOSITORY` to the fork SSH URL, for
example `git@ssh.gitgud.io:Cecic/servers5a3.git`. Without this variable the sync job
is disabled, including in upstream repositories and new forks.

Add a dedicated **project Deploy Key** with write access to that GitGud fork.
Store its private key in the GitHub Actions secret `GITGUD_SYNC_SSH_KEY`, and the
verified `ssh.gitgud.io` known-hosts line in `GITGUD_SYNC_KNOWN_HOSTS`. Protected
branch rules must allow this key to push. Do not reuse a developer's account key.
GitHub uses its short-lived `GITHUB_TOKEN` with `contents: write`.

The workflow runs on `master` pushes, manual dispatch, and approximately every
15 minutes. Schedules can be delayed by GitHub. Each run fetches both tips:

| Relationship | Action |
| --- | --- |
| Equal | No change |
| GitHub ahead | Fast-forward GitGud |
| GitGud ahead | Fast-forward GitHub |
| Diverged | Stop; review and merge explicitly |

No forced updates, automatic merges, branch deletion, or tag replacement are
performed. The job serializes writes and reads back both tips afterward. GitGud
changes to `.github/` or `scripts/ci/` require review and merge on GitHub first;
the synchronizer never executes fetched code. A read-only `Verify` workflow
runs after successful synchronization because `GITHUB_TOKEN` pushes do not
normally trigger another push workflow.

Run the integration tests with:

```text
python -m unittest discover -s scripts/ci -p "test_*.py" -v
```

The Python synchronizer defaults to a dry run. Use `--apply` only for the intended
two writable repositories. Keep upstream updates and contributions separate:
fetch upstream, review on a branch, and submit a GitGud merge request from the
fork branch. Repository synchronization does not synchronize issues, merge
requests, releases, settings, or secrets.

## Contributing through the fork

Keep `upstream` read-only and create a topic branch from a reviewed upstream tip.
Push that branch to the GitGud fork, then open a merge request targeting the
original project's `master`. The mirror job handles only `master`; it neither
publishes topic branches nor accepts upstream contributions automatically.

If a sync fails, inspect its Actions log before retrying. For a divergence,
fetch both remotes and resolve the changes on a review branch; do not reset
either remote to silence the failure. For a Deploy Key error, check that the
key is enabled for the fork with write permission and allowed by its protected
branch settings. A scheduled run is not guaranteed to start on the minute;
`Run workflow` can request an immediate check.
