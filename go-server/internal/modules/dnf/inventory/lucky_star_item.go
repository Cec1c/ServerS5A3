package inventory

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/itemexpiration"
	"longheng.io/server/internal/modules/dnf/rental"
	r "longheng.io/server/internal/modules/dnf/repository"
)

var ErrLuckyStarItem = errors.New("invalid lucky star item request")

type LuckyStarItemDefinition struct {
	ItemID        int64
	PVFPath       string
	Amount, Limit uint32
	MinimumLevel  int64
	DailyDelete   bool
}

type LuckyStarItemRequest struct {
	AccountID, CharacterID, RequestID string
	Slot                              int16
	// Resolve receives the locked source item ID; never trust a client amount.
	Resolve    func(int64) (LuckyStarItemDefinition, error)
	Revalidate func() bool
}

type LuckyStarItemResult struct {
	CharacterID string `json:"character_id"`
	Slot        int16  `json:"slot"`
	ItemID      int64  `json:"item_id"`
	Points      uint32 `json:"points"`
	Remaining   int64  `json:"remaining"`
	Replayed    bool   `json:"-"`
}

// UseLuckyStarItem shares the account wallet transaction with rental charges
// and settlement. The item, points and permanent transport receipt commit together.
func (o *Owner) UseLuckyStarItem(ctx context.Context, q LuckyStarItemRequest, now time.Time) (LuckyStarItemResult, error) {
	var result LuckyStarItemResult
	id, err := hex.DecodeString(q.RequestID)
	if o == nil || o.rentalAssets == nil || q.AccountID == "" || q.CharacterID == "" || q.Slot < 1 ||
		q.Resolve == nil || q.Revalidate == nil || now.IsZero() || err != nil || len(id) != 32 {
		return result, ErrLuckyStarItem
	}
	err = o.rentalAssets.WithinRentalAssets(ctx, q.AccountID, q.CharacterID, func(accounts r.AccountRepository, characters r.CharacterRepository, bags r.InventoryRepository, _ r.EquipmentRepository) error {
		if !q.Revalidate() {
			return ErrLuckyStarItem
		}
		actor, found, err := characters.Load(ctx, q.CharacterID)
		if err != nil {
			return err
		}
		if !found || actor.CharacterID != q.CharacterID || actor.AccountID != q.AccountID {
			return ErrLuckyStarItem
		}
		account, found, err := accounts.Load(ctx, q.AccountID)
		if err != nil {
			return err
		}
		if !found || account.AccountID != q.AccountID {
			return ErrAccountNotFound
		}
		account = r.CloneAccount(account)
		key := "lucky_star_item:v1:" + q.RequestID
		if old := account.Metadata[key]; old != "" {
			if json.Unmarshal([]byte(old), &result) != nil || result.CharacterID != q.CharacterID || result.Slot != q.Slot {
				return ErrLuckyStarItem
			}
			result.Replayed = true
			if !q.Revalidate() {
				return ErrLuckyStarItem
			}
			return nil
		}
		bag, found, err := bags.Load(ctx, q.CharacterID)
		if err != nil {
			return err
		}
		if !found {
			return ErrInventoryNotFound
		}
		bag = r.CloneInventory(bag)
		slot := slotKey(r.MainInventoryListType, q.Slot)
		stack, found := bag.Slots[slot]
		if !found || stack.ItemID <= 0 || stack.Count <= 0 || stack.Count > math.MaxInt32 || isEquipmentLocked(stack) {
			return ErrLuckyStarItem
		}
		def, err := q.Resolve(stack.ItemID)
		if err != nil {
			return err
		}
		if def.ItemID != stack.ItemID || def.Amount == 0 || def.Limit == 0 || def.Amount > def.Limit || def.PVFPath == "" || def.MinimumLevel < 0 || int64(actor.Level) < def.MinimumLevel {
			return ErrLuckyStarItem
		}
		if kind := stack.Extra["item_kind"]; kind != "" && kind != "stackable" {
			return ErrLuckyStarItem
		}
		if path := stack.Extra["pvf_path"]; path != "" && !strings.EqualFold(strings.ReplaceAll(path, "\\", "/"), def.PVFPath) {
			return ErrLuckyStarItem
		}
		if !stack.ExpireAt.IsZero() && !now.Before(stack.ExpireAt) {
			return ErrLuckyStarItem
		}
		if def.DailyDelete {
			var expired bool
			stack, _, expired, err = itemexpiration.ReconcileDailyDelete(stack, now)
			if err != nil {
				return err
			}
			if expired {
				return ErrLuckyStarItem
			}
		}
		points, err := rental.Points(account)
		if err != nil {
			return err
		}
		if uint64(points)+uint64(def.Amount) > uint64(def.Limit) {
			return rental.ErrPointLimit
		}
		points += def.Amount
		stack.Count--
		if stack.Count == 0 {
			delete(bag.Slots, slot)
		} else {
			updateStackRawAmount(&stack)
			bag.Slots[slot] = stack
		}
		rental.SetPoints(&account, points)
		result = LuckyStarItemResult{CharacterID: q.CharacterID, Slot: q.Slot, ItemID: stack.ItemID, Points: points, Remaining: stack.Count}
		receipt, err := json.Marshal(result)
		if err != nil {
			return err
		}
		account.Metadata[key] = string(receipt)
		account.UpdatedAt, bag.UpdatedAt = now, now
		if err := r.SaveInventoryFields(ctx, bags, bag, r.InventoryFieldSlots); err != nil {
			return err
		}
		if err := accounts.Save(ctx, account); err != nil {
			return err
		}
		if !q.Revalidate() {
			return ErrLuckyStarItem
		}
		return nil
	})
	if err != nil {
		return LuckyStarItemResult{}, err
	}
	return result, nil
}
