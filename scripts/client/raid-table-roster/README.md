# Raid-table group badges from the authoritative roster

The current v153 table rows first read the first character key in a NOTI584 participant vector, then call the local scene actor lookup `296D730`. A missing scene actor skips the group badge. With an actor present, `29614E0` merely recovers that same character key; the following loop searches the NOTI591 roster and uses its group byte at `+2D`.

This prevents an otherwise valid remote dungeon participant from reaching the badge setter when that participant has no local scene actor. Both table paths contain this dependency:

| Table path | First-key/scene lookup | Roster identity comparison | Badge call |
| --- | --- | --- | --- |
| Luke | `110C254` | `110C343` | `110C402` |
| Anton | `1875359` | `1875415` | `18754D2` |

`patch_roster.py` takes only the independently verified R195 seven-pair receive candidate (`8e92c29cbbbcc2e643a87cb73e199ec52e9b3beeb4146b3ec16d62404d36064c`). It stores the existing participant key in the former temporary actor slot and retains the original roster search and group-byte selection. Zero keys, empty vectors and absent roster entries do not produce a badge. It does not create scene actors, invent groups, alter raid participation, or change portrait controls. Four instruction ranges and the PE checksum are the only edits; the previous receive, barrier and PartyFace changes remain byte-identical.

The resulting candidate has SHA256 `53c93cf72f83454e1e5aa07e0ce45bffc492e3219f5f3f5a9434b0dd6645ff40` and size 111861760 bytes. The independent verifier does not import the builder. It checks all byte differences and checksum, then executes both native row branches, their actual roster comparison and `187ACD0` badge wrapper in 18 scenarios per image. Baseline local-member behavior passes; baseline remote-member scenarios skip the badge, while the candidate selects the matching roster group. Reordered and missing members, changed groups, multiple participants, zero/empty input, absent widgets, stack balance and unchanged source records are covered.

The native fixtures provide the manager, STL iterator leaves, shared-pointer construction, scene actor lookup and UI frame sink. Execution starts at a table row with valid surrounding state. This is not full 584/591 ingestion, full window construction or graphical acceptance. Actual online battle-table rendering remains to be checked.

Build and verification receipts are under `H:/codex-work/20261022/r195/resume/table-build3.json` and `table-native3.json`. The original table build had a wrong Luke instruction length; table2 had the six-pair receive candidate's incorrect class1/103 admission. Both are discarded and never deployment candidates. The corrected builder asserts all original bytes and preserves the cleanup instruction at `110C269`.

```text
python patch_roster.py --help
python verify_roster.py --help
```

Run through the external serial memory guard. Keep executable files and native dependencies outside the repository.
