package inventory

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/experiencebuff"
	"longheng.io/server/internal/modules/dnf/itemusage"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

var ErrQuestExperiencePotion = errors.New("invalid quest experience potion use")

// Definition and effect are resolved from the server's PVF, never from the
// action packet. Action 194 has no client instance/nonce or item-id field.
type QuestExperiencePotionRequest struct {
	AccountID, CharacterID, PVFPath string
	SlotIndex                       int16
	ItemID                          int64
	Effect                          experiencebuff.Effect
	Usage                           *itemusage.Definition
	Revalidate                      func() bool
}

func (o *Owner) UseQuestExperiencePotion(ctx context.Context, r QuestExperiencePotionRequest, now time.Time) (int64, error) {
	if o == nil || o.repo == nil || o.characters == nil || r.AccountID == "" || r.CharacterID == "" || r.PVFPath == "" || r.ItemID <= 0 || r.SlotIndex < 0 || r.Effect.Kind != experiencebuff.Quest || r.Revalidate == nil || now.IsZero() {
		return 0, ErrQuestExperiencePotion
	}
	if !o.inItemTransaction {
		var remaining int64
		err := o.withinCharacterAssetTransaction(ctx, r.CharacterID, func(tx *Owner) error {
			var err error
			remaining, err = tx.UseQuestExperiencePotion(ctx, r, now)
			return err
		})
		return remaining, err
	}
	if !r.Revalidate() {
		return 0, ErrQuestExperiencePotion
	}
	bag, found, err := o.repo.Load(ctx, r.CharacterID)
	if err != nil {
		return 0, err
	}
	key := "0:" + strconv.Itoa(int(r.SlotIndex))
	stack, exists := bag.Slots[key]
	if !found || !exists || stack.ItemID != r.ItemID || stack.Count <= 0 || !strings.EqualFold(stack.Extra["item_kind"], "stackable") {
		return 0, ErrQuestExperiencePotion
	}
	cmd := Command{AccountID: r.AccountID, UseExperienceBuff: &r.Effect, ExperienceBuffNow: now, UseItemUsage: r.Usage, ItemUsageNow: now}
	if err := o.recordItemUse(ctx, cmd, r.CharacterID, stack.ItemID); err != nil {
		return 0, err
	}
	if err := o.recordExperiencePotion(ctx, cmd, r.CharacterID, stack); err != nil {
		return 0, err
	}
	bag = repo.CloneInventory(bag)
	stack = bag.Slots[key]
	stack.Count--
	if stack.Count == 0 {
		delete(bag.Slots, key)
	} else {
		bag.Slots[key] = stack
	}
	bag.UpdatedAt = now
	if err := repo.SaveInventoryFields(ctx, o.repo, bag, repo.InventoryFieldSlots); err != nil {
		return 0, err
	}
	return stack.Count, nil
}
