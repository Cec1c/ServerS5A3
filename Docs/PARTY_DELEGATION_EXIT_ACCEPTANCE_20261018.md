# !86 delegated leader and member-exit acceptance

Date: 2026-10-18

The user confirmed both requested native checks for the party fix:

- After delegating leadership, the party interface no longer flickers.
- A delegated member can use Esc to leave the party inside the dungeon, returns to town, and is no longer in the party; the remaining member keeps the dungeon session.

This candidate carries only the public party scope from the r69 net repair. Delegation keeps each member's assigned slot and party ID, the leader identity remains separate from member slots, and learned relay/UDP endpoints survive repeated host changes. An active member op42 giveup leaves the party only after the town transition commits, with the current character generation and original party checked before removal. The patch excludes trade, GM, deployment records and unrelated private cumulative changes.

The public MR base was compared with r69 fixed-slot snapshot `6c3849752dbb08760e38fe5f4fc9a2477980bf6e`. Among the requested party files, only `party_online.go` already differs (`+12/-8`); that snapshot does not contain `dungeon_party_member_exit.go` or `party_round69_regression_test.go`. The public base also lacks the private `currentSceneActorRoute` helper and its three-argument op9 removal builder, so `dungeon_party_member_exit.go` uses the existing public two-argument `buildCurrentSceneOp9ActorRemovalBodyInContext`. No private actor-route helper was imported.

Validation with the fixed PVF `E:/90/mr-deployment-20261018/validation/bat-preflight/project/runtime/data/dnf/Script.pvf` (SHA256 `A56CACF96087BA34CA5299E04AB65F8268F694B214B97DEC9879FDEBF46F5290`):

- `go test -buildvcs=false -count=1 ./internal/modules/dnf/party ./internal/services/dnfbridge -run 'Party|ChangeHost|MemberGiveup|Round69'`: 560 passed, 0 failed, 0 skipped; 388 top-level tests passed.
- `gofmt -l` over the ten changed party files: no output.
- `git diff --check`: exit 0.

The earlier build-only failure and its public-base adaptation are retained in the r70 validation evidence. Full integration checks remain with the parent merge train.
