# Published v167 plus upstream guild client dependencies

The current published v167 contains the dungeon peer retirement repair. The
upstream guild grade, disband and party teleport builders target the older v166
image. This builder composes their exact, guarded output onto v167. It refuses
any overlapping change, retains all other v167 bytes and recalculates the PE
checksum. It does not install the result or modify either input.

```text
python build_candidate.py --base-v166 <published-v166.exe> --input-v167 <published-v167.exe> --output <new-candidate.exe> --manifest <new-manifest.json>
python verify_candidate.py --base-v166 <published-v166.exe> --input-v167 <published-v167.exe> --candidate <new-candidate.exe> --manifest <new-manifest.json> --report <new-report.json>
```

The verifier requires the existing Unicorn validation dependency. It executes
the actual composed image, checks the complete class-0/class-1 receive matrix,
grade sender, guild lifecycle callbacks and party teleport readers/writers
with the upstream bounded network/UI substitutes. Windows mapping uses
`SEC_IMAGE_NO_EXECUTE`. Real GUI operation and multiplayer acceptance remain
separate from these checks. The only new receive allowances are class0/377,
class1/303 and class1/434.
