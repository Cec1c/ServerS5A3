package pvp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

var ErrTotalRewardInventoryFull = errors.New("total mission reward inventory full")

type TotalRewardPlacement func(repo.InventoryRecord, int64, int64, time.Time) (repo.InventoryRecord, error)

type TotalRewards struct {
	Character        repo.CharacterRecord
	Items            map[int16]repo.ItemStack
	Granted, Pending []uint16
}

func totalRewardKey(id uint16) string { return "pvp_total_target_" + strconv.Itoa(int(id)) + "_paid" }

// GrantTotalTargets consumes committed account progress. Progress survives an
// interrupted delivery; the next result or login resumes it. Items, emblems and
// the account-wide paid marker commit together, so changing characters or
// replaying a result cannot pay a target twice. No speculative mail is created.
func GrantTotalTargets(ctx context.Context, repos repo.Group, c *Catalog, accountID, selected string, now time.Time, place TotalRewardPlacement) (TotalRewards, error) {
	var result TotalRewards
	if c == nil || repos.RentalAssets == nil || now.IsZero() {
		return result, fmt.Errorf("total reward dependencies missing")
	}
	err := repos.RentalAssets.WithinRentalAssets(ctx, accountID, selected,
		func(accounts repo.AccountRepository, characters repo.CharacterRepository, inventories repo.InventoryRepository, _ repo.EquipmentRepository) error {
			account, found, err := accounts.Load(ctx, accountID)
			if err != nil {
				return err
			}
			if !found || account.AccountID != accountID {
				return fmt.Errorf("total reward account missing")
			}
			character, found, err := characters.Load(ctx, selected)
			if err != nil {
				return err
			}
			if !found || character.AccountID != accountID || character.Stats["delete_flag"] != 0 {
				return fmt.Errorf("total reward character owner mismatch")
			}
			character = repo.CloneCharacter(character)
			if character.Stats == nil {
				character.Stats = map[string]int64{}
			}
			p, err := ReadTotalProgress(account)
			if err != nil {
				return err
			}
			rows, err := c.normalRows(totalMissionCharacter(accountID, p.Stats))
			if err != nil {
				return err
			}
			complete := map[uint16]bool{}
			for _, row := range rows {
				complete[row.ID] = row.Progress == row.Target
			}
			rewardIDs := map[int64]bool{}
			var inventory repo.InventoryRecord
			loaded, dirty := false, false
			for _, m := range c.Normal {
				paid := p.Stats[totalRewardKey(m.ID)]
				if paid != 0 && paid != 1 || paid == 1 && !complete[m.ID] {
					return fmt.Errorf("invalid total reward marker %d", m.ID)
				}
				if m.RewardItemID > 0 {
					rewardIDs[m.RewardItemID] = true
				}
				if paid == 1 || !complete[m.ID] || m.PreRequired != 0 && !complete[m.PreRequired] {
					continue
				}
				balance := character.Stats[EmblemsKey]
				if m.Emblems < 0 || m.Emblems > math.MaxInt32 || balance < 0 || balance > math.MaxInt32-m.Emblems {
					return fmt.Errorf("total reward emblem overflow")
				}
				if m.RewardItemID > 0 {
					if place == nil {
						return fmt.Errorf("total mission item placement missing")
					}
					if !loaded {
						inventory, found, err = inventories.Load(ctx, selected)
						if err != nil {
							return err
						}
						if !found || inventory.CharacterID != selected {
							return fmt.Errorf("total reward inventory missing")
						}
						loaded = true
					}
					next, err := place(repo.CloneInventory(inventory), m.RewardItemID, m.RewardItemCount, now)
					if errors.Is(err, ErrTotalRewardInventoryFull) {
						result.Pending = append(result.Pending, m.ID)
						continue
					}
					if err != nil {
						return err
					}
					if next.CharacterID != selected {
						return fmt.Errorf("total reward inventory owner changed")
					}
					inventory, dirty = next, true
				}
				character.Stats[EmblemsKey] = balance + m.Emblems
				p.Stats[totalRewardKey(m.ID)] = 1
				result.Granted = append(result.Granted, m.ID)
			}
			if len(result.Granted) > 0 {
				if dirty {
					if err := repo.SaveInventoryFields(ctx, inventories, inventory, repo.InventoryFieldSlots); err != nil {
						return err
					}
				}
				if err := repo.SaveCharacterFields(ctx, characters, character, repo.CharacterFieldStats); err != nil {
					return err
				}
				raw, err := json.Marshal(p)
				if err != nil {
					return err
				}
				if err := repo.SaveAccountMetadataEntry(ctx, accounts, account, TotalProgressMetadataKey, string(raw), now); err != nil {
					return err
				}
			}
			// Publish current absolute values also on an idempotent retry, including
			// an indeterminate commit that completed before the connection failed.
			if len(rewardIDs) > 0 && !loaded {
				inventory, _, err = inventories.Load(ctx, selected)
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
			result.Character = character
			return nil
		})
	if err != nil {
		return TotalRewards{}, err
	}
	return result, nil
}
