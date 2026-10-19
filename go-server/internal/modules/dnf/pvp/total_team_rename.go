package pvp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	repo "longheng.io/server/internal/modules/dnf/repository"
)

const TotalTeamRenameCardID int64 = 490002850

// The native 873 request selects a real main-inventory card. Name and card
// consumption share the account/character transaction; replaying the same
// committed name never consumes another card.
func RenameTotalTeam(ctx context.Context, repos repo.Group, accountID, characterID, name string, slot uint16, now time.Time) error {
	if repos.RentalAssets == nil || accountID == "" || characterID == "" || slot > 32767 || now.IsZero() {
		return fmt.Errorf("total-match rename dependencies missing")
	}
	if err := ValidateTotalTeamName(name); err != nil {
		return err
	}
	return repos.RentalAssets.WithinRentalAssets(ctx, accountID, characterID, func(accounts repo.AccountRepository, characters repo.CharacterRepository, inventories repo.InventoryRepository, _ repo.EquipmentRepository) error {
		character, found, err := characters.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !found || character.AccountID != accountID || character.Stats["delete_flag"] != 0 {
			return fmt.Errorf("total-match rename character owner mismatch")
		}
		a, found, err := accounts.Load(ctx, accountID)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("total-match rename account missing")
		}
		var team TotalTeam
		if err := json.Unmarshal([]byte(a.Metadata[TotalTeamMetadataKey]), &team); err != nil {
			return err
		}
		if team.Name == "" {
			return fmt.Errorf("total-match team missing")
		}
		if team.Name == name {
			return nil
		}
		inventory, found, err := inventories.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("rename inventory missing")
		}
		inventory = repo.CloneInventory(inventory)
		key := fmt.Sprintf("0:%d", slot)
		card, found := inventory.Slots[key]
		if !found || card.ItemID != TotalTeamRenameCardID || card.Count < 1 || (!card.ExpireAt.IsZero() && !card.ExpireAt.After(now)) {
			return fmt.Errorf("valid total-match rename card required")
		}
		switch strings.ToLower(strings.TrimSpace(card.Extra["equipment_lock_state"])) {
		case "1", "2", "active", "locked", "unlocking", "pending_unlock":
			return fmt.Errorf("rename card locked")
		}
		card.Count--
		if card.Count == 0 {
			delete(inventory.Slots, key)
		} else {
			inventory.Slots[key] = card
		}
		inventory.UpdatedAt = now.UTC()
		if err := repo.SaveInventoryFields(ctx, inventories, inventory, repo.InventoryFieldSlots); err != nil {
			return err
		}
		team.Name = name
		return SaveTotalTeam(ctx, accounts, accountID, team, now)
	})
}
