#!/bin/sh
set -eu
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
GO_EXE=${GO_EXE:-go}
GOOS=${1:-$("$GO_EXE" env GOOS)}
GOARCH=${2:-$("$GO_EXE" env GOARCH)}
test "$#" -le 2 || exit 2
case "$GOOS" in windows|linux|darwin) ;; *) echo "Unsupported GOOS: $GOOS" >&2; exit 2;; esac
case "$GOARCH" in amd64|arm64) ;; *) echo "Unsupported GOARCH: $GOARCH" >&2; exit 2;; esac
export GOOS GOARCH
export CGO_ENABLED=0
EXT=
test "$GOOS" != windows || EXT=.exe
OUTPUT="$ROOT/dist/$GOOS-$GOARCH"
mkdir -p "$OUTPUT"
cd "$ROOT/go-server"
"$GO_EXE" build -buildvcs=false -mod=readonly -trimpath -o "$OUTPUT/DNF90Server$EXT" ./cmd/server/dnf90
"$GO_EXE" build -buildvcs=false -mod=readonly -trimpath -o "$OUTPUT/DNF90Control$EXT" ./cmd/server/control
"$GO_EXE" build -buildvcs=false -mod=readonly -trimpath -o "$OUTPUT/DNF90Doctor$EXT" ./cmd/server/doctor
printf 'Build succeeded: %s\n' "$OUTPUT"
