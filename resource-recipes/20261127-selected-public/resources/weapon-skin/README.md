# Selected weapon-skin resources (C14-C17)

This recipe publishes the reusable resource side of the selected weapon-skin
catalog. It contains no private PVF, no EXE, no NPK binaries and no copied
private `equipment.lst`/`stackable.lst` files. The five NPKs are distributed in the
public Release [selected-resources-20261127](https://github.com/Cec1c/ServerS5A3/releases/tag/selected-resources-20261127), asset `weapon-skin-npk.zip`;
`assets-manifest.json` records the five expected names, sizes and SHA256 values. The Release ZIP is 6,965,428 bytes with SHA256 `0c65cefd06ca855247ed908c59f6f80aa0202f39414261372bc65ef2c88cd27b`.

## ID contract

| ID | Contract |
| --- | --- |
| C14 | Linus fifth-shop `幻化` tab, inserted before `[/sell info]` |
| C15 | Magical mold 490704411 at 1,000,000 |
| C16 | Secret mold 490704427 at 2,000,000 |
| C17 | 13,329 native catalog rows, 40,032 selected content paths and five NPK dependencies |

`native-catalog.schema1.json.gz` is the 13,329-row catalog extracted from the
precise client delta. Every row's `wid` is checked in order against the
13,329 equipment rows. `weapon-catalog.pvf-plan.json.gz` stores the complete
selected content token streams, the append-only equipment/stackable rows, the
Linus shop specification and the two mold price edits.

## Build

From the repository root, on 101:

```text
python resource-recipes/20261127-selected-public/resources/weapon-skin/build_weapon_skin.py \
  --input /path/to/Script.pvf \
  --output /path/to/Script.pvf.candidate \
  --report /path/to/weapon-scan-receipt.json \
  --sha256 81ccba6613a442a856ecc66ea6344ba62cb16cfd8783173adbd85e43ac9ad781
```

Use `--accept-input-sha256 <sha>` only after the target baseline is explicitly
reviewed. The tool refuses an unaccepted input, an existing output, a plan hash
mismatch, a duplicate or conflicting index row, a partial shop/mold edit, or any
change outside the planned paths. Existing index entries are never replaced.

## Verification on 101

Run serially with the main line (the Go module root is `go-server`):

```text
cd go-server
go test ./cmd/tools/sky-job-audit-pvf ./cmd/tools/weapon-catalog-pvf
go run ./cmd/tools/sky-job-audit-pvf -pvf <candidate> -report <audit.json>
```

The weapon-catalog receipt must report `passed: true`, the exact input/output
SHA256, 40,032 content paths verified, and zero unplanned changed paths. If the
input already contains all 13,329/26,660 rows, the append counters are zero;
that is the expected result for the reviewed 81cc baseline.
