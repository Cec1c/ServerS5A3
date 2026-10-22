# Readiness v2: raid-specific result eligibility

2026-10-22. Offline candidate; independent main-thread verification completed.
No commit or deployment performed by this worker. This worker did not write V1's
builder, verifier, README or `0bc04317` candidate. V2 adds only
`build_readiness_v2.py`, `verify_readiness_v2.py`, this review, and external
artifacts under `H:/codex-work/20261022/r195/resume/a02-readiness-v2/`.

**Not full A02/GUI acceptance.** V2 protects the tested network sequence
`601 eligible -> 5874 -> intervening tick with no party/scene -> wait -> kind3 ->
native state4`. It does not close the indirect pre-601 metadata/UI path described
below, or prove full rendering and online object lifetime.

## Input, output and evidence

- Exact input remains local-face, not v1 readiness:
  `8ab4297d1e3ebe2dc9d71801ebae210cb3ec673dbcdf707b35a3d5c8faf6f790`.
- Output `DNF-r195-readiness-v2.exe`, 111874048 bytes:
  `d41d6c4392182fa55327c2284e55797c06a73ae1a98703c876b126af56bb1203`.
- Private `.rdyrx` RX code is 889 bytes; `.rdyrw` RW record is 44 bytes. Both
  sections occupy 4096-byte pages. No native UI scratch field or RWX section.
- `build-v2.json`, `verify-v2.json`, `helper-v2.asm` and
  `native-anchors-v2.json` are external. Verifier has 72 passing bounded cases.
- Read-only design review is `resume/a02-readiness/eligibility-design-review.md`.
- Native field evidence: main-thread `result-eligibility-native.json` (12 cases)
  and its parser verifier. Server order evidence:
  `resume/a02-result-eligibility-order.md`.

## Four hooks

The three v1 sites retain their original scope: network 5874 `2001639`, entire
600 loop exit `202FCC8`, and local state3 transition CALL `192F6ED`. There is no
hook on generic flag setters, `192FAE0`, model clearing or team page construction.

The fourth hook is a six-byte JMP/NOP replacement at `201DCBF` of
`mov ecx,[56A37D0]`. It saves flags and all integer registers, reads parsed
`[ebp-64]` result, `[ebp-5B]` phase, `[ebp-51]` option, then restores and executes
the exact relocated MOV before returning to `201DCC5`. DCC5's original
`add esp,28h` cleans the pending parser arguments. No field depends on hook ESP
offsets. Native option SETE and `2678190` execute unchanged even for failed or
invalid results. The patch never reads generic manager+360 as a qualification.

Every new 601 first sets active outer/fresh-model/disposition to zero and clears
the previous result record. It then obtains current raid manager/key, rejects
key=-1, and calls the original `1A553E0`. This getter either returns the cached
raid or calls native `1A55350` map lookup and fills manager+8. It is not claimed
to be a zero-write operation: that native cache write is explicitly allowed.
The helper rechecks global manager, key and manager+8 after the call before
binding the returned object. It never dereferences a retired result token.

Only raid+24==0 is captured. The compact record contains:

| Offset | Meaning |
|---:|---|
| 0,4,8,12 | Active outer, manager, cached raid, current key |
| 16,20 | Active fresh model, active disposition |
| 24,28,32 | Captured result manager, cached raid, current key |
| 36,40 | Raw result phase, result disposition |

Disposition is 0 unknown/unbound, 1 eligible, 2 denied, 3 invalid-bypass.
Result0 + phase0/1 + option0 gives 1; the same with option1 gives 2.
An identified current Anton result with failed!=0 or invalid phase/option gives
3, which is **not a valid reward qualification**. It suppresses waiting rather
than falling back to old party ownership. Other mode, absent manager/key or map
miss leaves no bound result; a later cache recovery cannot adopt a partial
capture. Unknown retains v1 behavior and is not claimed fixed for such recovery.

Every network5874 clears active freshness/disposition, binds actual outer and
copies the retained result disposition only when all three identity tokens match.
It does not consume/delete the result record. Repeated same-stage 5874 may have
no new 601; retaining the record is required by the current server contract.

After all original arg4/state3/skip428/model+4==1/current Anton identity checks,
gate precedence is: eligible -> fresh same-model plus category2 row0; denied or
invalid-bypass -> original call; unknown -> v1 cached-local-actor and native
category0 ownership checks. Eligible requires no scene manager, local actor or
party data. Other modes and arg5 are preserved. Freshness still requires a
nonempty kind3 after the whole block loop, before the window gate. No team page
rebuild/replay, award change or packet alteration is introduced.

## Stage and pointer boundaries

New 601 invalidates old active state before 587 binds again. This handles changed
phase with the same manager/key/raid/model pointers when the new result arrives.
The saved phase byte is metadata; it is not compared with reward-time raid+30,
which cannot independently identify the real result phase. Pointer/key equality
alone is not a stage generation. The verifier explicitly records that without a
new 601, reused identical tokens are indistinguishable from same-stage reuse.

