#!/bin/sh
# Shared runners may bind one host lock file into every job. Only the artifact
# step is serialized; compilation and core tests can use both slots.
set -eu
if [ -z "${CI_ARTIFACT_LOCK_PATH:-}" ]; then
  exec go run scripts/ci/artifacts/upload.go
fi
command -v flock >/dev/null 2>&1 || { echo 'artifact lock requires flock' >&2; exit 1; }
test -f "$CI_ARTIFACT_LOCK_PATH" || { echo 'shared artifact lock is missing' >&2; exit 1; }
started=$(date +%s)
exec 9>>"$CI_ARTIFACT_LOCK_PATH"
flock -w "${CI_ARTIFACT_LOCK_TIMEOUT:-600}" 9 || { echo 'artifact lock timed out' >&2; exit 1; }
echo "Artifact lock wait seconds: $(($(date +%s) - started))"
exec go run scripts/ci/artifacts/upload.go
