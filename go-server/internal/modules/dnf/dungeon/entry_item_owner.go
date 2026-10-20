package dungeon

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

var (
	// ErrEntryItemConsumeInvalid 表示调用方给了一条无法执行的消耗要求
	// （角色缺失 / ItemID<=0 / Count<=0）。
	ErrEntryItemConsumeInvalid = errors.New("dnf dungeon entry item consume command is invalid")
	// ErrEntryItemConsumeInsufficient 表示**在事务内**发现持仓不足。
	// 这是硬失败：整个事务回滚，调用方必须以准入拒绝回应，绝不允许部分扣减。
	ErrEntryItemConsumeInsufficient = errors.New("dnf dungeon entry item holding is insufficient")
)

// EntryItemProjector 把"被扣减后的槽位"投影成调用方要回给客户端的 wire 行。
// 语义与 `ReviveCoinProjector` 相同：只在**非删除**（Count 仍 > 0）的槽位上调用一次。
type EntryItemProjector func(slot int16, stack dnfrepo.ItemStack) (dnfrepo.ItemStack, error)

// EntryItemConsumeCommand 是"入场即消耗 `[required item]`"的一次事务输入。
//
// 语义来源（厂商，逐行）：
//
//	`Tool\PvfLib\Models\DungeonFile.cs:921-958` 解析 `[required item] ItemId Count Flag`，
//	  `:938 consumeOnEntry = values[index+2] == 1`。
//	`Network\Handlers\Dungeon\DungeonEntryAdmissionApplicationService.cs:170-183`
//	  以 `tournamentDefinition.EntryItems` 收取，**在进场事务里**通过统一背包入口扣除。
//	`Docs\副本架构业务接入规范.md:862`
//	  "入场物品按 Definition 在进图时通过统一背包入口扣除并持久化，失败同时恢复实体主背包槽
//	   与主虚拟槽；回城、失败或放弃不返还。"
type EntryItemConsumeCommand struct {
	CharacterID string
	// Requirements 是**已按 PVF 解析**的 `[required item]` 列表；只有
	// `ConsumeOnEntry == true` 且 ItemID/Count 为正的条目会真的扣。
	Requirements []EntryItemRequirement
	UpdatedAt    time.Time
	// Project 可选；给定时会为每个"扣完还剩下"的槽位调用一次。
	Project EntryItemProjector
}

// EntryItemConsumeResult 是一次入场消耗的逐条回执。
type EntryItemConsumeResult struct {
	// Changed 为真表示本次事务真的改动了背包（全部要求都已在同一事务里扣完）。
	Changed bool
	// Consumed 是逐条回执，顺序与命令里**实际生效**的 Requirements 一致。
	Consumed []EntryItemConsumedStack
}

// EntryItemConsumedStack 是一条 `[required item]` 的扣减回执。
type EntryItemConsumedStack struct {
	ItemID int64
	Count  int64
	// Slots 是被改动的槽位（按扣减顺序）；Removed 标记该槽位被删空。
	Slots []EntryItemConsumedSlot
}

// EntryItemConsumedSlot 是单个槽位的变化。
type EntryItemConsumedSlot struct {
	SlotKey    string
	Slot       int16
	CountAfter int64
	Removed    bool
}

// ConsumeEntryItems 在**同一个** `WithinCharacterItems` 事务里扣掉全部
// `ConsumeOnEntry` 的 `[required item]`。
//
// 三条硬规矩（对应任务书"不许静默假成功 / 不许静默扣 0"）：
//  1. 任何一条持仓不足 ⇒ 返回 `ErrEntryItemConsumeInsufficient`，**整事务回滚**
//     （不会有"扣了第一条、第二条不够"的半成品）。
//  2. 一条要求都执行不了（Requirements 为空 / 全部不消费）⇒ `Changed=false`，
//     不写库、不报错（这是绝大多数副本的常态）。
//  3. 不会把"找不到槽位"当成"已扣 0"：找不到就是不足。
//
// 槽位选择：与 `ConsumeReviveCoin`（`revive_coin_owner.go:70-83`）同一策略 ——
// 按槽位号从小到大扣，允许跨多个槽位凑够数量。
func (o *Owner) ConsumeEntryItems(
	ctx context.Context,
	cmd EntryItemConsumeCommand,
) (EntryItemConsumeResult, error) {
	result := EntryItemConsumeResult{}
	if o == nil || o.items == nil || strings.TrimSpace(cmd.CharacterID) == "" {
		return result, ErrEntryItemConsumeInvalid
	}
	requirements := make([]EntryItemRequirement, 0, len(cmd.Requirements))
	for _, requirement := range cmd.Requirements {
		if requirement.ItemID <= 0 || requirement.Count <= 0 || !requirement.ConsumeOnEntry {
			continue
		}
		requirements = append(requirements, requirement)
	}
	if len(requirements) == 0 {
		return result, nil
	}
	ctx = contextOrBackground(ctx)
	now := updatedAtOrNow(cmd.UpdatedAt)

	err := o.items.WithinCharacterItems(ctx, cmd.CharacterID, func(
		inventories dnfrepo.InventoryRepository,
		_ dnfrepo.EquipmentRepository,
	) error {
		inventory, found, err := inventories.Load(ctx, cmd.CharacterID)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("%w: character=%s", ErrEntryItemConsumeInsufficient, cmd.CharacterID)
		}
		inventory = dnfrepo.CloneInventory(inventory)
		if inventory.Slots == nil {
			inventory.Slots = make(map[string]dnfrepo.ItemStack)
		}
		consumed := make([]EntryItemConsumedStack, 0, len(requirements))
		for _, requirement := range requirements {
			slots, err := consumeEntryItemRequirement(&inventory, requirement, cmd.Project)
			if err != nil {
				return err
			}
			consumed = append(consumed, EntryItemConsumedStack{
				ItemID: requirement.ItemID,
				Count:  requirement.Count,
				Slots:  slots,
			})
		}
		inventory.UpdatedAt = now
		result.Changed = true
		result.Consumed = consumed
		return dnfrepo.SaveInventoryFields(ctx, inventories, inventory, dnfrepo.InventoryFieldSlots)
	})
	if err != nil {
		return EntryItemConsumeResult{}, err
	}
	return result, nil
}

