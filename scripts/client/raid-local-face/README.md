# R195 local-own raid face fallback

Fixed input: `resume/DNF-r195-table3.exe`, SHA-256
`53c93cf72f83454e1e5aa07e0ce45bffc492e3219f5f3f5a9434b0dd6645ff40`.

Build only (never installs):

```text
python build_local_face.py \
  --input <external-artifacts>/DNF-r195-table3.exe \
  --output <external-artifacts>/DNF-r195-local-face.exe \
  --receipt <external-artifacts>/local-face-build.json

python verify_local_face.py \
  --input <external-artifacts>/DNF-r195-table3.exe \
  --candidate <external-artifacts>/DNF-r195-local-face.exe \
  --build-receipt <external-artifacts>/local-face-build.json \
  --receipt <external-artifacts>/local-face-verify.json
```

The candidate SHA-256 is
`8ab4297d1e3ebe2dc9d71801ebae210cb3ec673dbcdf707b35a3d5c8faf6f790`.

## Patch

The only original-code edit is a five-byte jump at `0x110B16A`, replacing:

```text
8B 4D 8C 8B 33
```

The appended `.locface` helper at `0x6EAE000`:

- reads `sceneManager=[0x56A3758]`, but calls raid getter `1A553E0` with
  `ecx=[0x56A3778]`;
- requires the existing local cache `[sceneManager+0x70]` and compares it to
  the current actor; it never calls lazy local getter `296DD80`;
- requires `[actor+0x4F4]==0`, `[actor+0x34]==0x10`, non-null control and raid;
- calls read-only `1A5A5E0(key,owner,route)` with owner/route from `raid+2/+3`;
- requires the returned actor's key to equal `[actor+0x20]`, requires the
  loaded image-key pointer `[cacheActor+0x30]`, and requires its `+0x34` to
  differ from `0x10`;
- draws through `3C1BEA0 -> 2966C60` at the original vcall geometry,
  `14x14`, `target=0`, `color=0xFF`;
- returns to `110B16F` on every failure, preserving the original vcall;
- never calls `2965BD0`, `1A62AA0`, or the lazy local getter.

The `.locface` helper and patch do not alter `29674E0`.

## Register contract

The original code after the successful draw resumes at `110B199`:

- `ECX` is overwritten immediately at `110B199`;
- `EDI` is overwritten at `110B1A4`;
- `EAX` is overwritten by the `1A553E0` result;
- `EDX` is overwritten at `110B1C3`;
- `ESI` is overwritten at `110B1E8`;
- `EBX` is still the actor and is used at `110B222`;
- `EBP` and the caller frame remain live.

The helper therefore saves/restores `EBX`, `ESI`, `EDI`, flags and stack, while
leaving `EAX/ECX/EDX` caller-clobbered like the original vcall. `ESI` is restored
to the actor vtable loaded by the relocated `mov esi,[ebx]`. It does not use a
blind `popad`.

On a miss, the original continuation at `110B16F` does not consume incoming
`EAX` or `EDX`; it starts by pushing constants and obtains `ECX` from the
control. On a hit, `110B199` overwrites volatile registers before their first
use. The helper exits are exactly `110B16F` and `110B199`.

## Verification scope

`verify_local_face.py` runs the complete appended helper with actual geometry
getters and a synthetic PE/object environment. The registry lookup, raid getter
and `2966C60` UI leaf are controlled stubs. It covers local/peer, drawable
present/empty, scene `+0x34`, missing scene/raid/cache/control, key mismatch,
cached `+0x34==0x10`, zero/nonzero owner-route namespace, flags, stack and source
object stability.

No GUI pixels, live process memory, PVF parse, deployment or installation are
claimed. `verify_local_face.py` also independently checks the PE checksum,
entry point, data directories, existing section headers, zero padding and the
exact set of changed original bytes.

## Public source usage

Historical input hashes remain mandatory; no EXE is included or installed. Use Python 3.10+ with `unicorn` 2.x and `capstone` 5.x in your environment (and `pefile` for the shared settlement harness). The `raid-partyface/pe_support.py` and `test_vending_settlement_v179.py` helpers already belong to this public repository. No workstation dependency path is required.
