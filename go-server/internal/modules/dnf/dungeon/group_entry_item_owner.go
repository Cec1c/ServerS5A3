package dungeon

import (
	"context"
	"fmt"
	"strings"
	"time"

	dnfrepo "longheng.io/server/internal/modules/dnf/repository"
)

// EntryItemGroupMember binds each inventory to its authenticated character owner.
// Alternatives are ordered: a free pass precedes normal tickets. All members
// select and consume under one group inventory transaction.
type EntryItemGroupMember struct {
	CharacterID  string
	AccountID    string
	Alternatives [][]EntryItemRequirement
}

type EntryItemGroupCommand struct {
	Members   []EntryItemGroupMember
	UpdatedAt time.Time
	Project   EntryItemProjector
}

func ConsumeEntryItemGroup(ctx context.Context, repos dnfrepo.Group, cmd EntryItemGroupCommand) (map[string]EntryItemConsumeResult, error) {
	unit, ok := repos.CharacterTrade.(dnfrepo.CharacterInventoryGroupUnitOfWork)
	if !ok {
		return nil, ErrOwnerUnavailable
	}
	if len(cmd.Members) == 0 {
		return nil, ErrEntryItemConsumeInvalid
	}
	ids := make([]string, 0, len(cmd.Members))
	byID := make(map[string]EntryItemGroupMember, len(cmd.Members))
	for _, member := range cmd.Members {
		if strings.TrimSpace(member.AccountID) == "" || len(member.Alternatives) == 0 {
			return nil, ErrEntryItemConsumeInvalid
		}
		if _, duplicate := byID[member.CharacterID]; duplicate {
			return nil, ErrEntryItemConsumeInvalid
		}
		for _, alternative := range member.Alternatives {
			if len(alternative) == 0 {
				return nil, ErrEntryItemConsumeInvalid
			}
			for _, requirement := range alternative {
				if requirement.ItemID <= 0 || requirement.Count <= 0 || !requirement.ConsumeOnEntry {
					return nil, ErrEntryItemConsumeInvalid
				}
			}
		}
		ids = append(ids, member.CharacterID)
		byID[member.CharacterID] = member
	}
	ids, err := dnfrepo.CharacterInventoryGroupIDs(ids)
	if err != nil {
		return nil, err
	}
	ctx = contextOrBackground(ctx)
	results := make(map[string]EntryItemConsumeResult, len(ids))
	err = unit.WithinCharacterInventoryGroup(ctx, ids, func(characters dnfrepo.CharacterRepository, inventories dnfrepo.InventoryRepository) error {
		updated := make([]dnfrepo.InventoryRecord, 0, len(ids))
		for _, id := range ids {
			member := byID[id]
			character, found, err := characters.Load(ctx, id)
			if err != nil {
				return err
			}
			if !found || character.CharacterID != id || character.AccountID != member.AccountID {
				return fmt.Errorf("%w: character owner mismatch", ErrEntryItemConsumeInvalid)
			}
			inventory, found, err := inventories.Load(ctx, id)
			if err != nil {
				return err
			}
			if !found || inventory.CharacterID != id {
				return fmt.Errorf("%w: character=%s", ErrEntryItemConsumeInsufficient, id)
			}
			selected := -1
			for index, alternative := range member.Alternatives {
				candidate := dnfrepo.CloneInventory(inventory)
				valid := true
				for _, requirement := range alternative {
					if _, err := consumeEntryItemRequirement(&candidate, requirement, nil); err != nil {
						valid = false
						break
					}
				}
				if valid {
					selected = index
					break
				}
			}
			if selected < 0 {
				return fmt.Errorf("%w: character=%s", ErrEntryItemConsumeInsufficient, id)
			}
			inventory = dnfrepo.CloneInventory(inventory)
			result := EntryItemConsumeResult{Changed: true}
			for _, requirement := range member.Alternatives[selected] {
				slots, err := consumeEntryItemRequirement(&inventory, requirement, cmd.Project)
				if err != nil {
					return err
				}
				result.Consumed = append(result.Consumed, EntryItemConsumedStack{ItemID: requirement.ItemID, Count: requirement.Count, Slots: slots})
			}
			inventory.UpdatedAt = updatedAtOrNow(cmd.UpdatedAt)
			results[id] = result
			updated = append(updated, inventory)
		}
		for _, inventory := range updated {
			if err := dnfrepo.SaveInventoryFields(ctx, inventories, inventory, dnfrepo.InventoryFieldSlots); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return results, nil
}
