package inventory

import (
	"context"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/itemusage"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

// recordItemUse is called only by the successful use owner within a character
// asset UOW. A later inventory/effect save failure rolls the counter back too.
func (o *Owner) recordItemUse(ctx context.Context, cmd Command, characterID string, itemID int64) error {
	if cmd.UseItemUsage == nil {
		return nil
	}
	if o.characters == nil || !o.inItemTransaction {
		return ErrCharacterAssetTxnRequired
	}
	if itemID != int64(cmd.UseItemUsage.ItemID) {
		return itemusage.ErrDefinition
	}
	c, found, err := o.characters.Load(ctx, characterID)
	if err != nil {
		return err
	}
	if !found || strings.TrimSpace(cmd.AccountID) == "" || c.AccountID != cmd.AccountID {
		return ErrCharacterRequired
	}
	c = repo.CloneCharacter(c)
	now := cmd.ItemUsageNow.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if err := itemusage.Consume(&c, *cmd.UseItemUsage, now); err != nil {
		return err
	}
	c.UpdatedAt = now
	return repo.SaveCharacterFields(ctx, o.characters, c, repo.CharacterFieldStats)
}
