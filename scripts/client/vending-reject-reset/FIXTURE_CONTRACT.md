# Local native fixture contract

The public verifier can validate profile binding and deterministic byte output
without a private fixture. A complete native RED/GREEN proof requires the
operator to provide a local module whose path is not committed.

The local fixture must also be supplied with its exact SHA256. The public
wrapper checks that digest before importing or invoking the module.

Required entry point:

```python
def verify_reset_fixture(profile, baseline_bytes, candidate_bytes):
    ...
```

The returned object must be a JSON-safe mapping with:

- `passed: true`;
- `tests: 8`;
- zero `failures`, `errors`, and `skipped`;
- `redFailureAdvanced: true`;
- `greenFailureReset: true`;
- `repeatedFailureReset: true`;
- `successParity: true`;
- `retrySuccessParity: true`;
- `nullManagerSafe: true`;
- `nullControllerSafe: true`;
- `helperMapped: true`.

The fixture must observe these controls separately:

1. The unmodified baseline reaches the original late empty result window.
2. The candidate clears controller pending/timer/state and still presents the
   original error window.
3. A repeated failure repeats the reset.
4. A normal success reply has the same slot, row, count, display and refresh
   outcome as baseline.
5. Failure followed by retry success matches baseline.
6. Null manager skips reset without changing the original failure behavior.
7. Null controller does the same.
8. The appended helper section maps with the profile-derived bytes.

The fixture must bind both complete-file SHA256 values from the local profile
and candidate before executing. It must not launch the game, attach to a
player process, accept a memory-only candidate, or infer success from logs.
UI, timer, inventory and transport adapters are allowed only when the tested
handler/controller/reset/settlement instructions are the original candidate
instructions.
