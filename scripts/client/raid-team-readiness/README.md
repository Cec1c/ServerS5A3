# Anton continuous party-to-team readiness candidate

Offline candidate only; main-thread bounded review completed, GUI acceptance pending. This is
not a complete A02 fix. No Go, existing raid-team-refresh/local-face file, native
model data, award logic or packet body is changed. Only the three local sites
listed below are patched on exact input local-face `8ab4297d`.

## Unresolved reconnect boundary

**A fresh client reconnecting directly into the team-reward stage may have no
local category0 ownership.** The server can send 587 state4 then kind3 without
party kind1. A UI tick can occur between those messages even when TCP delivery is
in order. This candidate deliberately bypasses the wait when ownership is false:
that tick can enter native state4 before kind3. The late packet sets freshness
but this patch does not rebuild/replay the team page. This scenario is NOT
protected and NOT claimed fixed. Widening the gate would risk waiting forever
for unqualified observers. The verifier contains an explicit unresolved-case
fixture, rather than treating wire ordering as proof of UI ordering.

The intended protected case is continuous Anton party -> team with local
category0 ownership already present. A committed kind3 delayed past the first
eligible tick causes a wait at state3 and retry on later native ticks. A commit
failure with no kind3 may continue waiting; no timeout invents reward data.

## Hooks and identity

| Site | Patch | Effect |
|---|---|---|
| `2001639` | CALL wrapper -> tail JMP original `32B6530` | Invalidate prior freshness on every network 587 state4, including already-open windows; record current Anton outer/raid identity before the unchanged native call. |
| `202FCC8` | JMP helper; relocate six-byte `mov ecx,[56A3798]`; return `202FCCE` | After the entire 600 block loop, kind `[ebp-31]==3`, count `[ebp-32]>0`, model `[ebp-50]+4==1` and matching identity mark the same model fresh. This happens before the closed-window test. |
| `192F6ED` | CALL wrapper -> original `192F0A0` or `RET 4` | Only arg4, outer state3, skip428==0, model `[ebp-14]+4==1`, matching current Anton identity, existing cached local actor and native category0 ownership can wait. Fresh same-model plus category2 row0 permits original state4. |

The data section holds five dwords: outer, raid manager, cached raid, current
raid key, fresh model. It does not own or retain any object. Current live pointers
are obtained first; stored identity tokens are only compared, never dereferenced.
`56A3798 + C3CC` is the current outer (confirmed by `32B8840/32B6530`).
`56A3778 +4/+8` are the current key/cached raid (confirmed by `1A553E0`).
The raid mode at `raid+24` must be zero; model mode at `model+4` must be one.
The gate also compares outer against the original ECX at `192F6ED`.

Before calling native `192B0D0(model,0)` and, if needed,
`192B140(model,0)`, require `56A3758` and its cached local actor `+70` nonzero.
Native functions then use cached `1A553E0/296DD80` and `29614E0`, followed by
read-only `192AF00` ownership lookup (types 1 or 2). Added code does not invoke
lazy lookup on a missing cache. `192AD70(model,2,0)` is the row0 lookup.

On identity mismatch/missing manager/missing actor, other mode, arg5, or absent
ownership, the original call is preserved. Native exit prefix
`1A64B8D/90` writes current key=-1 and cached raid=0: after those writes, the wait
gate cannot hold the old window. This is a local native proof, not a test of the
entire online leave protocol.

Native 587 state2 still clears the selected model through `2001515`, vtable+4
`192DF50` for all three modes. D960 is NOT a whole-category replacement. The
marker does not claim that every omitted row was deleted; it requires a later
nonempty same-model kind3 plus row0. It is not a per-row packet provenance tag.
Reverse kind3-before-587 is intentionally unsupported and remains waiting until
a later qualifying packet. Reset is not hooked at `192FAE0`.

## Register, stack and PE contract

Each helper saves flags and all integer registers with pushfd/pushad. Saved ECX
is at `[esp+18h]`; gate arg is `[esp+28h]`. Pass restores before tail-jumping to
the original callee; wait restores then RET4. The 600 helper restores all but
the original relocated ECX load. There are no extra persistent UI/model writes,
allocations, native refcount edits, team rebuilds, state2 calls or movie replays.

Two checked empty section-header slots are used: `.rdyrx` RX code (609 bytes)
and `.rdyrw` RW data (20 bytes), each occupying one 4096-byte file/image page.
The image is fixed-base/non-ASLR. Builder pins input SHA and 15 instruction
windows, including getters, ownership, native wrapper, loop tail and exit prefix.
Only three instruction sites, PE sizes/checksum/count and two new section
headers differ inside the original file; the 8192 appended bytes are independently
checked. Existing local-face bytes are preserved.

## Reproduce

Use an actual Python runtime with the existing native dependencies; Windows'
`python.exe` app alias is not the runtime used here. From the R195 root:

```text
<python> -B source/scripts/client/raid-team-readiness/build_readiness.py --input resume/DNF-r195-local-face.exe --output-dir <new-external-directory>
<python> -B source/scripts/client/raid-team-readiness/verify_readiness.py --input resume/DNF-r195-local-face.exe --output-dir <same-external-directory>
```

Both commands check available physical memory >=3 GiB before loading images.
Builder refuses an existing output candidate/receipt. No deployment or live
process access is implemented. Dependencies reuse read-only `raid-partyface`
PE support and `H:/codex-work/20261021/r163/raid-portrait-worker/native/python-deps`.

Current external output: `H:/codex-work/20261022/r195/resume/a02-readiness/`.
Candidate SHA256:
`0bc0431798eee35e509a4851040882756249ce7daea85c5854b9c91a8e4e12e0`.
Size: 111874048 bytes. Rebuild is byte-identical.

53 bounded cases pass, including the explicitly unprotected reconnect case.
The main thread reran all 53 cases from a separate copy under
`resume/a02-readiness-main-review/`; candidate bytes and hash match the reviewed
output. This confirms the bounded checks, not the unresolved reconnect behavior.
Real native `32B6530` is executed for closed/already-open and special-mode
branches, with exact baseline input/output register/flags/stack comparisons.
Ownership wrappers, cached getters and 600 loop tail execute native instructions.
UI leaves, model/STL lookups and decoded per-block payloads are fixtures. This
does not validate full packet parsing, the renderer, online callbacks, concurrent
UI/network scheduling or real-machine acceptance. The helpers assume the existing
native dispatch/object lifetime rules; they add no synchronization scheme.
