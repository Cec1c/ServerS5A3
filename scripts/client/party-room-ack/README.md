# Current-image room registration observer

Paired exact-image recipe for the exact `7970c5d…` client. Build with MSVC x86 using
`build_candidate.py --input <original DNF.exe> --out <new directory>`. The builder
requires `pefile`, verifies the input hash and produces a separate executable;
it does not install or access a running game. `verify_native.py --build <directory>
--previous-receipt <previous room recovery build.json>` executes the compiled
payload and original membership/key helper paths in Unicorn.

The existing getter chain supplies a game UI thread tick. A successful local
native player registration starts a 15-second, 60-attempt window at 250 ms
spacing. Each sweep re-resolves the current local player, party, scene and room,
then uses the existing guarded NOTI120 helper to register only already-created,
unregistered current peer objects. It does not create actors, replay skill or
monster packets, reset state, or change UDP admission. The sweep continues after
a receipt, so room context becoming ready later needs no additional mode1 packet.

## Registration receipt protocol

Class 1 fixed16 opcode `0x7f14` carries a 64-byte query. Integers are little endian.

| Offset | Bytes | Meaning |
| --- | --- | --- |
| 0 | 4 | `ARQ1` |
| 4 | 1 | Version 1 |
| 5, 6 | 1 each | Receiver and peer native slots, 0–7 |
| 7 | 1 | Zero |
| 8 | 16 | Random challenge nonce |
| 24 | 16 | Eight u16 character IDs; empty slots zero |
| 40, 44 | 4 each | Server map ID and room seed |
| 48 | 8 | Receiver placement epoch |
| 56 | 4 | Low 32 bits of membership revision |
| 60 | 4 | Remaining server budget, 1–15000 ms |

An 80-byte reply echoes the query with `ARR1` magic. Byte 64 is status:
1 waiting, 2 actor absent, 3 identity mismatch, 4 insertion incomplete,
5 registered, 6 stale context, 7 expired. Bytes 65–67 are zero; offsets 68, 72,
76 hold native local registration epoch, registry token and attempt count.
The network hook only copies the bounded query into an atomic per-slot mailbox.
Observation, registration and replies occur on the game UI thread. Busy transport
retries are bounded; identical queries cannot renew their original deadline.

Status 5 requires the current local owner and peer to belong to the pinned native
registry, type `0x111`, with matching roster, actor and party identities, rechecked
after insertion callbacks. The server also requires its current session binding,
full membership revision, shared instance, both placement epochs and exact nonce.
Only this matching receipt stops that direction's bounded profile recovery.

Map ID, seed and server epoch are opaque echoed fields on the client. This is
evidence of current native registry membership under the pinned local context,
not an independent native map-ID comparison, renderer visibility, UDP delivery
guarantee, or proof of the external-server incident's root cause. Unsupported
clients simply continue the bounded server retries without a receipt.

## Image and validation boundaries

The recipe preserves the existing getter, dispatch, gate and registration hook
chains. It adds an RX payload and uses verified unused RW space; no new imports
or relocation dependency. Adjacent RX section headers are combined with their
original zero-filled virtual gap preserved. Every original mapped section byte
is compared except the four explicit hook instructions; entry/import/TLS data
remain identical. Build receipts pin both executable and payload hashes.

Native tests use explicit OS, time, transport, controller-getter and insertion
leaf fixtures. They exercise compiled dispatch/tick/ABI and original membership
checks, including late scene/room/model readiness without packets. They do not
execute the entire network parser or renderer. Game acceptance remains separate.
