package dungeon

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/premium"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

var ErrDailyEntryLimit = errors.New("dungeon daily entry limit reached")

type DailyEntryCommand struct {
	CharacterID        string
	DungeonID          int64
	Limit              int64
	RequiredItemWeight float64
	Requirements       []EntryItemRequirement
	// FreePasses are ordered PVF alternatives with fixed counts, independent of
	// the normal ticket multiplier. They still consume one daily allowance.
	FreePasses []EntryItemRequirement
	UpdatedAt  time.Time
	Project    EntryItemProjector
	// InTransaction 在同一角色事务内、额度判据之后、计数写入之前执行，供桥侧把
	// 入场房间疲劳这类同批资产并入本次提交（全部成功才提交，任一失败全部回滚）。
	InTransaction func(character *dnfrepo.CharacterRecord) error
	// ClearNonceHi/ClearNonceLo 非零时把这次提交变成「每轮一次」的幂等通关计数：同一对 nonce 在
	// 同一个服务日已经记过就直接回放（返回已记的次数、不重复自增、不扣任何资产）。
	// nonce 必须是**每轮独立随机**的值（128bit、两个 int64 半段持久化，见桥侧
	// `ensureCurrentDungeonCycleClearNonce`），绝不能用会话内递增的计数：那种值在重登后会重复出现，
	// 会被误判成回放而漏计次数。
	ClearNonceHi int64
	ClearNonceLo int64
	// CycleEntry selects the independent native cycle admission ledger.
	CycleEntry bool
}

type DailyEntryResult struct {
	Character dnfrepo.CharacterRecord
	Used      int64
	Consumed  []EntryItemConsumedStack
}

func dailyEntryKeys(dungeonID int64) (string, string) {
	return fmt.Sprintf("dungeon_daily_entry_%d_day", dungeonID), fmt.Sprintf("dungeon_daily_entry_%d_used", dungeonID)
}

// dailyEntryNonceKeys 记录「最后一次已计入该副本每日次数的每轮 nonce」的两个半段（128bit）。
func dailyEntryNonceKeys(dungeonID int64) (string, string) {
	return fmt.Sprintf("dungeon_daily_entry_%d_nonce_hi", dungeonID), fmt.Sprintf("dungeon_daily_entry_%d_nonce_lo", dungeonID)
}

// DailyEntryUsed follows the existing 06:00 UTC+8 service day. Reading a new
// day does not write the database; the first successful entry commits the reset.
func DailyEntryUsed(character dnfrepo.CharacterRecord, dungeonID int64, now time.Time) (int64, error) {
	dayKey, usedKey := dailyEntryKeys(dungeonID)
	if character.Stats[dayKey] != premium.ServiceDayID(now) {
		return 0, nil
	}
	used := character.Stats[usedKey]
	if used < 0 {
		return 0, ErrEntryItemConsumeInvalid
	}
	return used, nil
}

// Preserve the reported rift charges: base * pow(used+1, weight), or 2/4/6
// with the current PVF weight 1. The original client reverses the operands;
// its display/precheck compatibility patch is documented in RIFT_TICKET_DISPLAY_20261017.
func DailyEntryRequirements(base []EntryItemRequirement, used int64, weight float64) ([]EntryItemRequirement, error) {
	if used < 0 || weight < 0 || math.IsNaN(weight) || math.IsInf(weight, 0) {
		return nil, ErrEntryItemConsumeInvalid
	}
	multiplier := math.Pow(float64(used+1), weight)
	out := append([]EntryItemRequirement(nil), base...)
	for i := range out {
		count := float64(out[i].Count) * multiplier
		if out[i].ItemID <= 0 || out[i].Count <= 0 || count >= float64(math.MaxInt64) || math.IsInf(count, 0) {
			return nil, ErrEntryItemConsumeInvalid
		}
		out[i].Count = int64(count)
		if out[i].Count <= 0 {
			return nil, ErrEntryItemConsumeInvalid
		}
	}
	return out, nil
}

func DailyEntryAlternatives(base, passes []EntryItemRequirement, used int64, weight float64) ([][]EntryItemRequirement, error) {
	normal, err := DailyEntryRequirements(base, used, weight)
	if err != nil {
		return nil, err
	}
	out := make([][]EntryItemRequirement, 0, len(passes)+1)
	for _, pass := range passes {
		if pass.ItemID <= 0 || pass.Count <= 0 || !pass.ConsumeOnEntry {
			return nil, ErrEntryItemConsumeInvalid
		}
		out = append(out, []EntryItemRequirement{pass})
	}
	return append(out, normal), nil
}

