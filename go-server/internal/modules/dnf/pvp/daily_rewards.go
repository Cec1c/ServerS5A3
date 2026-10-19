package pvp

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

func dailyPendingKey(id uint16) string { return "pvp_daily_item_pending_" + strconv.Itoa(int(id)) }

// 在跨日换卡前持久化已完成的物品权益。背包满时保留待发数量，
// queued只表示已登记权益，paid只有真实物品和账本一同提交后才置1。
func (c *Catalog) queueDailyItems(stats map[string]int64) (bool, error) {
	changed := false
	for tier, pool := range c.Pools {
		for _, m := range pool {
			if m.RewardItemID == 0 || stats[dailyKey(tier, "id")] != int64(m.ID) {
				continue
			}
			queued := stats[dailyKey(tier, "queued")]
			if queued != 0 && queued != 1 {
				return false, fmt.Errorf("每日物品奖励登记标志无效")
			}
			if queued == 1 || stats[dailyKey(tier, "paid")] == 1 || stats[dailyKey(tier, "progress")] != int64(m.Target) {
				continue
			}
			pending := stats[dailyPendingKey(m.ID)]
			if pending < 0 || m.RewardItemCount <= 0 || pending > math.MaxInt32-m.RewardItemCount {
				return false, fmt.Errorf("每日物品待发数量无效")
			}
			stats[dailyPendingKey(m.ID)] = pending + m.RewardItemCount
			stats[dailyKey(tier, "queued")] = 1
			changed = true
		}
	}
	return changed, nil
}

type DailyRewards struct {
	Snapshot
	Items            map[int16]repo.ItemStack
	Granted, Pending []uint16
}

// 已提交任务进度为唯一发奖依据。仓储事务同时保存物品、待发余额和领奖标志；
// 断线只重发当前绝对库存，不新增权益，跨日背包满也不会丢失奖励。
func GrantDailyItems(ctx context.Context, repos repo.Group, c *Catalog, accountID, characterID string, now time.Time, place TotalRewardPlacement) (DailyRewards, error) {
	var result DailyRewards
	if c == nil || repos.CharacterSettlement == nil || accountID == "" || characterID == "" || now.IsZero() {
		return result, fmt.Errorf("每日物品奖励依赖或身份缺失")
	}
	err := repos.CharacterSettlement.WithinCharacterSettlement(ctx, characterID, func(tx repo.Group) error {
		if tx.Character == nil || tx.Inventory == nil {
			return fmt.Errorf("每日奖励仓储不可用")
		}
		character, found, err := tx.Character.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !found || character.AccountID != accountID {
			return fmt.Errorf("每日奖励角色归属不符")
		}
		snap, changed, err := c.snapshot(character, now)
		if err != nil {
			return err
		}
		result.Snapshot = snap
		stats := result.Character.Stats
		var inventory repo.InventoryRecord
		loaded, dirty := false, false
		rewardIDs := map[int64]bool{}
		for tier, pool := range c.Pools {
			for _, m := range pool {
				if m.RewardItemID == 0 {
					continue
				}
				rewardIDs[m.RewardItemID] = true
				pending := stats[dailyPendingKey(m.ID)]
				if pending < 0 || pending > math.MaxInt32 {
					return fmt.Errorf("每日待发物品数量无效")
				}
				if pending == 0 {
					continue
				}
				if place == nil {
					return fmt.Errorf("每日奖励物品放置规则缺失")
				}
				if !loaded {
					inventory, found, err = tx.Inventory.Load(ctx, characterID)
					if err != nil {
						return err
					}
					if !found || inventory.CharacterID != characterID {
						return fmt.Errorf("每日奖励背包归属不符")
					}
					loaded = true
				}
				next, err := place(repo.CloneInventory(inventory), m.RewardItemID, pending, now)
				if errors.Is(err, ErrTotalRewardInventoryFull) {
					result.Pending = append(result.Pending, m.ID)
					continue
				}
				if err != nil {
					return err
				}
				if next.CharacterID != characterID {
					return fmt.Errorf("每日奖励物品归属改变")
				}
				inventory, dirty = next, true
				stats[dailyPendingKey(m.ID)] = 0
				if stats[dailyKey(tier, "id")] == int64(m.ID) && stats[dailyKey(tier, "queued")] == 1 && stats[dailyKey(tier, "progress")] == int64(m.Target) {
					stats[dailyKey(tier, "paid")] = 1
				}
				result.Granted = append(result.Granted, m.ID)
				changed = true
			}
		}
		if dirty {
			if err := repo.SaveInventoryFields(ctx, tx.Inventory, inventory, repo.InventoryFieldSlots); err != nil {
				return err
			}
		}
		if changed {
			if err := repo.SaveCharacterFields(ctx, tx.Character, result.Character, repo.CharacterFieldStats); err != nil {
				return err
			}
		}
		if len(rewardIDs) > 0 && !loaded {
			inventory, _, err = tx.Inventory.Load(ctx, characterID)
			if err != nil {
				return err
			}
		}
		result.Items = map[int16]repo.ItemStack{}
		for key, stack := range inventory.Slots {
			if !rewardIDs[stack.ItemID] {
				continue
			}
			var list, slot int
			if _, err := fmt.Sscanf(key, "%d:%d", &list, &slot); err == nil && list == 0 && slot >= 0 && slot <= math.MaxInt16 {
				result.Items[int16(slot)] = stack
			}
		}
		return nil
	})
	if err != nil {
		return DailyRewards{}, err
	}
	return result, nil
}
