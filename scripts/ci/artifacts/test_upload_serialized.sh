#!/bin/sh
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)
work=$(mktemp -d)
trap 'rm -f "$work/go" "$work/events" "$work/lock" "$work/first.log" "$work/second.log"; rmdir "$work"' EXIT HUP INT TERM
cat > "$work/go" <<'EOF'
#!/bin/sh
echo "start $$" >> "$EVENTS"
sleep 1
echo "end $$" >> "$EVENTS"
exit "${FAKE_STATUS:-0}"
EOF
chmod +x "$work/go"
export PATH="$work:$PATH" EVENTS="$work/events" CI_ARTIFACT_LOCK_PATH="$work/lock"
touch "$work/lock"
sh "$root/scripts/ci/artifacts/upload_serialized.sh" >"$work/first.log" &
first=$!
sh "$root/scripts/ci/artifacts/upload_serialized.sh" >"$work/second.log" &
second=$!
wait "$first"
wait "$second"
test "$(cut -d ' ' -f 1 "$EVENTS" | tr '\n' ' ')" = 'start end start end '
test "$(sed -n '1s/start //p' "$EVENTS")" = "$(sed -n '2s/end //p' "$EVENTS")"
# A failed uploader must propagate its exit code and release the OS lock.
export FAKE_STATUS=7
status=0
sh "$root/scripts/ci/artifacts/upload_serialized.sh" >/dev/null || status=$?
test "$status" = 7
unset FAKE_STATUS
sh "$root/scripts/ci/artifacts/upload_serialized.sh" >/dev/null
rm "$work/lock"
if sh "$root/scripts/ci/artifacts/upload_serialized.sh" >/dev/null 2>&1; then
  echo 'missing shared lock accepted' >&2; exit 1
fi
echo 'Artifact serialization and failure release passed'
