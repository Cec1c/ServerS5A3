#!/bin/sh
set -eu
sh scripts/check-server-boundary.sh
cd go-server
go list -buildvcs=false -mod=readonly ./...
go test -buildvcs=false -mod=readonly -count=1 ./internal/modules/dnf/...
go test -buildvcs=false -mod=readonly -count=1 ./internal/services/dnfbridge
go test -buildvcs=false -mod=readonly -count=1 ./cmd/server/control
go vet -mod=readonly ./cmd/server/control ./cmd/server/dnf90 ./cmd/server/doctor ./internal/app/dnf90 ./internal/services/dnfbridge
