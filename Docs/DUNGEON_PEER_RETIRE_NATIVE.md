# Dungeon peer-retire candidate

Fixed v166 experiment for the current dungeon op9 kind3-to-FFFF peer retirement
path. It keeps other members' active actors while clearing only the retiring
record association, and restores only a null actor+0x4C8 binding for the
following self kind0 update.

The builder and verifier do not install, launch, sign or publish a client.

## Fixed artifact

- Input SHA256: 0e16090f0e4e3ac830f70183c814f5b24724cec235935e6c1221bf2d865d4243
- Input size: 111902720
- Candidate SHA256: 922ba3122c4e1e16ed097279293381f24101099f5b08e2366d38d02936276c5b
- Candidate size: 111906816

Only the fixed v166 layout is accepted. The verifier rebuilds the candidate in
memory and requires byte equality before executing any native window.

## Build

The builder uses the Python standard library:

~~~text
python scripts/client/dungeon-peer-retire/build_candidate.py --input <v166.exe> --output <new-output.exe>
~~~

It appends one aligned .prsafe read/execute section, redirects the two exact
setter callsites, updates section count, code size, image size and PE checksum,
and verifies that all bytes outside the declared edit ranges remain unchanged.

## Verify

Python 3.10 or newer with the exact versions in Docs/dungeon-peer-retire-requirements-validation.txt:

~~~text
python scripts/client/dungeon-peer-retire/verify_candidate.py \
  --input <v166.exe> \
  --candidate <candidate.exe> \
  --receipt <new-directory>/verification.json
~~~

The verifier independently rebuilds the candidate, verifies input and candidate
hashes, candidate section and helper hashes, base-relative callsite relocation,
PE checksum, Capstone decoding, and Windows SEC_IMAGE_NO_EXECUTE mapping.

The receipt is created exclusively and only after all cases pass.

## Verification groups

The original group has 13 cases:

- Original and patched target cleanup
- Other-member actor/drawable preservation followed by kind0 refill
- Existing same-key null refill and new-self namespace path
- Nonzero binding preservation
- Lookup miss, actor-key mismatch and foreign record pointer
- Wrong route, wrong owner and invalid slot pass-through

The lookup miss and actor-key mismatch controls set their registry result
explicitly; they do not rely on a fixed lookup map.

The supplemental group has 38 cases:

- Slots 1 through 7 for kind3: other-member clear, self-key pass-through and
  empty-slot pass-through, 21 cases total
- Slots 1 through 7 for kind0: null-binding fill and existing-binding
  preservation, 14 cases total
- kind0 wrong route, wrong owner and FFFF key, 3 cases total

Every executed callsite checks EBX, ESI, EDI, EBP, ESP and EIP in the final
state. EFLAGS are recorded but intentionally not compared: the original setter
changes them and both patched callers do not consume them.

## Evidence boundary

The helper, original setter/getter/accessor/cleanup windows and patched callsites
are real v166 code. Record, actor, model, namespace decoder, registry lookup,
cleanup manager and model-destructor leaves use synthetic fixtures or controlled
stubs. The suite does not cover a complete op9 handler, UDP transport, rendering,
GUI or a live game process. It is not a release, installation or user acceptance
claim.