// ConsumeDailyEntry commits the durable counter and all entry tickets in the
// same CharacterAssets transaction. Failed inventory projection or exhausted
// allowance rolls everything back. It never grants items or modifies quests.
func (o *Owner) ConsumeDailyEntry(ctx context.Context, cmd DailyEntryCommand) (DailyEntryResult, error) {
	if o == nil || o.assets == nil || strings.TrimSpace(cmd.CharacterID) == "" || cmd.DungeonID <= 0 || cmd.Limit <= 0 || cmd.UpdatedAt.IsZero() {
		return DailyEntryResult{}, ErrEntryItemConsumeInvalid
	}
	ctx = contextOrBackground(ctx)
	var result DailyEntryResult
	err := o.assets.WithinCharacterAssets(ctx, cmd.CharacterID, func(chars dnfrepo.CharacterRepository, bags dnfrepo.InventoryRepository, _ dnfrepo.EquipmentRepository) error {
		character, found, err := chars.Load(ctx, cmd.CharacterID)
		if err != nil {
			return err
		}
		if !found {
			return ErrCharacterNotFound
		}
		character = dnfrepo.CloneCharacter(character)
		used, err := DailyEntryUsed(character, cmd.DungeonID, cmd.UpdatedAt)
		dayKey, usedKey := dailyEntryKeys(cmd.DungeonID)
		hiKey, loKey := dailyEntryNonceKeys(cmd.DungeonID)
		if cmd.CycleEntry {
			used, err = CycleEntryUsed(character, cmd.DungeonID, cmd.UpdatedAt)
			dayKey, usedKey, hiKey, loKey = cycleEntryKeys(cmd.DungeonID)
		}
		if err != nil {
			return err
		}
		if cmd.ClearNonceHi != 0 || cmd.ClearNonceLo != 0 {
			if character.Stats[hiKey] == cmd.ClearNonceHi && character.Stats[loKey] == cmd.ClearNonceLo &&
				character.Stats[dayKey] == premium.ServiceDayID(cmd.UpdatedAt) {
				// 幂等回放：这一轮已经计过，重试不得再计一次，也不扣资产。
				result.Character, result.Used = character, used
				return nil
			}
		}
		if used >= cmd.Limit {
			return ErrDailyEntryLimit
		}
		alternatives, err := DailyEntryAlternatives(cmd.Requirements, cmd.FreePasses, used, cmd.RequiredItemWeight)
		if err != nil {
			return err
		}
		inventory, found, err := bags.Load(ctx, cmd.CharacterID)
		if err != nil {
			return err
		}
		if !found && (len(cmd.Requirements) > 0 || len(cmd.FreePasses) > 0) {
			return ErrEntryItemConsumeInsufficient
		}
		inventory = dnfrepo.CloneInventory(inventory)
		consumed, err := consumeEntryAlternatives(&inventory, alternatives, cmd.Project)
		if err != nil {
			return err
		}
		result.Consumed = consumed
		if cmd.InTransaction != nil {
			if err := cmd.InTransaction(&character); err != nil {
				return err
			}
		}
		if len(result.Consumed) > 0 {
			inventory.UpdatedAt = cmd.UpdatedAt
			if err := dnfrepo.SaveInventoryFields(ctx, bags, inventory, dnfrepo.InventoryFieldSlots); err != nil {
				return err
			}
		}
		if character.Stats == nil {
			character.Stats = map[string]int64{}
		}
		character.Stats[dayKey] = premium.ServiceDayID(cmd.UpdatedAt)
		character.Stats[usedKey] = used + 1
		if cmd.ClearNonceHi != 0 || cmd.ClearNonceLo != 0 {
			character.Stats[hiKey], character.Stats[loKey] = cmd.ClearNonceHi, cmd.ClearNonceLo
		}
		character.UpdatedAt = cmd.UpdatedAt
		if err := dnfrepo.SaveCharacterFields(ctx, chars, character, dnfrepo.CharacterFieldStats); err != nil {
			return err
		}
		result.Character, result.Used = character, used+1
		return nil
	})
	if err != nil {
		return DailyEntryResult{}, err
	}
	return result, nil
}

// consumeEntryAlternatives 从 ordered 替代门票里选出**第一组可负担**的并真正扣除，返回被扣的
// 栈与槽位。一组都付不起时返回 ErrEntryItemConsumeInsufficient（调用方回滚整次入场）。
//
// 本函数是既有每日次数入场扣票逻辑的原样抽出，异次元裂缝共享池复用同一实现，
// 保证两条路径的选票/扣票语义逐字一致。
func consumeEntryAlternatives(inventory *dnfrepo.InventoryRecord, alternatives [][]EntryItemRequirement, project EntryItemProjector) ([]EntryItemConsumedStack, error) {
	selected := false
	var requirements []EntryItemRequirement
	for _, alternative := range alternatives {
		candidate := dnfrepo.CloneInventory(*inventory)
		valid := true
		for _, requirement := range alternative {
			if !requirement.ConsumeOnEntry {
				continue
			}
			if _, err := consumeEntryItemRequirement(&candidate, requirement, nil); err != nil {
				valid = false
				break
			}
		}
		if valid {
			requirements, selected = alternative, true
			break
		}
	}
	if !selected {
		return nil, ErrEntryItemConsumeInsufficient
	}
	var consumed []EntryItemConsumedStack
	for _, requirement := range requirements {
		if !requirement.ConsumeOnEntry {
			continue
		}
		slots, err := consumeEntryItemRequirement(inventory, requirement, project)
		if err != nil {
			return nil, err
		}
		consumed = append(consumed, EntryItemConsumedStack{ItemID: requirement.ItemID, Count: requirement.Count, Slots: slots})
	}
	return consumed, nil
}
