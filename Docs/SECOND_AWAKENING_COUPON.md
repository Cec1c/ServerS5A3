# Second-awakening completion coupons

## Problem and behavior

The client emits action 121 in a 19-byte op515 request for a second-awakening coupon. The existing decoder accepts actions 118/169/179 and does not dispatch action 121. This patch routes the exact supported shape through the authenticated character session and the existing character-settlement transaction.

Ordinary classes must already have first awakening, be at least level 75, and have a valid PVF class/skill profile plus a terminal second-awakening quest. Validate the actual inventory slot, PVF action, job/branch exclusions, instance expiry/lock and ready town scene. Preserve each item's usable-place definition: item 10164039 allows village only, so Seria's room remains rejected. Dungeon, raid and PVP use remain rejected. Self-awakening for external classes and lower-level special variants are not inferred from a permissive client flag.

The transaction persists character/skills/quests and consumes exactly one coupon, without quest currency/material rewards. Duplicate and concurrent uses cannot consume again after awakening. Send the native action result and existing inventory/character/skill/quest projections after commit; a projection failure must not replay the asset mutation. Release the asset reservation before acquiring projection locks or waiting on network writes.

A second-awakening marker on a Question quest does not invent a choice. Require an already-completed, explicitly answered Question with consistent optional answer metadata. This guard is self-contained and does not depend on the separate level-up-ticket fix.

## Protocol evidence

For EXE SHA256 a751cccfe3fb9f7a3730393fdfb32d0546222b55a5a254fc4f2416f6869fbdf6, RVA 0x20edd3e emits action121 with the action118 shape: slot:i16, list:u8, zero:u32, action:u32, two reserved zero:u32 fields (19 bytes total). The native success result is 8 bytes; failure is 6 bytes. No EXE, DLL or PVF modification is required by this patch.

## Validation boundaries

Regression tests cover request shape, source identity, ownership, level/stage/profession gates, missing resources, expiry, duplicate use, rollback, concurrent SQL commits, exact resource definitions and Question protection. Real-resource and SQL tests are opt-in; skipped fixtures are not counted as passes. SQL tests require a disposable local MariaDB and only create/drop their own scratch schema.

Native acceptance of the same coupon implementation on a private integration build: two ordinary-class fixtures completed second awakening and retained it after full relogin; a live ordinary-class role used item10164039, consumed one, used a second-awakening skill in a dungeon, received the first-awakening-only rejection on reuse without consumption, and retained awakening after a full relogin. Local low-level refusal was also observed. This does not establish every class/item variant or native acceptance of unrelated changes in the latest upstream base. No player identifiers, resources, credentials or private integration features are included.

No schema migration. A code rollback must preserve committed character progress; do not restore an old full database over later gameplay.


## 2026-10-25 补充

二次觉醒券提交后从同一次提交先发送活动任务快照，再发送已完成表和可接列表，避免空活动列表清空客户端完成缓存。补充真实PVF下的报文顺序与重复使用零变更回归。
