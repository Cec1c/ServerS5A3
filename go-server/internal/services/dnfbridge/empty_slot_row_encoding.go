package dnfbridge

import (
	"os"
	"strings"
)

// 本文件只有一个职责：**逐槽（class0/op14 载体，msg_id=14）行里的"这一格是空的"到底
// 该怎么编码**。它是现场故障"全身防具穿脱后耐久显示 0"的修复面。
//
// 故障链路（每一环都有线上字节）：
//
//  1. 操作者在装备栏里把一件防具脱到自己选的背包格（客户端形状
//     `src=(list0 主背包, slotN, instance=0) count=1 dst=(list3 装备, slotS, instance=<那件>)`，
//     见 worn_slot_reassert_gate.go 的长注）。
//
//  2. 服务端按权威状态回一行"那一格是空的"给**客户端指名的那个源槽**
//     （`sendCurrentSocketInventorySlotUpdate`，事件
//     `game-current-socket-inventory-slot-refresh source=login_wear_noop_after_ack_source_slot`，
//     证据串 `login_wear_noop_nominates_a_source_slot_the_client_has_no_object_for`）。
//     现场报文（char 1 / game-000027 / t=2026-09-29T09:45:11.886042431Z）：
//
//     msg_id=14 hex=... 00 0100 1200 00000000 00000000 ...（119 字节行里 itemID=0x00000000）
//
//  3. 客户端的槽位行 reader 把 itemID=0 当成**道具**（运行时 Script.pvf 里 item 0 是
//     `stackable/gold.stk`「金币」），于是那一行被实例化/覆盖到本地那一格上：行里
//     `+0x0B..0x0C`（= 耐久字）本来就是 0，客户端手里那件刚脱下来的防具因此被写成
//     "0 耐久"；再穿回身上就显示 0，而且**只有换场景/重登（整容器 + mode1 全量重放）
//     才会把客户端物品表整体重建回正确值** —— 与操作者"进副本后变满、出图也正常"
//     的观察逐条吻合。
//
// 本仓库自己已经**逐字节验证**过的空行约定不是 itemID=0，而是哨兵 `0xFFFFFFFF`：
//
//   - `internal/modules/dnf/inventory/body.go:186-237`（`emptyItemListEntryItemID`）：
//     「一行 op14 的槽位被清空时，当前 EXE 需要 itemID=0xFFFFFFFF + count=0；客户端
//     据此清掉该格」，并写明「item 0 是 `stackable/gold.stk`（金币）…itemID=0 的行
//     就被画成「金币」道具」—— 那正是本仓库已经修过一次的
//     `internal/modules/dnf/inventory/quick_slot_gold_row_test.go` 回归；
//   - dnfbridge 侧同一个约定见 `current_item_list_send.go:212`、
//     `title_book.go:140`、`pet_move_slot_refresh.go:77`、
//     `gameplay_module_emblem_compound.go:178`、`npc_shop_projection.go:199,341`、
//     `quest_finish.go:580`、`limited_cube.go:305`、`dungeon_discard.go:348`、
//     `dungeon_death_return.go:670`、`premium_inventory_activation.go:243`、
//     `aura_skin_slot.go:66`、`cera_shop_grant.go:74`（全部写 `math.MaxUint32`），
//     以及 `equip_move_refresh.go:229`、`game_aligned.go:828` 的注释口径
//     （"空槽发 0xFFFFFFFF 空行"）。
//
// 线上两种编码同时存在，量出来的（`packet_log.txt` 全量 msg_id=14 行）：
//
//	itemID=0xFFFFFFFF 空行                 40 行（bag 67/68/69/106 …）
//	itemID=0x00000000 且槽位 != 0 的行    105 行（穿脱源槽：13:46 那批 16/17/18/19/20/22/23）
//	itemID=0x00000000 且槽位 == 0 的行    184 行（**合法**：list0 槽 0 就是客户端自己的金币行）
//
// 所以"槽 0 的金币行"必须保持 itemID=0（`buildCurrentGoldStateBody` 与
// `current_item_list_account.go:109` 走的是 amount，不是空行语义，本文件不碰它们），
// 而**非 0 槽**的空行必须写哨兵。本文件只改后者。
//
// 本文件不改任何"有货"行：有货的行由 `currentItemListEntryFromStack` /
// `currentItemListEntryFromEquipment` 写权威字节（含 `+0x0B..0x0C` 耐久字），
// 那些行逐字节不变（现场实测：脱下 `[0:15 item=100170519 dur=30]`、
// 穿回 `[3:15 item=100170519 dur=30]`）。
const emptySlotRowEncodingEnv = "DNFBRIDGE_EMPTY_SLOT_ROW_ENCODING"

// emptySlotItemIDCurrentEXE 是当前客户端**已逐字节验证**的空行哨兵
// （与 inventory 包的 `emptyItemListEntryItemID`、dnfbridge 多处 `math.MaxUint32` 同值）。
const emptySlotItemIDCurrentEXE uint32 = 0xFFFFFFFF

// emptySlotItemIDLegacy 是改动前的写法：`entry.patchCore(slot, 0, 0)`
// —— itemID=0 = 运行时 PVF 的 `stackable/gold.stk`（金币）。
const emptySlotItemIDLegacy uint32 = 0

// emptySlotItemID 返回"非 0 槽的空行"该写的 itemID。
//
// 默认（未设置/空/任意其它值）= 修复档：写哨兵 `0xFFFFFFFF`。
// 显式 `legacy`（别名 `item0` / `zero` / `gold`，大小写不敏感、首尾空白忽略）= 回退档：
// 逐字节回到改动前的 `itemID=0`。
//
// 与仓库其它开关同一读法：每次调用都读 env（不缓存、不进 options.go），
// 现场改档不必重编译，回退必须**显式**写出来。
func emptySlotItemID() uint32 {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(emptySlotRowEncodingEnv))) {
	case "legacy", "item0", "item_0", "zero", "gold":
		return emptySlotItemIDLegacy
	default:
		return emptySlotItemIDCurrentEXE
	}
}

// emptySlotItemIDIsLegacy 只用于测试与诊断：回答当前是否处于回退档。
func emptySlotItemIDIsLegacy() bool {
	return emptySlotItemID() == emptySlotItemIDLegacy
}

// patchEmptySlotRow 把 entry 写成"非 0 槽的空行"：slot + 空行哨兵 + 其余全零。
//
// **只用于非 0 槽**。list 0 槽 0 的金币行走 `buildCurrentGoldStateBody` /
// `current_item_list_account.go`（itemID=0 + amount=金币），语义与编码都不同，
// 不许调用本函数。
//
// 现场归因（`packet_log.txt` 全量 msg_id=14 行，105 行"itemID=0 且槽位 != 0"）：
// **105/105 全部出自 `sendCurrentSocketInventorySlotUpdate`**，事件名一律是
// `game-current-socket-inventory-slot-refresh source=login_wear_noop_after_ack_source_slot`
// —— 也就是"玩家把装备脱到自己选的背包格之后，服务端把那一格回成空行"这一条。
// 另外两处同形空行（`guardian_gem_refresh.go` 的守护珠仓库格、
// `epic_production.go` 的史诗制作材料被扣光的那一格）在本份日志里是 **0 行**
// （功能没被触发过），但语义与编码错误完全相同、客户端反应也相同
// （item 0 = 金币），所以一并走本函数收口；`legacy` 档把它们一起逐字节还原。
func patchEmptySlotRow(entry *currentItemListEntry, slot int16) {
	entry.patchCore(slot, emptySlotItemID(), 0)
}
