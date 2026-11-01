#!/bin/sh
# Four scheduling slots need not mean four simultaneous large compilers.
set -eu
mode=$1
shift
case "$mode" in
  heavy) export GOMEMLIMIT=3GiB GOFLAGS=-p=1;;
  build) export GOMEMLIMIT=1800MiB;;
  *) echo 'invalid compute class' >&2; exit 1;;
esac
if [ -z "${CI_COMPUTE_LOCK_DIR:-}" ]; then exec "$@"; fi
command -v flock >/dev/null 2>&1 || { echo 'compute gate requires flock' >&2; exit 1; }
for name in compute.lock slot0.lock slot1.lock; do
  test -f "$CI_COMPUTE_LOCK_DIR/$name" || { echo 'shared compute lock missing' >&2; exit 1; }
done
exec 7>>"$CI_COMPUTE_LOCK_DIR/compute.lock"
started=$(date +%s)
timeout=${CI_COMPUTE_LOCK_TIMEOUT:-1800}
bounded_lock() {
  while ! flock -n "$1" 7; do
    if [ "$(($(date +%s) - started))" -ge "$timeout" ]; then echo 'compute gate timed out' >&2; return 1; fi
    sleep 1
  done
}
case "$mode" in
  heavy) bounded_lock -x;;
  build)
    bounded_lock -s
    while :; do
      exec 8>>"$CI_COMPUTE_LOCK_DIR/slot0.lock"
      if flock -n 8; then break; fi
      exec 8>>"$CI_COMPUTE_LOCK_DIR/slot1.lock"
      if flock -n 8; then break; fi
      if [ "$(($(date +%s) - started))" -ge "$timeout" ]; then echo 'compute slot timed out' >&2; exit 1; fi
      sleep 1
    done;;
esac
echo "Compute gate class=$mode wait_seconds=$(($(date +%s) - started))"
exec "$@"
