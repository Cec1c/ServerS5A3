# Luke single raid index restoration (offline only)

This tool restores the 12 dungeon and 58 map index rows missing from the pinned
current archive. It reads ID/path bindings from an independently extracted
upstream archive, never from numbered filename assumptions. Both archive SHA256
values are pinned in main.go. It requires identical complete MAP text and DGN
maze declarations; current descriptive text is retained. Existing conflicting or
duplicate bindings, missing upstream rows, changed maps and changed maze
declarations fail closed.

The reference is Script.pvf from the locally retained Tencent
DNF_SEASON4_V23.6.15.0_Pack.7z.001 payload. Payload MD5 is
671ef132e3532dbb3985b4f20bb85226, matching the pre-existing download log.
Extracted Script.pvf SHA256 is
a15e3390a4db095d53e75b12843a46d854e46b7b12ce418286d20fe5c0e64cfb.
Local provenance is recorded in the round-six evidence; this run did not
re-download or independently reauthenticate the historical CDN response.

From go-server:

```text
go run ./cmd/tools/lukesingleindex -input <current.pvf> -reference <upstream.pvf> -report <new-report.json>
go run ./cmd/tools/lukesingleindex -input <current.pvf> -reference <upstream.pvf> -output <new-candidate.pvf> -report <new-report.json>
```

No destination may already exist. The candidate is verified in memory before
writing: index restoration, archive preservation, both raid-family dependency
checks and all 12 Luke layouts. The report records both input hashes and output
hash. It is an **index-only research candidate**, not a deployment command.
An error writing the report after the candidate may leave the verified candidate
on disk; both paths are exclusive-create and reruns never overwrite it.

Upstream and current maps 95310/95311 both lack `[pathgate recognize range]`.
Their bytes are preserved. `native_ready` is always false; native doorway
traversal, current EXE loading, combat, rewards and the player's original start
failure are not proven by this tool. Do not remove the older scratch tool's
native-door gate or describe index restoration as a complete gameplay fix.

Tests use LUKE_INPUT_PVF and LUKE_REFERENCE_PVF, with the same pinned hashes.
`go test -count=1 -v ./cmd/tools/lukesingleindex` tests restoration and adverse
evidence. LUKE_TEST_BASELINE=1 applies the same postcondition to the unmodified
archive and must fail at absent map 95267. No archive file is changed by tests.
