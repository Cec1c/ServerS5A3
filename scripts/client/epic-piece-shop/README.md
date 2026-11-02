# CMD817/CMD818 exact one-byte receive-gate patch

This directory creates and verifies a candidate for the released V207 client. The fixed baseline is:

| Field | Value |
| --- | --- |
| SHA-256 | `eca5ae9017176fb159ae22d6498b2a6805b8b93295042363a90f37f2daae7a03` |
| Size | `112549888` bytes |
| PE image base | `0x400000` |
| Receive filter | `0x6E83610` |
| Receive bitmap | `0x6E92300` |

In this baseline, opcode `816` is enabled and opcodes `817` and `818` are disabled in the bitmap byte at virtual address `0x6E92366`:

```text
before: 0x01  (bit 816 only)
after:  0x07  (bits 816, 817, and 818)
mask:   0x06  (bits 817 and 818)
```

`patch.py` changes only that bitmap byte. It rejects the wrong input SHA-256 or length, validates the native filter bytes, and refuses to overwrite an existing output or receipt. The resulting image has the same length, headers, PE checksum field, section table, native filter bytes, notification data, and every other byte. No section or hook is added.

The expected candidate identity is:

```text
SHA-256: 5ce2ce3f01dacd05a6ce542d088911cc4fceb0f44ffa85dfa3a0886339e7cb5c
size:    112549888
```

## Dependencies

Both tools require Python 3 and `pefile`. `verify.py` also requires `unicorn`. Install dependencies into a private directory and add that directory to `PYTHONPATH`; the public tools contain no machine-specific dependency path:

```powershell
python -m pip install --target <private-deps-dir> pefile unicorn
$env:PYTHONPATH = '<private-deps-dir>'
```

## Create the candidate

Use a separate destination file and a new receipt path:

```powershell
python patch.py `
  --input <fixed-V207-DNF.exe> `
  --output <private-output-dir>/DNF.exe `
  --receipt <private-output-dir>/patch-receipt.json
```

The patch receipt records both SHA-256 values, the exact changed file offset, VA/RVA, `0x01 -> 0x07`, unchanged PE headers and checksum, unchanged section table, and byte-level scope evidence. Keep EXEs and receipts outside the public repository.

## Verify the native filter

```powershell
python verify.py `
  --baseline <fixed-V207-DNF.exe> `
  --candidate <private-output-dir>/DNF.exe `
  --receipt <private-output-dir>/verify-receipt.json
```

The verifier first rejects wrong baseline or candidate hashes and any difference other than the fixed one-byte edit. It then maps the PE sections into Unicorn and calls the actual native filter at `0x6E83610` for every opcode from `0` through `65535`, once against the baseline and once against the candidate.

The pass condition is exact:

- The only changed decisions are `817` and `818`, both rejected before and accepted after.
- Opcode `816` remains accepted.
- Every call returns to the sentinel through the native `ret 4` path.
- The stack is balanced after every call: the return address plus the four-byte argument are consumed.

This is an offline native filter check. It does not start the game or the native EXE, execute a full packet dispatcher, exercise a GUI consumer, or claim in-game acceptance.
