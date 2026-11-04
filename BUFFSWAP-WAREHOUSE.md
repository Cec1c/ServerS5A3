# BUFF-Swap Personal Warehouse Candidate

Baseline: GitGud `13a3a931735aa4dc022f841c816ee6c785006ef8`.

## Behavior

- Buff-swap registration now accepts source list `2` (personal warehouse).
- Item discovery scans both `InventoryRecord.Slots` and
  `InventoryRecord.Warehouse`.
- The `native_buff_instance` identity is written back to the matching container
  using `SaveInventoryFields`.
- Relogin projection, instance migration, and item movement tracking resolve the
  same physical item across bag and warehouse.
- Existing bag, avatar, worn equipment, pet, and no-item registration paths are
  unchanged.

## Ambiguity Safety

If a historical registration has no instance identity and the same template is
present in both bag and warehouse, the binding remains persisted but is omitted
from the active projection. The candidate never guesses which copy was meant.

## Verification

- `go test ./internal/modules/dnf/buffswap -count=1`
- `go test ./internal/services/dnfbridge -run "Buff|SkillSwitch" -count=1`
- `go test ./internal/modules/dnf/... -count=1`
- `go vet ./internal/modules/dnf/buffswap ./internal/services/dnfbridge`
- `go build ./cmd/server/dnf90`

## Boundaries

- Candidate only; not deployed.
- No database, schema, PVF, client, or production service change.
- Real in-game registration from warehouse and relogin still require player
  acceptance after any separate deployment approval.
