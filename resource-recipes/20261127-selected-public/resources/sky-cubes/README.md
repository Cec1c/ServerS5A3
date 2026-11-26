# Selected sky resources (A20-A22)

Scope is A20 outer-box update, A21 cleanup and A22 tooling only. The five cube
definitions from A15-A19 are excluded and must already exist in the input PVF;
the published specification does not contain their names, appearance mappings or
output rows.

## Contract

- Outer box 990032001 must resolve to exactly 14 existing stackable IDs.
- Absolute-bind keys 0..13 must already exist for the published A20 recipe.
- Cleanup removes the 21 gated job blocks; the cleaned table has 161 valid
  eight-piece mappings and no remaining removal candidates.
- `sky-cubes-pvf` accepts a zero-new-cube specification for A20. A separate,
  task-local five-cube specification may exercise the generic builder, but it is
  not part of this published recipe.

## A20 outer update

The published `sky-cubes.schema1.json` has an empty `new_cubes` array. The input
must already contain absolute-bind keys 0..13; the builder verifies all 14 keys
and all 14 outer-box stackable references. It updates only box 990032001 when
needed and copies the input unchanged when the outer list is already exact.

```text
python resource-recipes/20261127-selected-public/resources/sky-cubes/build_sky_cubes.py   --input /path/to/Script.pvf   --output /path/to/Script.pvf.sky-raw   --report /path/to/sky-cube-receipt.json   --input-sha256 <exact input sha256>
```

## A21 cleanup

The audit tool's patch mode is the executable cleanup entry. It requires the
exact input SHA256 in write-producing mode, removes only gated job blocks and
re-audits the output before writing it.

```text
cd go-server
go run ./cmd/tools/sky-job-audit-pvf   -pvf /path/to/Script.pvf.sky-raw   -patch-out /path/to/Script.pvf.sky-clean   -patch-report /path/to/sky-jobs-patchspec.json   -sha256 <exact raw candidate sha256>
```

## A22 audit

The standalone audit must report 14 cubes, 161 valid mapped job blocks, 14
outer-box entries and zero remaining gated blocks. It also carries the expected
input SHA256 when supplied.

```text
cd go-server
go run ./cmd/tools/sky-job-audit-pvf   -pvf /path/to/Script.pvf.sky-clean   -report /path/to/sky-audit.json   -sha256 <exact clean candidate sha256>
```

A failing cleanup or audit is a blocker. No reference PVF is included.
