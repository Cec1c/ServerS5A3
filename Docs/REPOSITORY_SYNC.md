# GitHub / GitGud fork synchronization

GitHub Actions can keep a GitHub repository and one writable GitGud fork on the
same `master` commit. It does not push to the original author's repository.

Set the repository variable `GITGUD_SYNC_REPOSITORY` to the fork SSH URL, for
example `git@ssh.gitgud.io:YOUR_NAMESPACE/YOUR_FORK.git`. Without this variable the sync job
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
the synchronizer never executes fetched code. `Verify` is manual-only; ordinary
pushes, PRs and mirror runs do not automatically build binaries or images.

Run the integration tests with:

```text
python -m unittest discover -s scripts/ci -p "test_*.py" -v
```

The Python synchronizer defaults to a dry run. Use `--apply` only for the intended
two writable repositories. Repository synchronization does not synchronize issues, merge
requests, settings, or secrets. Client Release assets use the separate workflow
described in [客户端 Release 同步与上线门槛](CLIENT_RELEASE_SYNC.md).

## Receiving original upstream updates

The separate **Upstream updates** workflow checks the original repository on
manual dispatch and twice an hour (GitHub may delay schedules). Set the repository
variable `GITGUD_UPSTREAM_REPOSITORY` to its public HTTPS Git URL, such as
`https://gitgud.io/ORIGINAL_NAMESPACE/PROJECT.git`. Without this variable the
workflow is disabled. Keep the Actions default token permission read-only and
enable **Allow GitHub Actions to create and approve pull requests** in repository
Actions settings; only the prepare/promote jobs request write permissions.

Updates are proposed from `automation/gitgud-upstream` in a PR against `master`.
By default, source tests, isolated MariaDB transaction tests, Windows checks and
Linux image builds run against GitHub's exact merge commit. Set the repository
variable `UPSTREAM_SYNC_ONLY` to `true` to skip all of those jobs and synchronize
code only. This mode does not claim that incoming source has passed tests.
Validation jobs, when enabled, have read-only repository permissions and receive
no mirror SSH key or deployment secrets. The final job uses the trusted base
version of the promotion script, rechecks the PR, base/head and merge commit,
then fast-forwards `master` to that exact candidate and explicitly dispatches
**Repository sync**. Neither mode publishes releases or deploys a server.

| Upstream update | Result |
| --- | --- |
| Already included in master | No new PR or write |
| Conflict-free and all required jobs pass | Merge the tested commit and synchronize the fork |
| Conflict-free with `UPSTREAM_SYNC_ONLY=true` | Merge the exact candidate and synchronize; no source tests or builds |
| Merge conflict or failed validation | Keep PR open; no automatic merge |
| Changes under `.github/`, `scripts/ci/`, `.gitlab/`, or `.gitlab-ci.yml` | Keep PR open for manual CI review |
| Base/head moves during validation | Stop; validate a fresh candidate on the next run |
| Upstream or automation branch history diverges | Stop without force-pushing |

Do not repurpose the automation branch for manual development. To resolve a
conflict, merge the upstream changes into `master` through a reviewed topic PR;
the automation branch should continue to track the original upstream history.
Branch protection must permit the promotion job's normal push, or a human must
merge the validated PR according to that protection. No server is automatically
deployed. CI does not include proprietary PVF/client resources, and a passing
workflow does not establish native game compatibility.

## GitGud forks without runners

GitGud CI is independent of the GitHub synchronization jobs. If the fork has no
eligible runner, disable its **CI/CD** project feature under **Settings → General
→ Visibility, project features, permissions** (API: `builds_access_level=disabled`).
Cancel existing pending/running pipelines first. Keep the repository, SSH deploy
key, merge requests and branch permissions enabled; those are needed for Git
synchronization and contributions. This setting affects only that fork and
survives upstream changes to `.gitlab-ci.yml`; do not disable the original
author's project or remove their CI file to work around a fork runner shortage.

For automatic code synchronization only, combine that GitGud project setting
with `UPSTREAM_SYNC_ONLY=true`. `Verify` remains available through manual
**Run workflow** when a deliberate build/test is wanted. To restore validated
upstream intake, remove the variable or set it to `false`; re-enable GitGud CI/CD
only after an eligible runner is available.

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
