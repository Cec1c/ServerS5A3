# P33 route2 candidate source

This directory contains only the isolated builder and bounded checker for the
v169-derived category0 gate. The main controller owns fresh pull, source
freezing, final building and deployment.

Behavior:

- `raid+0x24 == 2`: use current raid member `+0x2D` group equality.
- `raid+0x24` 0/1/3, missing manager/raid, or malformed roster: use the
  original temporary-party key gate unchanged.
- Missing route2 owner/self record: hide the non-self row.
- Category2 and reward entitlement data are not written.

Commands, when the main controller takes the serial shared lock:

```powershell
python -B candidate-route2/build_candidate.py `
  --input-exe <fixed-v169.exe> `
  --output-dir <new-build-dir>

python -B candidate-route2/verify_candidate.py `
  --build-dir <new-build-dir>
```

The builder enforces the fixed v169 size and SHA256. The checker reads the
actual candidate bytes for the predicate, helper and all three wrappers, then
requires byte-for-byte agreement with `build-receipt.json`. Its
`verification.json` is written into the same build directory.

Limits: synthetic objects only; no game, packet reader, GUI, resource/GPU or
final reward request. A structurally malformed vector falls back to the old
party gate; pointer-page validity is not proven by a userspace read.

`build-v1/` and `build-v2/` are preserved pre-directive local exploration
outputs. They were not accepted, are stale relative to the corrected
malformed-vector fallback source and must not be published or deployed.
