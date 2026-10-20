package repository

import (
	"fmt"
	"strings"
	"time"

	"longheng.io/server/internal/platform/db"
)

const (
	MainInventoryListType     byte  = 0
	CrystalWarehouseFirstSlot int16 = 354
	CrystalWarehouseLastSlot  int16 = 359
	SoulWarehouseFirstSlot    int16 = 360
	SoulWarehouseLastSlot     int16 = 365
)

// AccountInventoryRepository stores the account-owned portion of list type 0.
// The current client still addresses these entries as ordinary list-0 slots;
// only their durable owner changes from character to account.
type AccountInventoryRepository interface {
	db.Store[AccountInventoryRecord]
}

type AccountInventoryRecord struct {
	AccountID string               `json:"account_id"`
	Slots     map[string]ItemStack `json:"slots,omitempty"`
	UpdatedAt time.Time            `json:"updated_at,omitempty"`
}

func CloneAccountInventory(record AccountInventoryRecord) AccountInventoryRecord {
	record.Slots = cloneItemMap(record.Slots)
	return record
}

func AccountInventoryKey(record AccountInventoryRecord) string {
	return strings.TrimSpace(record.AccountID)
}

// IsAccountSharedInventorySlot reports whether a wire list/slot pair belongs
// to the account-scoped crystal or soul warehouse.
func IsAccountSharedInventorySlot(listType byte, slot int16) bool {
	return listType == MainInventoryListType &&
		slot >= CrystalWarehouseFirstSlot && slot <= SoulWarehouseLastSlot
}

// accountWarehouseItemSlots 是"小晶块/灵魂归属哪一格账号仓库"的**唯一映射**。
//
// 现场口径（玩家背包界面）：无色小晶块（3037）等六种晶块属于**晶块仓库**
// 354..359，灵魂属于灵魂仓库 360..365；客户端把它们渲染在 list 0 的这两段固定格
// 里，而 main bag 的材料页（121..176）**不是**它们的家。2026-09-29 现场：
// GM 面板发"无色小晶块"邮件 ⇒ 玩家领取 ⇒ 客户端把它写进主背包材料页
// （DB `dnf_inventory_items` 角色 1 `0:130` item 3037 count 16233，Extra 只有
// item_kind/pvf_path/stackable_type、没有 `source`，正是邮件领取
// `mailboxAttachmentStack` 的指纹）⇒ 客户端 1.3 s 后 connection reset
// （packet_log 196766 op14 行 slot=130 item=3037，196770 game-read-close）。
//
// 数量与槽位取值来源 = 运行时 PVF 的 `stackable/stackable.lst` 家族
// （material/cubepiece_{black,white,red,blue,clear,gold}.stk + soul 六件），
// 与 dnfbridge `currentDisjointWarehouseFixedSlots` 逐项一致（有对拍测试）。
var accountWarehouseItemSlots = map[int64]int16{
	3033:     CrystalWarehouseFirstSlot + 0, // cube_black
	3034:     CrystalWarehouseFirstSlot + 1, // cube_white
	3035:     CrystalWarehouseFirstSlot + 2, // cube_red
	3036:     CrystalWarehouseFirstSlot + 3, // cube_blue
	3037:     CrystalWarehouseFirstSlot + 4, // cube_clear
	3262:     CrystalWarehouseFirstSlot + 5, // cube_gold
	10100115: SoulWarehouseFirstSlot + 0,    // common soul
	10100116: SoulWarehouseFirstSlot + 1,    // advanced soul
	10099773: SoulWarehouseFirstSlot + 2,    // rare soul
	10099774: SoulWarehouseFirstSlot + 3,    // unique soul
	10099775: SoulWarehouseFirstSlot + 4,    // legendary soul
	10158124: SoulWarehouseFirstSlot + 5,    // epic soul
}

// AccountWarehouseSlotForItem 返回这件道具在账号仓库里的固定格；不在这张表里的
// 道具返回 (0, false)。它是"不管从哪条路径获得小晶块，都进晶块仓库"这条规则的
// 唯一判据。
func AccountWarehouseSlotForItem(itemID int64) (int16, bool) {
	slot, ok := accountWarehouseItemSlots[itemID]
	return slot, ok
}

// AccountWarehouseItemForSlot 是 AccountWarehouseSlotForItem 的逆映射。
func AccountWarehouseItemForSlot(slot int16) (int64, bool) {
	if !IsAccountSharedInventorySlot(MainInventoryListType, slot) {
		return 0, false
	}
	for itemID, fixed := range accountWarehouseItemSlots {
		if fixed == slot {
			return itemID, true
		}
	}
	return 0, false
}

func AccountSharedInventorySlotKey(slot int16) string {
	return fmt.Sprintf("%d:%d", MainInventoryListType, slot)
}

// MergeAccountSharedInventory produces the list-0 view for one character.
// Stale character-owned copies of slots 354..365 are removed before the
// account record is overlaid, so callers never expose two competing owners.
func MergeAccountSharedInventory(character InventoryRecord, account AccountInventoryRecord) InventoryRecord {
	merged := CloneInventory(character)
	if merged.Slots == nil {
		merged.Slots = make(map[string]ItemStack)
	}
	for slot := CrystalWarehouseFirstSlot; slot <= SoulWarehouseLastSlot; slot++ {
		delete(merged.Slots, AccountSharedInventorySlotKey(slot))
	}
	for slot := CrystalWarehouseFirstSlot; slot <= SoulWarehouseLastSlot; slot++ {
		key := AccountSharedInventorySlotKey(slot)
		if stack, ok := account.Slots[key]; ok {
			merged.Slots[key] = cloneStackRecord(stack)
		}
	}
	return merged
}

func cloneStackRecord(stack ItemStack) ItemStack {
	stack.RawEntry = append([]byte(nil), stack.RawEntry...)
	stack.Extra = cloneStringMap(stack.Extra)
	return stack
}
