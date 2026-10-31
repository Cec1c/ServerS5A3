# Personal quest materials after a shared combat death

## Problem and scope

The room accepts one authoritative monster death. Previously only its reporting
session reached the type-1 personal quest-material path. Other party members
received the death notification or replay, but never evaluated their own quest
rules. A 100% rule therefore still produced only one personal settlement.

Combat-only/tutorial compositions without a repository provider retain their
existing behavior. A configured repository or SQL error is never treated as
permission to bypass durable preparation.

This fix covers both `[monster reward item]` and type-1 `[enemy reward item]`
through the existing matcher. It contains no quest-ID exceptions. Ordinary
equipment, gold, ground-item ownership, APC/type-2, passive/type-3, and quest
completion rewards keep their existing owners and packet layouts.

## Durable decisions and personal settlement

1. Freeze recipients from the shared-instance scene registrations and current
   character bindings. Require the same room, map, and announced object binding.
   A later join cannot become a recipient of an earlier report.
2. Validate the authoritative death's state and scene-owner proof. Serialize
   death commits with a dedicated mutex, releasing the room state mutex over
   database work. No peer dungeon mutex or blocking cross-session queue wait is
   acquired.
3. In one preparation transaction, persist each recipient's matched rules and
   independent random results. A random room nonce plus object key, enemy code,
   and character ID identifies a decision. Repeated reports or ambiguous commits
   reload the original decision instead of rolling again. Invalid reports and
   forced boss cleanup do not create these decisions.
4. Reuse the character settlement unit of work for inventory and the durable
   delivery receipt. Check the authenticated account/character owner and the
   quest's active generation again. Enforce each character's current holding
   cap independently. Split complete grants across stacks; never acknowledge a
   silently truncated grant.
5. Queue peer delivery nonblockingly after the normal death notification and
   before the existing final-room completion dispatch. The reporter retains
   its previous material-refresh position in the packet sequence.
6. Recover the persisted inbox on character bootstrap and every five seconds on
   that character's event queue. A disconnected or full-queue session does not
   own the reward identity. No offline email or compensating reward is added.

Preparation failure leaves the room death uncommitted. A committed preparation
is the durable acceptance of an already validated combat report; its decisions
can survive a lost acknowledgement or process exit before the in-memory death
projection finishes. Inventory settlement failures affect only the corresponding
character, and committed members are never rolled back to retry another member.

## Persistence and recovery boundaries

No schema migration is required. The existing Settings/setting-values stores
hold `character:<id>:quest_material:<room nonce>` event journals and the
`character:<id>:quest_material:pending` room index. The namespace is explicitly
allowed only for that character in character settlements. Preparation exposes
only the frozen recipients' Quest and material Settings repositories, not assets.

An opaque `quest_material_generation_v1` value is lazily added to the matched
active quest's Extra map. Normal abandon/reaccept and daily reaccept construct a
new state, so an old pending decision cannot reward a new quest acceptance.
Existing active tasks need no reset. Active quest planning and optimistic delta
writes preserve completed history and unrelated Extra fields.

Completed decisions, probability misses, cap outcomes, and invalidated quest
generations remain durable tombstones. Notification receipts are separate from
asset receipts: a failed send retries an inventory snapshot, not the grant.
Acknowledgement identifies the exact delivered notices.

Full inventory preserves the original decision without partially applying that
event. Recovery rotates unresolved rooms so a blocked room cannot starve later
deliveries. Work is bounded to four rooms per recovery transaction, 128 pending
rooms per character, and 4096 events per room journal. Capacity or corrupt/missing
journals fail closed without age-evicting unresolved results. Transaction retries
are bounded to eight attempts and the caller's deadline.

Completed room journals are persisted, not accumulated in a process-global
cache. This patch deliberately does not delete them automatically. Any future
retention policy must preserve live/replayable room identities and unresolved or
ambiguous receipts. Rolling back the binary must not delete these records.

## Validation

From `go-server`:

```text
go test ./internal/modules/dnf/questmaterial ./internal/modules/dnf/dungeon ./internal/modules/dnf/quest ./internal/modules/dnf/repository/...
go test ./internal/services/dnfbridge -run PartyQuestMaterial
go test -race ./internal/modules/dnf/questmaterial ./internal/services/dnfbridge -run 'PersonalMaterial|PartyQuestMaterial'
go vet ./internal/modules/dnf/questmaterial ./internal/modules/dnf/repository/... ./internal/services/dnfbridge
CGO_ENABLED=0 go build -trimpath -tags timetzdata ./cmd/server/dnf90
```

The MariaDB test requires `DNFBRIDGE_PLAYER_TEST_DSN`, refuses endpoints other
than disposable `127.0.0.1:34323`, creates a uniquely named scratch schema, and
drops only that schema. It verifies real rollback, commit-then-error recovery,
concurrent consumers, and reload through a fresh pool. Missing SQL credentials
are an explicit skip, never SQL acceptance.

Handler tests use the official-24.9 88-byte death report and synthetic PVF
rules/placements. They cover solo and 2/3/4 members, leader/reporter changes,
normal/Boss targets, both type-1 rule forms, personal eligibility/caps/accounts,
independent 50%, 5%, and 1% probabilities, duplicate reports, disconnected queues, full bags,
stack splits, and concurrent session queues. These tests do not constitute
native-client party acceptance or a production deployment.
