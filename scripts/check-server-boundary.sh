#!/bin/sh
set -eu

# Check the tracked tree, not local ignored tools that the owner keeps separately.
blocked=$(git ls-files -- go-server/internal/services/gmtool \
  go-server/internal/services/dnfbridge/gmtool_service.go \
  go-server/internal/services/dnfbridge/gm_administrative_membership.go gm-tool gmtool)
if [ -n "$blocked" ]; then
  echo "GM tool content must remain outside the online server repository:" >&2
  echo "$blocked" >&2
  exit 1
fi

cd go-server
dependencies=$(go list -buildvcs=false -mod=readonly -deps ./cmd/server/dnf90)
printf '%s\n' "$dependencies" |
while IFS= read -r dependency; do
  case "$dependency" in
    */services/gmtool|*/services/gmtool/*)
      echo "Server must not depend on the GM tool: $dependency" >&2
      exit 1
      ;;
  esac
done
