# Mail/Fate candidate builder

`build_candidate.py` is the public planner for the reviewed V179 Mail/Fate raw candidate. It does
not ship, generate, or execute a client binary by itself. The caller supplies the exact local input;
the checked packet-level source for this candidate is:

- input SHA256 `3d6f3c989b6407676ff8ce2b4a7b189a6dc079f2c84740313070826641f1f6b1`, size
  `111919104`
- expected raw output SHA256
  `df4962789c2587d9f8af239619e3bcae38a0999b9f147fad0415b43f648b6c33`

The builder contains two reviewed client changes:

1. At the op97 clear path, it clears the selected mail-detail window through the native window
   manager before the list is destroyed. The hook preserves flags, registers, stack phase, and the
   original continuation; it verifies the exact op95 manager-lookup bytes and only handles the
   clear branch.
2. At the shared guild gate call site, it adds an outer gate that accepts only class0/1456 for the
   Fate weekly acquisition notification. The shared receive bitset remains unchanged, and all
   other classes/opcodes fall through to the previous gate.

The candidate is **not game-accepted**. The existing native evidence remains attached to the exact
raw SHA above; player mail refresh/selection, Fate weekly delivery, relog, and gameplay acceptance
must still be checked separately. This repository and builder contain no anti-RE profile, protected
build, private key, EXE, PVF, disassembly dump, or native payload. Anti-RE/protected packaging and
deployment are separate release operations and are not implied by this tool.

The builder requires Python 3 and `pefile`. Run without `--apply` for a dry-run plan:

```text
python scripts/client/mail-fate/build_candidate.py --input <exact-local-DNF.exe>
```

`--apply` requires a new output path. Do not overwrite the source or an existing candidate.
