package adventuregroup

import (
	"context"
	"errors"
	repo "longheng.io/server/internal/modules/dnf/repository"
	"time"
)

var ErrExpeditionChanged = errors.New("远征状态已变化，请刷新后重试")

// 请求、队员及收益由既有远征入口规划；事务内再次核对账号与旧状态，避免多次派遣计数。
func CommitBattlePassExpeditionStart(ctx context.Context, g repo.Group, accountID, characterID, expected string, next RuntimeState, now time.Time) error {
	if g.CeraShopAssets == nil || accountID == "" || characterID == "" || now.IsZero() {
		return ErrOwnerUnavailable
	}
	encoded, err := next.Marshal()
	if err != nil {
		return err
	}
	return g.CeraShopAssets.WithinCeraShopAssets(ctx, accountID, characterID, "adventure_group_expedition:"+characterID, func(accounts repo.AccountRepository, chars repo.CharacterRepository, _ repo.InventoryRepository, _ repo.EquipmentRepository, _ repo.SettingsRepository) error {
		account, found, err := accounts.Load(ctx, accountID)
		if err != nil {
			return err
		}
		if !found || account.AccountID != accountID || account.Metadata[RuntimeStateMetadataKey] != expected {
			return ErrExpeditionChanged
		}
		ch, found, err := chars.Load(ctx, characterID)
		if err != nil {
			return err
		}
		if !found || ch.AccountID != accountID || ch.CharacterID != characterID {
			return ErrExpeditionChanged
		}
		if err := repo.SaveAccountMetadataEntry(ctx, accounts, account, RuntimeStateMetadataKey, encoded, now); err != nil {
			return err
		}
		ch = repo.CloneCharacter(ch)
		repo.AddBattlePassCounter(&ch, 12, -1, 1, now)
		ch.UpdatedAt = now
		return repo.SaveCharacterFields(ctx, chars, ch, repo.CharacterFieldStats)
	})
}
