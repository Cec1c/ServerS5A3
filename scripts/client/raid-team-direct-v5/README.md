# Raid team readiness v5: Anton direct entry

This offline tool adds the readiness check to Anton's direct state2-to-state4
transition at `0x0192F60D`. It reuses the adjacent
[`raid-team-ready-v4`](../raid-team-ready-v4/) helpers for current raid/model
identity, result eligibility, complete kind3 receipt, row0 availability and
lifecycle invalidation. The existing state3 transition remains protected.

An eligible result waits until the current complete team data is available;
denied/invalid results retain native passage. The other raid model branches at
this caller still request state3. No reward grants, packet layouts, resource
files or server behavior are changed.

## Exact images

| Role | SHA256 |
|---|---|
| Required v156 input | `3fdc7c17584731cae240ca5e54122a4180485aab05b7fff8114a14d7822bf2ad` |
| Reproducible output | `cd3b8976a586fb5870a4a0ec854e1fce86c6c50af78d5b6a816660a4130571d0` |

The tool rejects any other input. It checks the existing v4 code and hooks,
updates the direct CALL and relocated helper targets, adjusts the `.a4rx`
virtual size and PE checksum, and asserts the exact final output SHA256.
Bytes outside the recorded changed ranges must remain identical. The input is
never overwritten; the output directory must not exist. Image length is unchanged.

`build.json` records this v156-to-v5 operation using `changed_ranges`; it does
not inherit a v154-to-v4 `allowed_ranges` record. Runtime memory availability in
the JSON varies by machine; executable bytes are deterministic.

## Dependencies and commands

Use Python 3 on Windows. The inherited memory guard uses
`GlobalMemoryStatusEx` and requires at least 3 GiB available RAM. Keep the entire
adjacent `raid-team-ready-v4` directory: its v1/v2/v4 build and verify modules
and `pe_support.py` are dependencies. No absolute import paths are embedded.

Building requires only the Python standard library and these sibling modules.
Verification additionally requires Capstone and Unicorn, installed into the
selected Python environment (or provided through its normal `PYTHONPATH`):

```text
python -m pip install capstone unicorn
```

Run from this directory, substituting paths to your exact input and a new output
directory:

```text
python -B build_direct_v5.py --input /path/to/v156.exe --output /path/to/new-v5-build
python -B verify_direct_v5.py --input /path/to/v156.exe --candidate-dir /path/to/new-v5-build --output /path/to/new-v5-build/verify.json
```

The builder writes `candidate.exe` and `build.json`; the verifier writes a new
JSON report and refuses to overwrite an existing report. No installation,
running-process access, network access or deployment is performed.

The helper extension deliberately matches one exact source anchor in the
sibling v4 builder. If that source changes, review the helper semantics instead
of weakening the anchor assertion. Both the old helper bytes and final image
hash provide additional checks.

## Executed coverage

The portable verifier contains **22 bounded native checks**, tested against
the exact reconstructed output. It includes:

- The old v156 direct bypass, with the real completion getter and protected
  global wrapper; the decoded protected value remains a fixture.
- Unknown/eligible waiting in state2, repeated ticks, full kind3 arrival and
  transition on the next tick; denied/invalid passage.
- Non-shortcut protected values, other raid models and unfinished introduction.
- Empty/party packets, missing row, changed identity, phase clear and kind3
  preceding result601.
- Waiting register/flags/stack restoration and passing ABI comparison against
  the original v156 setter boundary.
- Late kind3 through the native Anton party-refresh path and category2 team
  notification callee; the latter is a native `RET 16` and does not rebuild the
  team page.

It also verifies the checksum, changed-range boundaries and hook targets, and
classifies the ten previously identified direct setter callers. Only two in
that set can submit state4; both are gated. This is not a global proof about
every indirect call or arbitrary field store.

This directory does **not** contain or claim to run the separate six-case suite
or the original 95-case v4 adapter. The existing v4 verifier's PE checks target
v154-to-v4; do not point its unmodified CLI at this v156-to-v5 build. These 22
checks are a separate bounded suite, not a replacement claim for all v4
lifecycle coverage.

## Evidence limits and acceptance

Native execution covers the state2 tick dispatcher, the full direct transition
function, completion getter, protected global wrapper, installed helpers,
result601 parser window, decoded kind3 loop/tail, party-refresh selector and
category2 notification callee. UI setters, model containers, row lookup,
decoded block content, protected-value decoding and mutex leaves are fixtures.
The common post-tick validation/UI-error tail is not executed.

An empty team page built before data arrives can remain unbound: the observed
late notification path does not rebuild it. This patch prevents that early
entry; it does not repair a page already opened in state4. The actual runtime
shortcut value and full renderer are not established by these fixtures.
Unknown eligibility or missing matching complete data continues waiting under
the existing v4 contract; no timeout bypass is added.

Controlled acceptance should confirm that the first team page binds its
recipient and item rows after complete data, with both the direct and regular
entry paths exercised where available. Offline success alone does not establish
that every placeholder, image or reward-display report has the same cause.
