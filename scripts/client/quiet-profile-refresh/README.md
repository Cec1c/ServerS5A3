# Quiet automatic character profile refresh

Entering Luke's dungeon selector can refresh raid profiles on teammates' clients.
The mode8 profile receiver reuses mode3 and its details consumer opens a character
window after parsing. Repeated automatic updates therefore open a window without
an inspection request. The reported subsequent crash still needs live retesting;
this patch does not establish every cause of that crash.

This candidate consumes the same profile fields and retains cache/stat updates.
It suppresses only the final window binding and creation in `22C2390`, when its
return address is `22C461E` and the enclosing mode3 return is `22C8ACD` (mode8).
All parsing finishes before the hook at `22C2539`; the quiet branch goes to the
original cleanup at `22C25B8`. Ordinary mode3 and the other details consumer
returning to `22C4971` retain their original behavior. EAX and flags are preserved.

The paired server change is required: an explicitly requested cross-scene
inspection sends a quiet mode8 cache prime followed by normal mode3. The two
frames share the appearance and wire locks and deadline, and recheck membership
and session identity before each write. Automatic refresh still sends only mode8.
This is a client/server candidate, pending deployment and human acceptance.

## Pinned files

| File | SHA-256 | Bytes |
|---|---|---:|
| v162 input | `04d0725f099bb94cc06ac305f14430e781c8f5c768ee149aef698d06d76c34ad` | 111894528 |
| Candidate | `d3d46a344c56f45f4104945b0fb804ccb426b3f9bd124743e8d536d4d093ce59` | 111898624 |

The standard-library builder changes one five-byte jump, the necessary PE header
fields, and a new `.prquiet` RX section. Its 50-byte helper starts at `06EC3000`.
It validates original bytes, exact changed ranges, zero padding, checksum, output
hash, and disk readback. It refuses an existing output directory or incorrect
input and does not execute or install the image.

```text
python -B build_candidate.py --client-exe <v162.exe> --output-dir <new-build-dir>
python -B verify_candidate.py --baseline <v162.exe> --candidate <new-build-dir>/DNF-prquiet-experiment.exe --output <new-proof-dir>
```

Use Python 3.11+ with `requirements-validation.txt` for verification. Optimization
is rejected. The verifier reconstructs and compares the entire candidate before
executing the fixed native details consumer through its return. There are 129
cases, including old/new behavior, 0/1/3/255 list entries, ordinary and other
callers, and 1000 consecutive refreshes in one emulator. It checks packet cursor,
window calls, stack, saved registers, SEH restoration and temporary allocations.

Packet-reader leaves, allocation/reserve/free, actor getters and the UI manager
are fixtures. This proves the scoped consumer and cleanup, not full mode3 object
lifetime, real resources/GPU, or absence of crashes in the game. Live verification
must include both clients, repeated selector entry, manual cross-scene inspection,
outfit changes and character/party transitions. No game process memory is read.
