package dungeon

import (
	"context"
	"strings"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// EntryAssetsCommand 只提交"入场资产"：DGN `[required item]` 里 `ConsumeOnEntry == 1` 的门票
// 与 `InTransaction` 钩子里的同批资产（例如入场房间疲劳）。它**不写每日次数**。
//
// 用途：按**通关**计数的副本（艾肯，见 `etc/dungeoncyclesystem.etc` 与
// `n_quest/new_quest/daily_90_legend*.qst` 的 `[condition under clear]` 任务）。
// 入场只扣门票/疲劳，次数在通关时由部署侧另行提交，因此中止的入场不会吃掉当天次数。
type EntryAssetsCommand struct {
	CharacterID   string
	Requirements  []EntryItemRequirement
	FreePasses    []EntryItemRequirement
	UpdatedAt     time.Time
	Project       EntryItemProjector
	InTransaction func(character *dnfrepo.CharacterRecord) error
	// AllowanceDungeonID/AllowanceLimit 非零时，在**同一事务内**复核「当日次数还没用尽」，
	// 用尽即整体拒绝：与门票/疲劳同批，拒绝不会留下已扣资产，也不会出现
	//「prepare 通过、commit 后额度被别人抢走」的窗口。
	AllowanceDungeonID int64
	AllowanceLimit     int64
	// PrerequisiteDungeonID/PrerequisiteCount 非零时，在同一事务内要求
	// `DailyEntryUsed(character, PrerequisiteDungeonID) >= PrerequisiteCount`（真档每日任务：
	// 试炼 5098/3571 需要当日通关遗落的神殿 5097/3570 三次）。不满足即整批拒绝，不扣票/疲劳。
	PrerequisiteDungeonID int64
	PrerequisiteCount     int64
}

type EntryAssetsResult struct {
	Character dnfrepo.CharacterRecord
	Consumed  []EntryItemConsumedStack
}

func (o *Owner) ConsumeEntryAssets(ctx context.Context, cmd EntryAssetsCommand) (EntryAssetsResult, error) {
	if o == nil || o.assets == nil || strings.TrimSpace(cmd.CharacterID) == "" || cmd.UpdatedAt.IsZero() {
		return EntryAssetsResult{}, ErrEntryItemConsumeInvalid
	}
	ctx = contextOrBackground(ctx)
	var result EntryAssetsResult
	err := o.assets.WithinCharacterAssets(ctx, cmd.CharacterID, func(chars dnfrepo.CharacterRepository, bags dnfrepo.InventoryRepository, _ dnfrepo.EquipmentRepository) error {
		character, found, err := chars.Load(ctx, cmd.CharacterID)
		if err != nil {
			return err
		}
		if !found {
			return ErrCharacterNotFound
		}
		character = dnfrepo.CloneCharacter(character)
		if cmd.AllowanceDungeonID > 0 && cmd.AllowanceLimit > 0 {
			used, err := DailyEntryUsed(character, cmd.AllowanceDungeonID, cmd.UpdatedAt)
			if err != nil {
				return err
			}
			if used >= cmd.AllowanceLimit {
				return ErrDailyEntryLimit
			}
		}
		if cmd.PrerequisiteDungeonID > 0 && cmd.PrerequisiteCount > 0 {
			cleared, err := DailyEntryUsed(character, cmd.PrerequisiteDungeonID, cmd.UpdatedAt)
			if err != nil {
				return err
			}
			if cleared < cmd.PrerequisiteCount {
				return ErrEntryPrerequisiteMissing
			}
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
		if cmd.InTransaction != nil {
			character.UpdatedAt = cmd.UpdatedAt
			if err := dnfrepo.SaveCharacterFields(ctx, chars, character, dnfrepo.CharacterFieldStats); err != nil {
				return err
			}
		}
		result.Character, result.Consumed = character, consumed
		return nil
	})
	if err != nil {
		return EntryAssetsResult{}, err
	}
	return result, nil
}
