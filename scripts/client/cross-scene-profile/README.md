# R195 cross-scene client profile candidate

This directory rebuilds the reviewed eight-site cross-scene client candidate
from the fixed direction-release base. The builder is self-contained: it uses
only the Python standard library and the four manifests in `manifests/`.
There is no import from the private resume tree and no machine-specific
manifest path.

The candidate is **not deployed and not accepted in a live client**. It covers
menu fallback, mode3 detail actor selection, aux portrait drawing and
portrait-only registration protection, and the two mode8 direct-call routes.
Those client changes require the matching server change; do not publish the
client candidate by itself.

## Provenance

Ported from `resume/build-cross-scene-client-candidate.py`. The PE, hash,
alignment and checksum helpers are a generic reduction of
`scripts/client/direction-key-release/build_candidate.py`; no direction-key
hook, IAT scan or helper emitter is included. The four manifest payloads are
byte-for-byte copies of the reviewed `feedback-next-native` manifests. The
original script's Windows memory probe and fixed absolute base path were
replaced by the input argument and the fixed SHA check.

## Fixed input and expected output

| Item | Value |
| --- | --- |
| Input SHA-256 | `a751cccfe3fb9f7a3730393fdfb32d0546222b55a5a254fc4f2416f6869fbdf6` |
| Input size | 111882240 bytes |
| Candidate file | `DNF-cross-scene-candidate.exe` |
| Candidate SHA-256 | `db8d0c921284d0baa2a39789ba20ba22e806f32a5c54bef17d4719ad3aee9ec0` |
| Candidate size | 111886336 bytes |

The builder rejects any other input hash or size, and refuses to write a
candidate whose hash differs from the reviewed candidate hash above.

## Usage

```text
python build_candidate.py --input <fixed-base.exe> --output-dir <new-or-empty-dir>
```

`--output-dir` must be absent or empty. The builder creates it when needed
and refuses to overwrite an existing candidate or receipt. It never writes to
the input path. On success the output directory contains:

- `DNF-cross-scene-candidate.exe`
- `build-receipt.json`

The EXE is rehashed after writing. Both files are prepared in a new sibling
staging directory, then the completed directory is renamed into place. A
preexisting empty destination is removed with an empty-only directory operation;
a concurrently populated destination causes failure. If publishing fails, the
staging directory is retained for diagnosis, with no recursive cleanup.

The receipt records relative manifest names and hashes, per-patch details,
the appended section, the input and output hashes, and the proof scope. It
does not record an absolute H-drive path.

This repository does not include the EXE or a generated receipt. The tool
intentionally has no Windows memory check; the parent rebuild must perform its
5 GiB available-memory check before invoking it.

## Reviewed manifests

Paths are relative to this directory. Each manifest is an exact copy of the
reviewed candidate manifest.

| Manifest | Sites | Purpose |
| --- | --- | --- |
| `manifests/menu-candidate/windows.json` | 3 | Menu open, build and click fallback |
| `manifests/detail-candidate/windows-v1.json` | 1 | Mode3 local-context actor selection |
| `manifests/portrait-candidate/windows-v1.json` | 2 | Portrait-only registration guard and aux portrait draw |
| `manifests/mode8-route-candidate/windows-v1.json` | 2 | Mode0 and mode3 mode8 direct-call routes |

## PE construction and checks

The builder appends a `.xscene` PE32 section at VA `0x6EC0000` and redirects
the eight reviewed call sites into it. It preserves the original image outside
the listed call sites, section header, section count, SizeOfCode, SizeOfImage
and checksum fields.

Before writing, it verifies the fixed input, PE32 layout, non-dynamic base,
empty certificate table, no overlay, section-header slack, non-overlapping
edit ranges, every manifest call site and code hash, every replacement's
relative target, original-byte preservation, output section read-back, PE
checksum, and the final candidate SHA-256.

## Verification boundary

A successful build proves PE structure, patch read-back and byte preservation
for the fixed input. It does not prove full-client execution, graphical
behavior, cache population, model allocation, live-server authorization or
deployment. The candidate still needs the paired server build and the normal
serial rebuild and acceptance checks. Do not describe it as deployed or
accepted until those checks are recorded.

## Portable native verification

Use Python 3.11+ in a dedicated environment with the pinned dependency:

```text
python -m pip install -r requirements-validation.txt
python -B verify_candidate.py --baseline <fixed-v158.exe> --candidate <candidate.exe> --output <new-directory>
```

The output directory must not exist. The verifier rejects Python optimization
mode because the native instruction checks use assertions. It requires
Unicorn 2.1.4, reads the two EXEs, reconstructs the candidate in memory from
the four manifests, and compares every byte before executing scoped Unicorn
tests. Neither EXE is launched or copied by the verifier. There are no private
directory imports or machine-specific dependency paths.

The nine suites cover menu gates (648 candidate/12 baseline), mode3 detail
selection (189/3), auxiliary lookup (221), portrait drawing/registration
(48 draw/24 guard/28 baseline), mode8 routing (216/8), appearance data (5),
request context (60), portrait reset (16), and actor cleanup (32). The final
summary identifies the actual portable source files, manifests, dependency
pin and each generated proof by SHA-256. Failed runs retain partial output;
rerun with a fresh destination. The original investigative source hashes in
source-provenance.json describe ancestry, not runtime dependencies.

These tests execute native windows with explicit boundary fixtures. Graphics,
resource/model creation, some scene/container lookups, virtual destructors
and UI managers remain fixtures. Ordinary party HUD slots resolve scene actors
through a different path from raid MemberFace controls; zero-owner cache rows
and the independent scene-target menu branch are not established as repaired
by this candidate. Live acceptance must separately check raid portraits,
inspection, fashion changes, scene transitions and subsequent cleanup.
