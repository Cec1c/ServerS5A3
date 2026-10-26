# RaidMatchingWindow portrait namespace

The `PartyFace%c%d` control receives key/zero/zero at VA01DF054F even though
the 591 raid object stores its channel/server at +2/+3. When its local actor
lookup misses, the native CNUIFace draw routine performs an exact cache lookup
using this three-part identity. The category1 cache populated by752 and591
uses the actual namespace. A zero namespace therefore misses that profile.

This candidate binds the control to key/object.channel/object.server. It
changes no portrait frames, job/grow fields, actor tables, or packet layouts.
The helper preserves every register and flags before the original setter;
only the two intended stack arguments change. It retains the actual setter
and subsequent drawing behavior.

Exact cumulative input includes v152 job-change gates and the Anton barrier
candidate:111853568 bytes, SHA256
`aac86cd74eb2a0155f025e92c394124ffaaeceecf50479fe26e178fe2b04d661`.
Output:111857664 bytes, SHA256
`c7ba1bfe81d7283c97e33c6a3449f729b63e79a916357aa7ea69fb7754709bc8`.
The added read/execute section `.rface` holds a28-byte helper.

```text
python -B patch_partyface.py --input <cumulative.exe> --output <new.exe> --receipt <new.json>
python -B verify_partyface.py --baseline <cumulative.exe> --candidate <new.exe> --output <native.json> --python-deps <directory>
```

All outputs must be new paths outside the source tree. Verification requires
Unicorn2 and executes the original callsite, setter and cache-key arithmetic
across24 combinations of role keys, namespaces and flags. It checks the
pre-setter register state, exact stack arguments, setter memory writes, final
stack and unchanged raid object. This is not a live rendering test or proof
that every single-player black portrait has the same cause. Single-player
752/591 production has a server regression. Partial profile loading failures
remain existing behavior, not the established cause of this incident.
