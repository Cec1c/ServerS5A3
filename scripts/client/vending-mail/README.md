# Black-diamond vending full-page rejection

## Native boundary

The protected V179-family client used for the historical probe has SHA256
`15dfd6a8adf6f4d0f815bb3313642c2a2971d5ff6bd2aefb44830776a522b68e`. Its
original CMD215 settlement does not have a display-only or mail-only branch for
an embedded row whose slot is zero. It calls the inventory lookup with slot 0
and then passes `[0, reward, 1, 1]` to the inventory insertion function.

Therefore the server must not encode a mail-only reward as a 119-byte item row
at synthetic slot 0. Slot zero is a real inventory key in this client flow, not
a presentation sentinel.

## Selected behavior

A full reward segment rejects the paid draw with the already established
two-byte CMD215 failure body `[00 01]`. The coin debit, reward placement and
mail creation share no forwarded side effects: the transaction returns
`dungeon.ErrInventoryFull` before inventory save, so the coin remains in place
and no mail is created. Normal-capacity draws continue to return the real
committed inventory slot and settlement row.

The domain's explicit overflow mode remains available to its existing
transaction tests, including mail-save rollback, but this CMD215 bridge does
not enable it. No client-facing path reaches a `RewardSlot=-1` settlement row.

No client patch or builder is supplied. The hash-matched historical input has no
proven safe display-only branch, and changing the native settlement to suppress
insertion would require an unverified branch that could separate the HUD row
from the client inventory object. This directory instead supplies an exact
input-hash verifier so the main controller can verify the chosen failure and a
normal nonzero-slot control against the original client control flow.

## Verification

`verify_reject_no_phantom.py` requires the final protected SHA256 above and
uses the existing matched `SettlementLab`. It does not treat every slot as a
valid reward: only the known coin slot 121 and explicit test reward slot 79 are
resolved. It requires:

- a slot-79 control performs lookup 79, insertion 79 and one settlement row;
- failure body `[00 01]` performs no slot-0 lookup, no inventory insertion, no
  coin-removal effect, and creates no display or settlement rows.

The inventory adapter is keyed by the exact slot and starts with only the coin.
The merged-reward control explicitly seeds its real reward slot. When
`DNF_VENDING_NATIVE_EXPORT` points to the current Go test exports, every real
PVF reward reply is replayed and checked for its exact inserted slot, item,
count/quality, one displayed reward and preservation of the coin slot. Failure
also requires no coin setter call; absence of deletion alone is insufficient.

The matched client's native error-byte classifier exempts CMD215 from the
generic error read. For `[00 01]`, the dispatcher consumes status `00`, invokes
the settlement callback with `(215, 0, 0)`, and leaves the trailing `01` byte
unread. The verifier checks this exact behavior and does not replace the
classifier with a stub to force full consumption. This evidence establishes
the absence of asset/display effects; it does not establish a specific
inventory-full error message in this historical client.

This is a historical verifier, not a new client release. It does not apply to V201 by changing the expected hash. GUI acceptance and any future release require separate evidence.

## Public source usage

Historical input hashes remain mandatory; no EXE is included or installed. Use Python 3.10+ with `unicorn` 2.x and `capstone` 5.x in your environment (and `pefile` for the shared settlement harness). The `raid-partyface/pe_support.py` and `test_vending_settlement_v179.py` helpers already belong to this public repository. No workstation dependency path is required.
