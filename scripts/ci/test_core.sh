#!/bin/sh
set -eu
sh scripts/check-server-boundary.sh
cd go-server
go list -buildvcs=false -mod=readonly ./...
# Keep Go's content-addressed test cache. Integration/freshness investigations
# can opt out explicitly without making every ordinary regression uncached.
set --
if [ "${GO_TEST_UNCACHED:-0}" = 1 ]; then set -- -count=1; fi
go test -buildvcs=false -mod=readonly "$@" ./internal/modules/dnf/...
go test -buildvcs=false -mod=readonly "$@" ./internal/services/dnfbridge
go test -buildvcs=false -mod=readonly "$@" ./cmd/server/control
go vet -mod=readonly ./cmd/server/control ./cmd/server/dnf90 ./cmd/server/doctor ./internal/app/dnf90 ./internal/services/dnfbridge
