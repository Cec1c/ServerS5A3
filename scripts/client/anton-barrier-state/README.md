# Anton current barrier state candidate

Status: local offline candidate, not released, deployed or accepted in the game.

The native Anton controller defaults `AC` to1 (barrier closed). A received
symbol111 updates the global store before notifying an existing controller.
If the controller does not exist yet, that callback is skipped. The original
map-matched monster update later uses AC, so the restored global state and the
monster barrier argument can disagree. This is an executable ordering case,
not proof that every reported reconnect failure has this cause.

The patch replaces the seven-byte load at VA01872744, after the existing map
comparison. It preserves the loaded AC as fallback, saves registers and flags,
and calls the original symbol getter01A57B40 for111. Values0/1 become closed/open
arguments1/0; absent manager or other values keep AC. A missing key returns0 in
the native getter and therefore gives the closed argument. Only the argument
to01872470 changes. The controller, constructor,111 callback and its UI/movie
paths remain byte-identical. No network packet, historical event, item, PVF or
server rule is changed.

Exact input: released v152 SHA256
`44aecee7c48bc1c89582ef1cd95694950569951caeef3b22ec3478c917b8ce7e`.
Output:111853568 bytes, SHA256
`aac86cd74eb2a0155f025e92c394124ffaaeceecf50479fe26e178fe2b04d661`.
Added executable/read-only section `.antbar`,53-byte helper,4096-byte allocation.
The original receiver chain, including class1/502,785 and class0/1150, is kept.

Run with Python3; verification needs Unicorn2 and pefile. Use absolute output
paths on the analysis drive, outside this source tree. All output paths must
be new; neither the installed client nor the input is overwritten.

```text
python -B patch_volcano_apply.py --input <v152.exe> --output <candidate.exe> --receipt <patch.json>
python -B verify_volcano_apply.py --baseline <v152.exe> --candidate <candidate.exe> --output <native.json> --python-deps <optional-dependency-directory>
python -B check_pe_contract.py --baseline <v152.exe> --candidate <candidate.exe> --output <pe.json> --python-deps <optional-dependency-directory>
```

The48-case executable comparison covers present/absent manager, empty or
missing111,0,1,2 and unsigned-1 values, both old latch values and matching/
nonmatching maps. It executes the original getter and iterators, map comparison,
argument load and eligible-monster loop through both27FD7E0/27F6450 calls.
World lookup/list/classification and the final setters are explicit stubs:
these tests prove arguments, not actual monster memory, rendering or damage.
Registers except intended ECX, flags, stack and controller/global state are
checked. The separate PE verifier independently checks checksum, section bounds,
original-byte edit limits and receiver-chain preservation. Rebuilding must
produce the same bytes.

The constructor-initialization experiment was rejected because changing AC
could suppress the first later111 UI edge. It is not this candidate.

Remaining acceptance: live map/controller lifetime, authoritative symbol reset
between raids, repeated entry, barrier break/recharge, native frame timing and
both players' actual damage. This patch does not repair teammate actor models,
raid portraits, guild synthesis, Aiken transactions or reward UI by itself.