// consumeEntryItemRequirement 在一个**已经被克隆**的背包上扣掉一条要求。
// 持仓不足时返回 `ErrEntryItemConsumeInsufficient`（调用方会让整个事务回滚）。
func consumeEntryItemRequirement(
	inventory *dnfrepo.InventoryRecord,
	requirement EntryItemRequirement,
	project EntryItemProjector,
) ([]EntryItemConsumedSlot, error) {
	if inventory == nil {
		return nil, ErrEntryItemConsumeInvalid
	}
	remaining := requirement.Count
	slots := make([]EntryItemConsumedSlot, 0, 1)
	for remaining > 0 {
		key, stack, ok := lowestEntryItemSlot(inventory, requirement.ItemID)
		if !ok {
			return nil, fmt.Errorf(
				"%w: item=%d required=%d short_by=%d",
				ErrEntryItemConsumeInsufficient,
				requirement.ItemID,
				requirement.Count,
				remaining,
			)
		}
		take := stack.Count
		if take > remaining {
			take = remaining
		}
		remaining -= take
		stack.Count -= take
		if stack.Extra == nil {
			stack.Extra = make(map[string]string, 4)
		}
		stack.Extra["count"] = strconv.FormatInt(stack.Count, 10)
		stack.Extra["amount_or_count"] = strconv.FormatInt(stack.Count, 10)
		stack.Extra["last_consume_source"] = "dungeon_entry_required_item"
		slot, _ := parseMainInventorySlotKey(key)
		entry := EntryItemConsumedSlot{
			SlotKey:    key,
			Slot:       slot,
			CountAfter: stack.Count,
		}
		if stack.Count <= 0 {
			delete(inventory.Slots, key)
			entry.CountAfter = 0
			entry.Removed = true
			slots = append(slots, entry)
			continue
		}
		if project != nil {
			projected, err := project(slot, stack)
			if err != nil {
				return nil, err
			}
			stack = projected
		}
		inventory.Slots[key] = cloneStack(stack)
		slots = append(slots, entry)
	}
	return slots, nil
}

// lowestEntryItemSlot 找该道具在**主背包列表**里槽位号最小的那一格。
// 复用 `parseMainInventorySlotKey`（`revive_coin_owner.go:128-142`）：
// 非主背包列表（仓库/账号库）不参与入场消耗 —— 与准入的持仓口径一致
// （`dnfbridge.currentCharacterItemCounts` 也只数 `record.Slots`）。
func lowestEntryItemSlot(
	inventory *dnfrepo.InventoryRecord,
	itemID int64,
) (string, dnfrepo.ItemStack, bool) {
	if inventory == nil {
		return "", dnfrepo.ItemStack{}, false
	}
	bestKey := ""
	bestSlot := int16(0)
	var best dnfrepo.ItemStack
	for key, stack := range inventory.Slots {
		slot, parsed := parseMainInventorySlotKey(key)
		if !parsed || stack.ItemID != itemID || stack.Count <= 0 {
			continue
		}
		if bestKey == "" || slot < bestSlot {
			bestKey = key
			bestSlot = slot
			best = cloneStack(stack)
		}
	}
	if bestKey == "" {
		return "", dnfrepo.ItemStack{}, false
	}
	return bestKey, best, true
}
