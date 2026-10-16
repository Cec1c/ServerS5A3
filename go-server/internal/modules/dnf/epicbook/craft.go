package epicbook

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/itemlock"
	"longheng.io/server/internal/modules/dnf/repository"
)

var ErrRecipe = errors.New("epic book recipe unavailable")
var ErrMaterial = errors.New("epic book material insufficient or invalid")

// ProductProjector applies the authoritative equipment definition and capacity
// rules to the buffered character inventory. It must not perform external writes.
type ProductProjector func(context.Context, repository.CharacterRecord, *repository.InventoryRecord, uint32) error

type CraftResult struct {
	PieceID          uint32
	PieceBalance     uint32
	ChangedSoulSlots []int16
}

func Craft(ctx context.Context, assets repository.EpicBookAssetUnitOfWork, catalog *Catalog, accountID, characterID string, output uint32, now time.Time, grant ProductProjector) (CraftResult, error) {
	if assets == nil || grant == nil {
		return CraftResult{}, repository.ErrEpicBookTransactionUnavailable
	}
	accountID, characterID = strings.TrimSpace(accountID), strings.TrimSpace(characterID)
	if accountID == "" || characterID == "" || now.IsZero() {
		return CraftResult{}, repository.ErrEpicBookOwnership
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return CraftResult{}, err
	}
	recipe, ok := catalog.Lookup(output)
	if !ok || recipe.OutputID != output || recipe.PieceID == 0 {
		return CraftResult{}, ErrRecipe
	}
	var result CraftResult
	err := assets.WithinEpicBookAssets(ctx, accountID, characterID, func(pieces repository.EpicBookPieceDebitor, character repository.CharacterRecord, accounts repository.AccountInventoryRepository, inventories repository.InventoryRepository) error {
		if pieces == nil || accounts == nil || inventories == nil {
			return repository.ErrEpicBookTransactionUnavailable
		}
		if character.CharacterID != characterID || character.AccountID != accountID {
			return repository.ErrEpicBookOwnership
		}
		account, found, err := accounts.Load(ctx, accountID)
		if err != nil {
			return err
		}
		if !found || account.AccountID != accountID {
			return ErrMaterial
		}
		inventory, found, err := inventories.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !found || inventory.CharacterID != characterID {
			return ErrMaterial
		}
		account = repository.CloneAccountInventory(account)
		inventory = repository.CloneInventory(inventory)
		var balance uint32
		var slots []int16
		for _, material := range recipe.Materials {
			if material.ItemID == recipe.PieceID {
				balance, err = pieces.DebitPiece(ctx, recipe.PieceID, material.Count, now)
				if err != nil {
					return err
				}
				continue
			}
			slot, shared := repository.AccountWarehouseSlotForItem(int64(material.ItemID))
			if !shared {
				return ErrRecipe
			}
			key := repository.AccountSharedInventorySlotKey(slot)
			stack, found := account.Slots[key]
			if !found || stack.ItemID != int64(material.ItemID) || stack.Count < int64(material.Count) || stack.Count > math.MaxInt32 || itemlock.IsLocked(stack) {
				return ErrMaterial
			}
			stack.Count -= int64(material.Count)
			// Count is authoritative; preserve unrelated raw item bytes. The
			// transport projection overlays the new count on that raw identity.
			if stack.Count == 0 {
				delete(account.Slots, key)
			} else {
				account.Slots[key] = stack
			}
			slots = append(slots, slot)
		}
		if err := grant(ctx, character, &inventory, output); err != nil {
			return err
		}
		account.UpdatedAt, inventory.UpdatedAt = now.UTC(), now.UTC()
		if err := accounts.Save(ctx, account); err != nil {
			return err
		}
		if err := repository.SaveInventoryFields(ctx, inventories, inventory, repository.InventoryFieldSlots); err != nil {
			return err
		}
		result = CraftResult{PieceID: recipe.PieceID, PieceBalance: balance, ChangedSoulSlots: slots}
		return nil
	})
	if err != nil {
		return CraftResult{}, err
	}
	return result, nil
}
