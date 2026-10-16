package inventory

import (
	"context"
	"strings"
	"time"

	"longheng.io/server/internal/modules/dnf/experiencebuff"
	repo "longheng.io/server/internal/modules/dnf/repository"
)

func (o *Owner) recordExperiencePotion(ctx context.Context, cmd Command, characterID string, stack repo.ItemStack) error {
	if o.characters == nil {
		return ErrCharacterAssetTxnRequired
	}
	now := cmd.ExperienceBuffNow.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if !stack.ExpireAt.IsZero() && !now.Before(stack.ExpireAt) {
		return experiencebuff.ErrEffect
	}
	character, found, err := o.characters.Load(ctx, characterID)
	if err != nil {
		return err
	}
	if !found || strings.TrimSpace(cmd.AccountID) == "" || character.AccountID != cmd.AccountID {
		return ErrCharacterRequired
	}
	character = repo.CloneCharacter(character)
	if err := experiencebuff.Record(&character, *cmd.UseExperienceBuff, stack.ItemID, now); err != nil {
		return err
	}
	character.UpdatedAt = now
	return repo.SaveCharacterFields(ctx, o.characters, character, repo.CharacterFieldStats)
}