The server review establishes current production network5874 callers are guarded
by a successfully written 601 for the same session/instance/phase/character/
generation; send failures close the connection. It also establishes that later
same-stage 5874 can reuse that 601 and that option1 observers legitimately receive
state4. These are server/TCP order facts, not proof of client UI scheduling or
successful packet dispatch. The fixtures explicitly insert ticks between packets.

An isolated 5874 after kind3 resets freshness and will wait for another kind3.
This unsupported sequence remains an executable counterexample, not silently
accepted using old freshness. Earlier protocol review found no normal isolated
post-kind3 network5874 producer; that constraint must remain in deployment review.
Commit failure with no kind3 may continue waiting. Exit key=-1/cache=0 or changed
current identity releases the gate, including known-eligible paths.

**Pre-601 metadata remains open.** Main-thread evidence says rejoin sends 591
mode2, handler `2024897`, `1A5CA60` writes raid state/argument, then `1A624D0` is
effectively empty and `1A618E0` refreshes roster/listeners. There is no direct
open call on the traced chain, but `1A61B98` listener vtable+0c is not fully
resolved. Only the network587 boundary is claimed here. V2 does not claim that
591-before601 cannot indirectly alter the reward outer.

## Validation and remaining fixtures

72 cases execute installed helpers and native instruction slices. They cover:

- Actual ten-byte 601 parser + capture + original generic setter, baseline versus
  patched registers/flags/ESP, including pending parser arguments at the hook;
  valid, denied, failed and out-of-contract values. Failed/invalid paired with old
  party ownership do not wait. A new invalid601 also revokes old eligible/fresh.
- Original `1A553E0` cached hit, uncached map hit with cache write, map miss, and
  post-lookup manager/key recheck. Only its map leaf is a fixture; native getter
  branch/call/store/return instructions really execute.
- Both result phases; capture before any outer exists; eligible with no scene
  or no local actor and no party; waiting tick followed by kind3 -> native state4.
- Denied overriding old ownership types1/2; newphase same-pointer replacement;
  repeated587 reusing result but clearing fresh; failed/invalid/othermode clearing
  old result; missing-cache recovery without a new601 stays unknown.
- Native 600 loop tails for 1/2/4 blocks, closed-window kind3, other kinds and zero
  blocks, row0 missing, reverse ordering, arg5/othermode/skip passthrough, native
  manager exit prefix and original 32B6530 closed/open/special branches.
- Independent byte exclusions, four installed branch targets, RX/RW section
  headers, code/data/image sizes, checksum, appended bytes and 18 native anchors.
  Hook writes are constrained to stack/private data plus original generic setter
  and native manager cache. No model/scene allocations or UI scratch writes.

The initial verifier run hit a Python name collision with the immutable v1
Harness's `capture` attribute; v2 method was renamed `receive601`. Candidate
instructions were unchanged. Final verification passes; failures are not counted
as passing cases.

Packet-copy leaves, raid map results, decoded600 blocks, STL/model lookup and UI
effects are fixtures. Full GUI, renderer, native container lifetime, callbacks
and concurrent destruction are not validated. The helpers rely on existing
native serial dispatch/lifetime assumptions and add no synchronization scheme.

Old category2 row provenance is also unresolved: D960 updates nested entries
rather than replacing the whole category. A fresh nonempty kind3 plus an existing
same-model row0 does not independently prove that this packet supplied that row.
Normal native state2 clearing remains relevant; v2 does not add a row clear or
claim this omitted-old-row boundary fixed.

## Reproduce

Keep the unchanged v1 Python modules and `raid-partyface/pe_support.py` available;
v2 imports their read-only support. Use the same existing native dependency path
as v1. Run from the R195 root with an actual Python runtime:

```text
<python> -B source/scripts/client/raid-team-readiness/build_readiness_v2.py --input resume/DNF-r195-local-face.exe --output-dir <new-external-dir>
<python> -B source/scripts/client/raid-team-readiness/verify_readiness_v2.py --input resume/DNF-r195-local-face.exe --output-dir <same-external-dir>
```

Both commands check available physical memory >=3GiB; the builder refuses existing
candidate/receipt outputs. No Go/PVF, deployment, installation or live-process
access is performed.

Main-thread independent verification is complete: it rebuilt from source into
`resume/a02-readiness-v2-main-review/`, passed all 72 verifier cases and confirmed
SHA256 `d41d6c4392182fa55327c2284e55797c06a73ae1a98703c876b126af56bb1203`.
This confirms reproducibility and the bounded fixtures, not GUI acceptance,
the unresolved 591 listener path or old-row provenance. No A02-complete claim is
made. The worker has handed back the three new files and stopped further edits.
