# Collect Box Account-Shared Candidate

Baseline: GitGud `13a3a931735aa4dc022f841c816ee6c785006ef8`.

## Behavior

- Collect-box registrations remain in reserved pseudo-container `101`.
- Ownership moves from the selected character's `InventoryRecord` to the
  account's `AccountInventoryRecord`.
- `DNFBRIDGE_COLLECTBOX_MUTATION=reference` still performs the character-bag
  item move and account registration inside `WithinAccountCharacterItems`.
- All characters on one account read the same collection after relogin.
- No client-wire shape or PVF contract changes.

## Legacy Data Migration

The old `dnf_account_metadata.collectbox_state_v1` rows are retained. Use the
new migration tool while the game server is stopped:

```powershell
$env:DNF90_MYSQL_DSN = '<dnf_local DSN>'
go run ./cmd/tools/collectbox-account-migrate
go run ./cmd/tools/collectbox-account-migrate -apply
```

The first invocation is read-only and reports planned rows. `-apply` runs in
one transaction and:

- inserts legacy registrations into `dnf_account_inventory_items`;
- writes audit extras for box index and slot index;
- treats an identical existing row as already migrated;
- aborts on a target-key item conflict;
- leaves `collectbox_state_v1` untouched for rollback.

## Verification

- `go test ./internal/modules/dnf/collectbox`
- `go test ./internal/services/dnfbridge -run Collectbox -count=1`
- `go test ./internal/modules/dnf/... -count=1`
- `go test ./internal/modules/dnf/repository/mysql ./internal/modules/dnf/repository/memory`
- `go vet ./internal/modules/dnf/collectbox ./internal/services/dnfbridge ./cmd/tools/collectbox-account-migrate`
- `go build ./cmd/server/dnf90`

The full `dnfbridge` package still has two pre-existing guild-test failures.
They reproduce unchanged on baseline `13a3a931`.

## Boundaries

- Candidate only; not deployed and not tested in game.
- Collect-box withdrawal remains disabled exactly as on the baseline.
- The migration must run with the game server stopped and a fresh database
  backup.
