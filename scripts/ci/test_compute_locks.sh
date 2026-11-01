#!/bin/sh
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
work=$(mktemp -d)
trap 'rm -f "$work/compute.lock" "$work/slot0.lock" "$work/slot1.lock" "$work/events" "$work/job"; rmdir "$work"' EXIT HUP INT TERM
touch "$work/compute.lock" "$work/slot0.lock" "$work/slot1.lock"
export CI_COMPUTE_LOCK_DIR="$work" EVENTS="$work/events"
cat > "$work/job" <<'EOF'
#!/bin/sh
# Only two workers start together; the heavy worker must observe both finished.
echo "$1 start" >> "$EVENTS"
sleep 1
echo "$1 end" >> "$EVENTS"
EOF
sh "$root/scripts/ci/run_compute.sh" build sh "$work/job" first >/dev/null & a=$!
sh "$root/scripts/ci/run_compute.sh" build sh "$work/job" second >/dev/null & b=$!
wait "$a"; wait "$b"
test "$(head -n 2 "$EVENTS" | cut -d ' ' -f 2 | tr '\n' ' ')" = 'start start '
exec 6>>"$work/compute.lock"
flock -s 6
sh "$root/scripts/ci/run_compute.sh" heavy sh "$work/job" heavy >/dev/null & h=$!
sleep 1
test "$(wc -l < "$EVENTS" | tr -d ' ')" = 4
flock -u 6
wait "$h"
test "$(tail -n 2 "$EVENTS" | tr '\n' ' ')" = 'heavy start heavy end '
if sh "$root/scripts/ci/run_compute.sh" heavy false >/dev/null; then exit 1; fi
sh "$root/scripts/ci/run_compute.sh" build true >/dev/null
echo 'Compute parallel slots, heavy exclusion and failure release passed'
