# Repository changelog

## 2026-10-11 — D33 candidate repairs and user acceptance

- Record user acceptance of original groups 1/2/3 and additional item 3. Production remains r98.
- Fix Vagabond support-weapon placement and integrate reviewed Luke route, prerequisite, material-drop and dual-boss candidates.
- Preserve historical source manifest; add current-source manifest and D33 validation/acceptance boundaries.
- Validate domain/bridge/control, real-PVF cases, vet and Windows/Linux builds; retain prior failure evidence.


## 2026-10-11 — Luke D33 candidate review (Luke source/test only)

- Review the Luke light/dark route owner, ordinary Mechanical Throne prerequisites, dual-boss clear gate and raid material filter against the real runtime Script.pvf.
- Confirm PHASE-2 `[LOCKOUT TYPE]` 4/5/0 = light/dark/shared: a route commits only on accepted entry, survives rejoin and group move, resets per epoch, and shared energy dungeons are never gated.
- Confirm `map/luke_normal/light_altar/55946.map` (dungeon 5104) spawns two bosses 56699/56698, so the >1 blocking-boss gate blocks the premature clear; dark altar 55953 keeps a single boss.
- Confirm the four Luke raid materials 10159142/10159143/10159623/10159649 are `[material]` and covered by the filter; light-altar essence 10159404 is `[quest]` and stays outside it.
- Document limitations without inventing rules: Mechanical Throne admission uses the persisted lifetime `DungeonPermission.ClearState` because sequence 42 and `etc/` carry no reset/day evidence; the material filter suppresses all `[material]` in the multiplayer Luke raid, which is broader than the named materials.
- Add `dungeon_luke_review_regression_test.go` and the limitation comment in `dungeon_luke_normal_rules.go`. Changed scope stays Luke source/test; no deployment, database or runtime asset change.

## 2026-10-11 — Online repository acceptance handoff

- Record source import commit 22960444f648a4f2f5bcc7b8a3fbe7086eebc5cc as merged into master.
- Add Docs/ACCEPTANCE_HANDOFF.md with previously accepted items, exact acceptance boundaries and merged source status.
- Preserve all pending GUI items and r98 deployment/paused-task boundaries; no new code or runtime changes.

## 2026-10-11 — Initial Go server source import

- Import complete 3,356-file D22/D23 source closure without changing source bytes.
- Add Windows BAT and POSIX shell build entrypoints with ignored dist outputs.
- Add Linux/Windows amd64 GitLab builds and core tests; preserve SAST include.
- Include example configuration with placeholder password; exclude runtime assets, credentials, player data and binaries.
- Record candidate/online version boundaries and pending acceptance in Docs/SOURCE_IMPORT.md.
- Build organization reference: rewio/ServerS4A21 commit 1f4a4e80610c767278008cd666588cb9a9629e34.
