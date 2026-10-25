#!/bin/sh
set -eu
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
WORK=$(mktemp -d)
cleanup() {
  rm -f "$WORK/go-fixture" "$WORK/calls" "$WORK/build.sh"
  rmdir "$WORK/dist/linux-amd64" "$WORK/dist/windows-amd64" "$WORK/dist" "$WORK/go-server" "$WORK" 2>/dev/null || :
}
trap cleanup EXIT HUP INT TERM
cp "$ROOT/build.sh" "$WORK/build.sh"
mkdir "$WORK/go-server"
cat > "$WORK/go-fixture" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >> "$BUILD_TEST_LOG"
EOF
chmod +x "$WORK/go-fixture"
export GO_EXE="$WORK/go-fixture"
export BUILD_TEST_LOG="$WORK/calls"
for target in linux windows; do
  : > "$BUILD_TEST_LOG"
  sh "$WORK/build.sh" "$target" amd64 >/dev/null
  test "$(wc -l < "$BUILD_TEST_LOG" | tr -d ' ')" = 3
  grep -q './cmd/server/dnf90$' "$BUILD_TEST_LOG"
  grep -q './cmd/server/control$' "$BUILD_TEST_LOG"
  grep -q './cmd/server/doctor$' "$BUILD_TEST_LOG"
  for part in server control doctor; do
    case "$part" in
      server) program=DNF90Server; package=dnf90;;
      control) program=DNF90Control; package=control;;
      doctor) program=DNF90Doctor; package=doctor;;
    esac
    suffix=
    test "$target" != windows || suffix=.exe
    : > "$BUILD_TEST_LOG"
    sh "$WORK/build.sh" "$target" amd64 "$part" >/dev/null
    test "$(wc -l < "$BUILD_TEST_LOG" | tr -d ' ')" = 1
    grep -Fq "/dist/$target-amd64/$program$suffix ./cmd/server/$package" "$BUILD_TEST_LOG"
  done
done
: > "$BUILD_TEST_LOG"
if sh "$WORK/build.sh" linux amd64 invalid >/dev/null 2>&1; then
  echo 'invalid component accepted' >&2
  exit 1
fi
test ! -s "$BUILD_TEST_LOG"
echo 'Build component matrix and legacy all-program build passed'
