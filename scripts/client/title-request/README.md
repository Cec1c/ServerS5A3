# Title request OP19 marker

This directory contains an exact-input, offline native candidate for ADD07.
It does not deploy or install anything and does not inspect a running process.

Required baseline:

```
SHA256 e5751354901fc142324d11cfc717e4a958396877f1c0fc545f51645f8e77af55
111910912 bytes
```

The existing `.op19ui` section starts at `0x6E95000`. Its current simple
whitelist is `0x2499E10`, `0x249A344`, `0x249A3DF`, `0x249A455`; two additional
routes require native stack authentication. Recognized unchanged equipment
requests emit marker 2 at `0x6E951C6`; all other caller returns use marker 0 at
`0x6E95052`.

The title-specific producer returns are `0x12A3474`, `0x12AB194`,
`0x12ACB2B`, `0x12ACB78`, and `0x12AF918`. Their containing event paths are
preserved in `evidence/` by `analyze_native.py`. The candidate extends only the
fallback at `0x6E95050` and matches these exact returns, destination slot 13,
count 1, and known parent return addresses where the containing function has a
frame pointer. Any different return, slot, count, or parent falls through to the
unchanged `push 0` behavior, so login/equipment reassert is not globally marked.

Run from this directory under the shared guard. Pass the source image and both
output locations explicitly; generated binaries, logs, and raw disassembly are
written only to the caller-selected private directories:

```powershell
$env:PYTHONPATH='<directory containing Capstone and Unicorn>'
python -B <path-to-serial-guard.py> add07-title-request python -B run_all.py `
  --input <fixed-v170.exe> `
  --output-dir <private-candidate-directory> `
  --evidence-dir <private-evidence-directory>
```

`run_all.py` runs static analysis, builds a candidate, and executes bounded
Unicorn ABI cases against both images. It requires Windows, Unicorn 2.x,
Capstone, at least 3 GiB available physical memory, and the exact baseline
digest. `title_extension.title_extension(extension_va)` and
`title_extension.fallback_redirect(extension_va)` are self-contained portable
entry points for direct composition by the main gameplay refresh builder.
