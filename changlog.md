# Repository changelog

## 2026-10-12 — Docker Linux profile and local token launcher

- Add a separate Linux image and generated Compose profile under ignored runtime/docker, with a dedicated MariaDB volume, private interface bindings, embedded UDP relay and timezone data. Extract real PVF channel assets with a source-built tool and preserve explicit external table overrides.
- Bind the embedded UDP relay to gameListenHost when configured while preserving the advertised host address and the legacy default; cover NAT address separation and invalid bind addresses.
- Add BAT/Python local-client entrypoints for the inspected v56 executable, API-issued login tokens, duplicate-client and asset-hash checks. Keep the vendor updater and binaries unchanged.
- Verify actual Linux server/database startup and local account registration/login. Client GUI compatibility and token binding remain under investigation; a successful API response alone does not prove in-game login.
- Complete live fast-forward synchronization in both directions: GitHub Actions runs 38162071646 (GitHub to GitGud) and 38162153930 (GitGud to GitHub). Linux Verify passed after portable controller path fixes (38161846104).

## 2026-10-12 — Portable controller path validation

- GitHub's first Linux run reproduced two existing controller failures: backslash traversal was accepted in an installation receipt and Windows absolute assets bypassed the expected path guard.
- Normalize both separator styles and reject drive-relative/absolute Windows paths and UNC paths on every host. Retain Windows behavior and add cross-platform escape cases; do not skip the failing checks.

## 2026-10-12 — Optional GitHub / GitGud fork synchronization

- Add an opt-in GitHub Actions synchronizer for one master branch, using only fast-forward pushes and an independently scoped GitGud Deploy Key. Stop on divergent history or imported changes to CI definitions.
- Add a read-only verification workflow for Go checks, Linux/Windows builds and synchronization tests, including checks after token-driven synchronization.
- Validate seven integration cases with temporary bare repositories: equality, dry run, both directions, divergence, CI-change rejection and missing branches. Remote workflow results are recorded separately after publication.

## 2026-10-12 — Anton expired completed-map return across phases

- Preserve an already-owned first-phase expired-completed return receipt after the same Anton instance advances to the real second phase; retain exact run/account/channel/participant guards and town-only permission.
- Reproduce both op42/op72 failures before the patch with the real PVF phase VM, then cover phase advance, foreign instance rejection and unchanged retry/reward gates. Keep native phase-return root cause and GUI acceptance open.
- Complete final27 domain/bridge/control, scoped real-PVF, go list, scoped vet and Windows/Linux builds; preserve historical broader PVF failures and asset skip. Production stays r98 and QA09 retained.


## 2026-10-12 — Anton volcano native barrier state and adventure friend presence

- Derive native Anton symbol111 from full barrier depletion/recovery after verifying the shipped client controller. Keep it latched during partial recharge and restore it in reconnect snapshots; preserve ordered110/111/126 and110/111/127 writes without altering formal PVF markers. Exclude phase1 and Luke.
- Verify unmodified Go-exported break/recovery frames through actual v96/v97 native receive/583/store/UI111 branches; confirm AC and cutscene selection with explicit external fixtures, retain failed harness attempts, keep GUI/damage acceptance open.
- Verify real-PVF cross-squad delivery and actual server16 frames; add low-frequency successful-send diagnostics. Keep native animation and monster damage acceptance open; user reproduces on daily r98.
- Filter adventure-group friends to verified online characters; persist last-selected identity through the existing metadata entry and use per-account bind-generation ordering to prevent stale writes. Verify offline cold read and account isolation; native UI remains pending.
- Validate8227 core PASS/0 FAIL/736 environment SKIP and272 scoped real-PVF PASS/0 FAIL/1 separate-asset SKIP, scoped vet and Windows/Linux builds. Preserve the broader PVF run's seven assertion failures and timeout; reproduce all seven on the prior source baseline. Keep retained-QA port preflight failure explicit.
- Synchronize3383 source files to maintenance and current manifest. Production remainsr98, QA09 retained, computer control paused until2026-10-12 01:30 HK.

## 2026-10-11 — D33 raw declaration guards and CI runner diagnosis

- Reject ten unproved operation tags by raw section presence, including empty/symbol/duplicate/orphan declarations; retain atomic failure and legal floor1 controls. Independent B1 review passed.
- Final11 measured 1,058 supported/plannable level90 recipes, including273 floor recipes. Four former unresolved outputs declare empty item-category sections and now fail the raw guard; preserve1062/1058 as historical measurement.
- Validate8,220 core PASS/0 FAIL/725 environment SKIP,84 real-PVF PASS/0 FAIL/0 SKIP, go list, scoped vet and Windows/Linux builds; preserve final10 assertion failure.
- Record user acceptance of one2659502 necklace craft, retain remaining recipe/experience/cold-login work and UI pause until2026-10-12 01:30 HK. Production staysr98.
- Diagnose pipeline164381: both builds failed stuck_pending_no_matching_runners before script execution. Enable project shared runners; sole instance runner remains paused/offline. Keep CI gates and record the external runner dependency.

## 2026-10-11 — D33 bounded crafting floor and source qualifications

- Permit strict making-level1 in the eleven proved client categories; validate raw PVF shape instead of trusting the catalog projection. Preserve rejection for higher levels, skill gates, explicit rates and unknown semantics.
- Check ordinary drawing level in the owner transaction, reject restricted or malformed job fields, and conservatively reject all 39 drawings declaring unproved date semantics. Retain zero-count material audit and shared-asset rollback.
- Measure 1,062 supported / 1,058 plannable recipes at level90, including 273 floor recipes; preserve historical counts and avoid claiming native acceptance from catalog coverage.
- Validate final09: 8,097 core passes with 725 environment skips, 84 real-PVF passes without skips, scoped vet, go list, maintenance preflight and Windows/Linux builds. Keep native floor crafting and experience progression pending; production remains r98.

## 2026-10-11 — D33 native Vagabond checkpoint

- Verify one isolated level90 Vagabond support weapon through three native equip/unequip moves, unchanged item instance and durability on bag readback, and a new native client process retaining support slot24.
- Preserve combat and post-combat persistence as pending; additional item12 remains open. Shut down only the owned QA client/server and retain its database. Production remains r98.

## 2026-10-11 — D33 recipe settlement and observed native routes

- Read equipment design recipes from real PVF; fail closed for unproven rates, skill/itemmaking gates, upgrade tables and alternative source semantics.
- Settle supported drawings, character materials, account warehouse materials, gold and outputs atomically through the existing owner; reject historical character copies of account assets.
- Register observed native legacy op25 and op65 routes alongside their proved Upper owners. Refresh recipe inventory with op13 after commit; do not invent 119-byte reward fields.
- Retain rollback, locked/corrupt asset, overflow, shared refresh, cold read and native dispatch regressions; validate domain/bridge/control, real PVF, scoped vet and Windows/Linux builds (final07).
- Record isolated PID-bound native town entry and bounded recharge/diamond window recovery; verify one native 5003 craft and client cold relaunch; keep other recipes/failure paths, membership/payment, Luke and equipment acceptance open. Production remains r98; isolate the joust catalog workaround.

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

## 2026-10-11 — D33 latest user acceptance and volcano priority

- Close original groups5/12 on direct user confirmation; preserve groups1/2/3 and additional3. Keep original4/9 untested and original11 unreproduced.
- Prioritize Anton phase-two black-volcano barrier remaining at0%; separately track phase-one settlement return, squad board occupancy, adventure friend character selection and temporary-town/abyss admission.
- Preserve r98 deployment and UI pause until2026-10-12 01:30 HK; source tests do not prove native physical barrier removal.
