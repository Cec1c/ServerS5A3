package dungeon

import (
	"context"
	"math"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/premium"
	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// 异次元裂缝（`[dimension dungeon]`）的每日入场次数是**角色级共享池**，不是按副本各记一份。
//
// 厂商依据（S4A21 参考服务端）：
//
//	`Game\Dungeon\DungeonEntryLimitService.cs`
//	  `character_dimensiongate_records(character_id PRIMARY KEY, day_id, current_count,
//	  extra_count, used_count)` —— 记录按 character_id 建主键，没有 dgn_id 列；
//	  `TryConsumeDimensionGateLimit` 按角色读写同一行。
//	`Network\Handlers\Dungeon\DungeonEntryHandler.cs:4405-4432/4480-4519`
//	  准入 `TryCheckDimensionGateLimit(...)`、入场 `TryConsumeDimensionGateLimit(...)`，
//	  两者只在 `DungeonData.IsDimensionDungeon(dungeonId)`（= DGN `[dimension dungeon]`）为真时走。
//	`Game\Dungeon\DimensionGateEntryLimitConfigProvider.cs`
//	  `etc/dimensiongate.etc` 的 `[daily default enter count]` / `[daily default extra enter count]`；
//	  本仓真档读到 5 / 0（见 dnfbridge `currentDimensionGateEntryLimitForSession`）。
//
// 额度用尽即拒绝入场；计数与**同一轮入场的其他资产**（门票、入场房间疲劳）必须在同一个
// `WithinCharacterAssets` 事务里提交，否则拒绝分支会留下已经扣掉的疲劳或门票。
// 领域持久化额度与准入判据；bridge按已验证的812契约投影基础/追加/周计数。
const (
	dimensionGateDayKey   = "dimension_gate_daily_entry_day"
	dimensionGateUsedKey  = "dimension_gate_daily_entry_used"
	dimensionGateExtraKey = "dimension_gate_daily_entry_extra_granted"
)

// DimensionGateDailyUsed 沿用既有 06:00（UTC+8）服务日口径。读到新的一天不写库；
// 第一次成功入场才提交换日后的计数。
func DimensionGateDailyUsed(character dnfrepo.CharacterRecord, now time.Time) (int64, error) {
	if character.Stats[dimensionGateDayKey] != premium.ServiceDayID(now) {
		return 0, nil
	}
	used := character.Stats[dimensionGateUsedKey]
	if used < 0 {
		return 0, ErrEntryItemConsumeInvalid
	}
	return used, nil
}

// DimensionGateDailyAllowance returns the used count and earned allowance for
// this service day. Extra is the cumulative grant, not the unspent balance:
// spending an entry must never reopen the daily ticket grant limit.
// Missing extra state preserves records created before tickets were supported.
func DimensionGateDailyAllowance(character dnfrepo.CharacterRecord, now time.Time, base, extraLimit int64) (used, extra, total int64, err error) {
	if now.IsZero() || base <= 0 || extraLimit < 0 || extraLimit > math.MaxInt64-base {
		return 0, 0, 0, ErrEntryItemConsumeInvalid
	}
	used, err = DimensionGateDailyUsed(character, now)
	if err != nil {
		return 0, 0, 0, err
	}
	if character.Stats[dimensionGateDayKey] == premium.ServiceDayID(now) {
		extra = character.Stats[dimensionGateExtraKey]
	}
	if extra < 0 || extra > extraLimit {
		return 0, 0, 0, ErrEntryItemConsumeInvalid
	}
	return used, extra, base + extra, nil
}

type DimensionGateEntryCommand struct {
	CharacterID string
	Limit       int64
	// ExtraLimit is the verified daily ticket grant cap; zero disables grants.
	// Limit remains the initial allowance, never the maximum including tickets.
	ExtraLimit int64
	// Requirements / FreePasses / Project 与本仓既有每日次数命令同义：DGN 声明的
	// `ConsumeOnEntry` 门票在同一事务里扣除，失败则整轮拒绝且不写任何字节。
	Requirements []EntryItemRequirement
	FreePasses   []EntryItemRequirement
	UpdatedAt    time.Time
	Project      EntryItemProjector
	// InTransaction 在同一角色事务内、额度判据之后、计数写入之前执行，供桥侧把
	// 入场房间疲劳这类同批资产并入本次提交（全部成功才提交，任一失败全部回滚）。
	InTransaction func(character *dnfrepo.CharacterRecord) error
}

type DimensionGateEntryResult struct {
	Character dnfrepo.CharacterRecord
	Used      int64
	Consumed  []EntryItemConsumedStack
}

// ConsumeDimensionGateEntry 在单次角色资产事务里提交共享日计数与同批入场资产。
// 不发放任何奖励、不改任务。
func (o *Owner) ConsumeDimensionGateEntry(ctx context.Context, cmd DimensionGateEntryCommand) (DimensionGateEntryResult, error) {
	if o == nil || o.assets == nil || strings.TrimSpace(cmd.CharacterID) == "" || cmd.Limit <= 0 || cmd.UpdatedAt.IsZero() {
		return DimensionGateEntryResult{}, ErrEntryItemConsumeInvalid
	}
	ctx = contextOrBackground(ctx)
	var result DimensionGateEntryResult
	err := o.assets.WithinCharacterAssets(ctx, cmd.CharacterID, func(chars dnfrepo.CharacterRepository, bags dnfrepo.InventoryRepository, _ dnfrepo.EquipmentRepository) error {
		character, found, err := chars.Load(ctx, cmd.CharacterID)
		if err != nil {
			return err
		}
		if !found {
			return ErrCharacterNotFound
		}
		character = dnfrepo.CloneCharacter(character)
		used, extra, total, err := DimensionGateDailyAllowance(character, cmd.UpdatedAt, cmd.Limit, cmd.ExtraLimit)
		if err != nil {
			return err
		}
		if used >= total {
			return ErrDailyEntryLimit
		}
		alternatives, err := DailyEntryAlternatives(cmd.Requirements, cmd.FreePasses, 0, 0)
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
		if cmd.InTransaction != nil {
			if err := cmd.InTransaction(&character); err != nil {
				return err
			}
		}
		if len(consumed) > 0 {
			inventory.UpdatedAt = cmd.UpdatedAt
			if err := dnfrepo.SaveInventoryFields(ctx, bags, inventory, dnfrepo.InventoryFieldSlots); err != nil {
				return err
			}
		}
		if character.Stats == nil {
			character.Stats = map[string]int64{}
		}
		character.Stats[dimensionGateDayKey] = premium.ServiceDayID(cmd.UpdatedAt)
		character.Stats[dimensionGateUsedKey] = used + 1
		character.Stats[dimensionGateExtraKey] = extra
		character.UpdatedAt = cmd.UpdatedAt
		if err := dnfrepo.SaveCharacterFields(ctx, chars, character, dnfrepo.CharacterFieldStats); err != nil {
			return err
		}
		result.Character, result.Used, result.Consumed = character, used+1, consumed
		return nil
	})
	if err != nil {
		return DimensionGateEntryResult{}, err
	}
	return result, nil
}
