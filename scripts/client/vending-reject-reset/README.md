# Vending reject-reset tooling

These tools build and inspect a version-specific patch for the CMD215 vending
failure path. The repository does not contain a client binary, a supported
client hash, a fixture, or any locally derived profile.

The operator supplies:

- an exact local input EXE;
- a local JSON profile matching `PROFILE.schema.json`;
- optionally, a local fixture module implementing `FIXTURE_CONTRACT.md`.

No path or expected input SHA is embedded in this directory. The same builder
can support another reviewed client layout only by supplying a new profile and
reviewing every fingerprint.

## Safety model

`build_candidate.py` verifies all of the following before changing bytes:

- the complete input SHA256 and size;
- PE32 i386/image-base and section-table invariants;
- the exact CMD215 failure-site bytes;
- the controller getter load/store fingerprints;
- the reset prologue, return convention and existing-caller fingerprints;
- a clear section-table slot and non-overlapping appended section;
- only declared original-byte ranges change.

The default command is a plan: it computes the output in memory and prints the
exact output SHA256 and receipt, but writes nothing. `--write` requires a new
output and receipt path plus an `expected_output_sha256` and
`expected_output_size` pinned in the profile. It refuses existing files and a
digest mismatch.

This tool does not provide a release wrapper, protection layer, signing,
deployment, or live-process access.

## Commands

Run the public static tests:

```text
python -B -X utf8 scripts/client/vending-reject-reset/test_tooling.py
```

Print a plan:

```text
python -B -X utf8 scripts/client/vending-reject-reset/build_candidate.py --input <local-client.exe> --profile <local-profile.json>
```

After reviewing that exact output digest and writing it into the local profile:

```text
python -B -X utf8 scripts/client/vending-reject-reset/build_candidate.py --input <local-client.exe> --profile <local-profile.json> --write --output <new-candidate.exe> --receipt <new-receipt.json>
```

Run static candidate verification:

```text
python -B -X utf8 scripts/client/vending-reject-reset/verify_reset.py --profile <local-profile.json> --baseline <local-client.exe> --candidate <candidate.exe> --receipt <verification.json>
```

The verification profile must already pin `expected_output_sha256` and
`expected_output_size`; a plan-only profile is intentionally insufficient.

A complete native RED/GREEN result requires the optional local fixture:

```text
python -B -X utf8 scripts/client/vending-reject-reset/verify_reset.py --profile <local-profile.json> --baseline <local-client.exe> --candidate <candidate.exe> --fixture-module <local-fixture.py> --fixture-sha256 <sha256-of-local-fixture> --receipt <verification.json>
```

Without the fixture, the verifier reports
`static-only-native-not-run`. It never substitutes a real-client result with a
synthetic success.

The public repository contains no local fixture path or digest. Supplying the
fixture digest is an explicit local authorization boundary; it must match the
approved local fixture before import.

## Scope

The patch changes only the CMD215 `[00 01]` branch and appends the helper. The
profile's `resume_va` must immediately follow the original bytes. A null UI
manager or controller skips reset and resumes the original error display.
Success behavior and inventory settlement are outside this builder.

The optional native fixture is separate because a real exact input is not a
public repository asset. Its contract requires eight outcomes: old failure
advances to the late empty result, the candidate clears pending/timer before
the original error path, repeated failure resets each time, success parity,
retry-success parity, null-manager safety, null-controller safety, and exact
helper-section mapping.

## Already-patched cumulative clients

A reset branch alone does not prove end-to-end failure handling. V186 still
classified CMD215 as owning its error byte, while its callback expected the
dispatcher to supply that byte. Direct callback tests with error=1 missed this.
The shared native harness now executes the real opcode classifier. See
`../patch_vending_ack_error.py` and `../test_vending_ack_error.py` for the exact
V186 correction and dispatcher-to-reset RED/GREEN verification.

Inspect the current failure branch and mapped helper before choosing a build
profile. An already-patched cumulative release should be verified directly;
do not append another helper or strip its existing patches to feed this builder.
`verify_reset.py` proves a deterministic build from a declared unpatched input.
It does not attest the build provenance of an existing cumulative artifact.
A native regression on such an artifact must bind its complete hash, preserve
its actual receiver/reset/settlement instructions, and identify any independently
written negative-control copy as a controlled mutation, not a historical release.
